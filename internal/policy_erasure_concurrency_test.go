package internal

import (
	"context"
	"os"
	"reflect"
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
