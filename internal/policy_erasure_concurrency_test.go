package internal

import (
	"context"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	authv1 "github.com/Muxcore-Media/core/proto/gen/muxcore/auth/v1"
	"github.com/Muxcore-Media/core/sdk/go/module/erasure"
	"github.com/Muxcore-Media/request-media/internal/reqquota"
)

// Pause a real store lookup after it has said the victim is live. If the
// setter does not protect that result through assignment, finish the real
// erasure sweep and OK acknowledgement before allowing the stale save to run.
// If it does protect the result, release the setter so erasure can acquire the
// policy lock. The scheduling branch avoids sleeps and timeout-based inference;
// the assertions concern the resulting policy, acknowledgement and restart.
func TestPolicySaveOverlappingErasureDoesNotRestoreUser(t *testing.T) {
	h := newLedgerHarness(t, erasureProvID)
	m := h.module(t)
	seedUsers(t, m, erasureVictim, erasureBystand)
	writePolicy(t, m, 0o640, policyFixture())
	edit := reqquota.Policy{
		MaxPendingPerUser: 7, MaxPerWeek: 19,
		AutoApproveUsers: []string{"admin-9", erasureBystand, erasureVictim},
	}
	checked, release, saved := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseSave := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseSave)
	go func() {
		defer close(saved)
		m.setQuotaPolicyChecked(edit, func(userID string) (bool, error) {
			erased, err := m.store.UserErased(context.Background(), userID)
			if userID == erasureVictim && err == nil && !erased {
				close(checked)
				<-release
			}
			return erased, err
		})
	}()
	waitPolicySignal(t, "completed live-user lookup", checked)

	// No other worker is running yet, so a free lock means the lookup result
	// is unprotected. Drive precisely the problematic interleaving in that case.
	unprotected := m.cfgMu.TryLock()
	if unprotected {
		m.cfgMu.Unlock()
	}
	erasureID := h.provider.AddErasure(erasureVictim, "")
	r := h.reconciler(t, m)
	type sweepResult struct {
		result erasure.Result
		err    error
	}
	swept := make(chan sweepResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		result, err := r.SweepOnce(ctx)
		swept <- sweepResult{result, err}
	}()
	if !unprotected {
		// The database phase may complete while the setter is paused, but
		// policy removal must wait until the protected save finishes.
		waitFor(t, "committed erasure transaction", func() bool {
			erased, err := m.store.UserErased(context.Background(), erasureVictim)
			return err == nil && erased
		})
		releaseSave()
	}
	select {
	case out := <-swept:
		if out.err != nil || out.result.Failed != 0 || out.result.Errors != 0 {
			t.Fatalf("sweep = %+v, %v", out.result, out.err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("erasure sweep did not complete")
	}
	ack, ok := h.provider.Latest(erasureID, erasureModID)
	if !ok || ack.Outcome != authv1.ErasureOutcome_ERASURE_OUTCOME_OK {
		t.Fatalf("acknowledgement = %+v, present=%v", ack, ok)
	}
	releaseSave()
	waitPolicySignal(t, "policy save", saved)

	want := requestPolicyFile{MaxPendingPerUser: 7, MaxPerWeek: 19, AutoApproveUsers: []string{"admin-9", erasureBystand}}
	assertErasedPolicy(t, m, want)
	if n, err := (erasureOwner{m: m}).Verify(context.Background(), erasure.Tombstone{UserID: erasureVictim}); err != nil || n != 0 {
		t.Errorf("post-ack post-condition = %d, %v", n, err)
	}
	if result := mustSweepOnce(t, r); result.Skipped != 1 || result.Applied != 0 {
		t.Errorf("repeat sweep = %+v", result)
	}
	assertErasedPolicy(t, m, want)
	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted := h.module(t)
	assertErasedPolicy(t, restarted, want)

	// A later ordinary save still updates quotas and retains live IDs while
	// rejecting a serial attempt to put the erased ID back.
	restarted.setQuotaPolicy(reqquota.Policy{MaxPendingPerUser: 8, MaxPerWeek: 21,
		AutoApproveUsers: []string{erasureVictim, erasureBystand}})
	assertErasedPolicy(t, restarted, requestPolicyFile{MaxPendingPerUser: 8, MaxPerWeek: 21,
		AutoApproveUsers: []string{erasureBystand}})
}

func TestStartupRepairsPolicyForAcknowledgedErasure(t *testing.T) {
	h := newLedgerHarness(t, erasureProvID)
	m := h.module(t)
	writePolicy(t, m, 0o640, policyFixture())
	erasureID := h.provider.AddErasure(erasureVictim, "")
	mustSweepOnce(t, h.reconciler(t, m))
	ack, ok := h.provider.Latest(erasureID, erasureModID)
	if !ok || ack.Outcome != authv1.ErasureOutcome_ERASURE_OUTCOME_OK {
		t.Fatalf("fixture acknowledgement = %+v, present=%v", ack, ok)
	}

	// Reproduce a file left by the old stale-save race after the erasure was
	// marked complete. The SDK will skip this record even after a restart.
	writePolicy(t, m, 0o640, policyFixture())
	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted := h.module(t)
	want := policyFixture()
	want.AutoApproveUsers = []string{"admin-9", erasureBystand}
	assertErasedPolicy(t, restarted, want)
	if result := mustSweepOnce(t, h.reconciler(t, restarted)); result.Skipped != 1 || result.Applied != 0 {
		t.Errorf("already acknowledged erasure was reset: %+v", result)
	}
	assertErasedPolicy(t, restarted, want)
}

func TestStartupPolicySanitationFailureIsReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires an unprivileged process to enforce directory write permissions")
	}
	h := newLedgerHarness(t, erasureProvID)
	m := h.module(t)
	writePolicy(t, m, 0o640, policyFixture())
	h.provider.AddErasure(erasureVictim, "")
	mustSweepOnce(t, h.reconciler(t, m))
	writePolicy(t, m, 0o640, policyFixture())
	before, err := os.ReadFile(m.policyPath())
	if err != nil {
		t.Fatal(err)
	}
	// Keep the original store open. Its database and sidecar files remain
	// available, but atomic policy replacement cannot create its temp file.
	if err := os.Chmod(h.dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(h.dir, 0o700) })
	restarted := NewModule(Config{ID: erasureModID, DataDir: h.dir,
		Authz: allowAllAuthz(), Identity: testResolver{}, ErasureDialer: h.dialer})
	if err := restarted.Init(context.Background()); err == nil || !strings.Contains(err.Error(), "sanitize auto-approve policy file") {
		t.Fatalf("Init = %v, want a policy sanitation failure", err)
	}
	if restarted.store != nil {
		t.Error("failed initialization retained its request store")
	}
	after, err := os.ReadFile(m.policyPath())
	if err != nil || string(after) != string(before) {
		t.Errorf("failed atomic write changed the original policy: err=%v", err)
	}
	if err := os.Chmod(h.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	recovered := h.module(t)
	want := policyFixture()
	want.AutoApproveUsers = []string{"admin-9", erasureBystand}
	assertErasedPolicy(t, recovered, want)
}

func TestStartupPolicySanitationReportsErasureLookupFailure(t *testing.T) {
	h := newLedgerHarness(t, erasureProvID)
	m := h.module(t)
	writePolicy(t, m, 0o640, policyFixture())
	before, err := os.ReadFile(m.policyPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.dropErasedAutoApprove(context.Background()); err == nil {
		t.Error("closed erasure store was reported as successful sanitation")
	}
	after, err := os.ReadFile(m.policyPath())
	if err != nil || string(after) != string(before) {
		t.Errorf("lookup failure changed the policy file: err=%v", err)
	}
}

func TestStartupPolicySanitationRejectsUnreadableOrInvalidPolicy(t *testing.T) {
	for _, invalidJSON := range []bool{false, true} {
		name := "unreadable"
		if invalidJSON {
			name = "invalid-json"
		}
		t.Run(name, func(t *testing.T) {
			h := newLedgerHarness(t, erasureProvID)
			m := h.module(t)
			writePolicy(t, m, 0o640, policyFixture())
			h.provider.AddErasure(erasureVictim, "")
			mustSweepOnce(t, h.reconciler(t, m))
			path := m.policyPath()
			if err := m.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			if invalidJSON {
				if err := os.WriteFile(path, []byte(`{"autoApproveUsers":["`+erasureVictim+`"`), 0o640); err != nil {
					t.Fatal(err)
				}
			} else {
				// A directory causes a deterministic read error even under uid 0.
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			restarted := NewModule(Config{ID: erasureModID, DataDir: h.dir,
				Authz: allowAllAuthz(), Identity: testResolver{}, ErasureDialer: h.dialer})
			if len(restarted.autoApproveUsers) != 0 {
				t.Fatal("fixture must have an empty environment policy")
			}
			if err := restarted.Init(context.Background()); err == nil || !strings.Contains(err.Error(), "read auto-approve policy file") {
				t.Fatalf("Init = %v, want policy read/parse failure", err)
			}
			if restarted.store != nil {
				t.Error("failed initialization retained its request store")
			}
		})
	}
}

func waitPolicySignal(t *testing.T, what string, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(20 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func assertErasedPolicy(t *testing.T, m *Module, want requestPolicyFile) {
	t.Helper()
	p := m.getQuotaPolicy()
	got := requestPolicyFile{MaxPendingPerUser: p.MaxPendingPerUser, MaxPerWeek: p.MaxPerWeek, AutoApproveUsers: p.AutoApproveUsers}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("in-memory policy = %+v, want %+v", got, want)
	}
	if got := readPolicy(t, m); !reflect.DeepEqual(got, want) {
		t.Errorf("persisted policy = %+v, want %+v", got, want)
	}
	if info, err := os.Stat(m.policyPath()); err != nil || info.Mode().Perm() != 0o640 {
		t.Errorf("policy file mode changed: info=%v, err=%v", info, err)
	}
}
