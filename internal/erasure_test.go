package internal

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	authv1 "github.com/Muxcore-Media/core/proto/gen/muxcore/auth/v1"
	discoveryv1 "github.com/Muxcore-Media/core/proto/gen/muxcore/discovery/v1"
	eventsv1 "github.com/Muxcore-Media/core/proto/gen/muxcore/events/v1"
	"github.com/Muxcore-Media/core/sdk/go/module/erasure"
	"github.com/Muxcore-Media/core/sdk/go/module/erasure/erasuretest"
	"github.com/Muxcore-Media/request-media/internal/authn"
	"github.com/Muxcore-Media/request-media/internal/reqquota"
	"github.com/Muxcore-Media/request-media/internal/reqstore"
	requestmedia "github.com/Muxcore-Media/request-media/proto/requestmedia"
)

const (
	erasureModID   = "request-media-test"
	erasureProvID  = "auth-local"
	erasureVictim  = "victim-0001"
	erasureBystand = "bystander-0002"
)

var allStatuses = []string{
	StatusWatchlisted, StatusPending, StatusDenied, // deleted on erasure
	StatusRequested, StatusAdded, StatusWorkflow, "available", // household record
}

func isDeletedStatus(st string) bool {
	return st == StatusWatchlisted || st == StatusPending || st == StatusDenied
}

// TestDeletedOnErasureMatchesStatusConstants pins the store's list to the
// module's status constants: the ADR names three states to delete.
func TestDeletedOnErasureMatchesStatusConstants(t *testing.T) {
	want := []string{StatusWatchlisted, StatusPending, StatusDenied}
	if !reflect.DeepEqual(reqstore.DeletedOnErasure, want) {
		t.Fatalf("reqstore.DeletedOnErasure = %v, want %v", reqstore.DeletedOnErasure, want)
	}
}

// ledgerHarness is a fake identity provider (erasuretest) behind real mTLS,
// discovered through a fake core, plus the module's data directory.
type ledgerHarness struct {
	pki      *erasuretest.PKI
	provider *erasuretest.Provider
	dialer   *erasure.ProviderDialer
	dir      string
}

// newLedgerHarness serves the provider under certificate CN providerCN while
// discovery advertises it as auth-local, so a mismatch is the "wrong CN"
// case. The module's own certificate has CN erasureModID.
func newLedgerHarness(t *testing.T, providerCN string) *ledgerHarness {
	t.Helper()
	for _, k := range []string{"MUXCORE_INSECURE_DISABLE_TLS", "MUXCORE_DEV_TLS_SKIP", "MUXCORE_GRPC_INSECURE",
		"MUXCORE_PROFILE", "MUXCORE_MESH_DIAL_LOCAL", "REQUEST_TRUST_CALLER_HEADER", "REQUEST_AUTO_APPROVE_USERS",
		erasure.EnvSweepInterval} {
		t.Setenv(k, "")
	}
	h := &ledgerHarness{pki: erasuretest.NewPKI(t), provider: erasuretest.NewProvider(), dir: t.TempDir()}
	h.provider.Allowed = map[string]bool{erasureModID: true}
	sc, sk := h.pki.Issue(t, providerCN, providerCN, "localhost")
	addr := erasuretest.ServeTLS(t, h.provider, sc, sk, h.pki.CAFile)
	cc, ck := h.pki.Issue(t, erasureModID)
	h.dialer = &erasure.ProviderDialer{
		Discovery: erasuretest.NewDiscovery(erasuretest.Module(erasureProvID, addr)),
		CertFile:  cc, KeyFile: ck, CAFile: h.pki.CAFile,
	}
	// The module's own gRPC server needs a mesh identity when it is Started.
	t.Setenv("MUXCORE_TLS_CA", h.pki.CAFile)
	t.Setenv("MUXCORE_TLS_CERT", cc)
	t.Setenv("MUXCORE_TLS_KEY", ck)
	return h
}

func (h *ledgerHarness) module(t *testing.T) *Module {
	t.Helper()
	noApproval := false
	m := NewModule(Config{
		ID: erasureModID, GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0", DataDir: h.dir,
		RequireApproval: &noApproval, Authz: allowAllAuthz(), Identity: testResolver{},
		ErasureDialer: h.dialer,
	})
	if err := m.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })
	return m
}

// reconciler builds the SDK reconciler around the module's real Owner.
func (h *ledgerHarness) reconciler(t *testing.T, m *Module) *erasure.Reconciler {
	t.Helper()
	r, err := erasure.New(erasure.Config{
		Owner: erasureOwner{m: m}, Dialer: h.dialer, Interval: time.Minute,
		AckBackoff: time.Millisecond, RetryBackoff: 10 * time.Millisecond, CallTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func sweepOnce(t *testing.T, r *erasure.Reconciler) (erasure.Result, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return r.SweepOnce(ctx)
}

func mustSweepOnce(t *testing.T, r *erasure.Reconciler) erasure.Result {
	t.Helper()
	res, err := sweepOnce(t, r)
	if err != nil {
		t.Fatalf("sweep: %v (%+v)", err, res)
	}
	return res
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// seedUsers stores one request per status for every user and claims the
// ready notification for each, directly through the module's save path.
func seedUsers(t *testing.T, m *Module, users ...string) {
	t.Helper()
	for _, u := range users {
		for i, st := range allStatuses {
			id := u + "-" + st
			if err := m.saveRequest(&requestRecord{
				ID: id, ItemType: "movie", Title: "Title " + id, TMDBID: int32(100 + i), Status: st, RequestedBy: u,
			}); err != nil {
				t.Fatalf("seed %s: %v", id, err)
			}
			if _, err := m.store.ClaimReadyNotified(id); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func writePolicy(t *testing.T, m *Module, mode os.FileMode, f requestPolicyFile) {
	t.Helper()
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.policyPath(), b, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(m.policyPath(), mode); err != nil {
		t.Fatal(err)
	}
	m.loadPolicyFile()
}

func readPolicy(t *testing.T, m *Module) requestPolicyFile {
	t.Helper()
	b, err := os.ReadFile(m.policyPath())
	if err != nil {
		t.Fatal(err)
	}
	var f requestPolicyFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("policy file: %v\n%s", err, b)
	}
	return f
}

// dbOwners maps request id to requested_by as stored in SQLite.
func dbOwners(t *testing.T, m *Module) map[string]string {
	t.Helper()
	list, err := m.store.List()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, r := range list {
		out[r.ID] = r.RequestedBy
	}
	return out
}

func memOwners(m *Module) map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string]string{}
	for id, r := range m.requests {
		out[id] = r.RequestedBy
	}
	return out
}

func requireVictimDisposition(t *testing.T, owners map[string]string, where string) {
	t.Helper()
	for _, st := range allStatuses {
		v, b := erasureVictim+"-"+st, erasureBystand+"-"+st
		got, ok := owners[v]
		switch {
		case isDeletedStatus(st) && ok:
			t.Errorf("%s: victim %s row survived (requested_by %q)", where, st, got)
		case !isDeletedStatus(st) && (!ok || got != reqstore.AnonymisedUser):
			t.Errorf("%s: victim %s row = %q (present=%v), want anonymised", where, st, got, ok)
		}
		if owners[b] != erasureBystand {
			t.Errorf("%s: bystander %s row = %q, want intact", where, st, owners[b])
		}
	}
	for id, by := range owners {
		if by == erasureVictim {
			t.Errorf("%s: %s still names the victim", where, id)
		}
	}
}

func policyFixture() requestPolicyFile {
	return requestPolicyFile{
		MaxPendingPerUser: 4, MaxPerWeek: 9,
		AutoApproveUsers: []string{"admin-9", erasureVictim, erasureBystand},
	}
}

func TestErasureDispositionAndPolicyFile(t *testing.T) {
	h := newLedgerHarness(t, erasureProvID)
	m := h.module(t)
	seedUsers(t, m, erasureVictim, erasureBystand)
	writePolicy(t, m, 0o644, policyFixture())
	id := h.provider.AddErasure(erasureVictim, "")
	r := h.reconciler(t, m)

	res := mustSweepOnce(t, r)
	if res.Seen != 1 || res.Applied != 1 || res.Acked != 1 || res.Failed != 0 {
		t.Fatalf("result %+v", res)
	}
	requireVictimDisposition(t, dbOwners(t, m), "sqlite")
	requireVictimDisposition(t, memOwners(m), "memory")

	// request-policy.json: id removed, others and the format preserved.
	f := readPolicy(t, m)
	if want := []string{"admin-9", erasureBystand}; !reflect.DeepEqual(f.AutoApproveUsers, want) {
		t.Errorf("autoApproveUsers = %v, want %v", f.AutoApproveUsers, want)
	}
	if f.MaxPendingPerUser != 4 || f.MaxPerWeek != 9 {
		t.Errorf("quota fields changed: %+v", f)
	}
	st, err := os.Stat(m.policyPath())
	if err != nil || st.Mode().Perm() != 0o644 {
		t.Errorf("policy file mode = %v (%v), want 0644", st.Mode().Perm(), err)
	}
	raw, _ := os.ReadFile(m.policyPath())
	if strings.Contains(string(raw), erasureVictim) {
		t.Errorf("policy file still names the victim:\n%s", raw)
	}
	if !strings.Contains(string(raw), "\n  \"maxPendingPerUser\": 4,") {
		t.Errorf("policy file format changed:\n%s", raw)
	}
	if left, _ := filepath.Glob(filepath.Join(h.dir, policyTempPrefix+"*")); len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
	if p := m.getQuotaPolicy(); !reflect.DeepEqual(p.AutoApproveUsers, []string{"admin-9", erasureBystand}) {
		t.Errorf("in-memory auto-approve = %v", p.AutoApproveUsers)
	}

	// Acknowledged OK with the counts; applied record is complete.
	a, ok := h.provider.Latest(id, erasureModID)
	if !ok || a.Outcome != authv1.ErasureOutcome_ERASURE_OUTCOME_OK {
		t.Fatalf("ack = %+v (%v)", a, ok)
	}
	wantCounts := map[string]int64{
		reqstore.CountRequestsDeleted: 3, reqstore.CountReadyDeleted: 3,
		reqstore.CountRequestsAnonymised: 4, reqstore.CountAutoApprove: 1,
	}
	if !reflect.DeepEqual(a.Counts, wantCounts) {
		t.Errorf("counts = %v, want %v", a.Counts, wantCounts)
	}
	if done, err := m.store.ErasureComplete(context.Background(), id); err != nil || !done {
		t.Fatalf("ErasureComplete = %v, %v", done, err)
	}
	if n, err := (erasureOwner{m: m}).Verify(context.Background(), erasure.Tombstone{UserID: erasureVictim}); err != nil || n != 0 {
		t.Fatalf("post-condition = %d, %v", n, err)
	}

	// A second sweep is a no-op: nothing applied, nothing rewritten.
	beforeDB, beforePolicy := dbOwners(t, m), readPolicy(t, m)
	statBefore, _ := os.Stat(m.policyPath())
	res = mustSweepOnce(t, r)
	if res.Applied != 0 || res.Skipped != 1 {
		t.Fatalf("second sweep %+v, want a no-op", res)
	}
	if !reflect.DeepEqual(dbOwners(t, m), beforeDB) || !reflect.DeepEqual(readPolicy(t, m), beforePolicy) {
		t.Error("second sweep changed state")
	}
	if statAfter, _ := os.Stat(m.policyPath()); !statAfter.ModTime().Equal(statBefore.ModTime()) {
		t.Error("second sweep rewrote the policy file")
	}
}

// TestErasureUserOnlyInEnvAutoApproveList: an id that comes from
// REQUEST_AUTO_APPROVE_USERS (which the module cannot rewrite) is dropped at
// runtime and on every restart.
func TestErasureUserOnlyInEnvAutoApproveList(t *testing.T) {
	h := newLedgerHarness(t, erasureProvID)
	t.Setenv("REQUEST_AUTO_APPROVE_USERS", "admin-9,"+erasureVictim)
	m := h.module(t)
	if p := m.getQuotaPolicy(); len(p.AutoApproveUsers) != 2 {
		t.Fatalf("env list = %v", p.AutoApproveUsers)
	}
	id := h.provider.AddErasure(erasureVictim, "")
	mustSweepOnce(t, h.reconciler(t, m))
	if p := m.getQuotaPolicy(); !reflect.DeepEqual(p.AutoApproveUsers, []string{"admin-9"}) {
		t.Fatalf("in-memory list after erasure = %v", p.AutoApproveUsers)
	}
	if _, err := os.Stat(m.policyPath()); err == nil {
		t.Error("an absent policy file was created")
	}
	if _, ok := h.provider.Latest(id, erasureModID); !ok {
		t.Fatal("not acknowledged")
	}
	// Restart: the environment still lists the id; Init drops it.
	_ = m.Stop(context.Background())
	m2 := h.module(t)
	if p := m2.getQuotaPolicy(); !reflect.DeepEqual(p.AutoApproveUsers, []string{"admin-9"}) {
		t.Fatalf("list after restart = %v", p.AutoApproveUsers)
	}
	// An admin edit cannot put it back either.
	m2.setQuotaPolicy(reqquota.Policy{AutoApproveUsers: []string{"admin-9", erasureVictim}})
	if p := m2.getQuotaPolicy(); !reflect.DeepEqual(p.AutoApproveUsers, []string{"admin-9"}) {
		t.Fatalf("list after edit = %v", p.AutoApproveUsers)
	}
	if f := readPolicy(t, m2); strings.Contains(strings.Join(f.AutoApproveUsers, ","), erasureVictim) {
		t.Fatalf("policy file after edit = %+v", f)
	}
}

// TestErasureCrashBetweenPhases: the process stops after the SQLite
// transaction committed and before the policy file was edited. The tombstone
// must not count as applied, and the next sweep (after a restart) finishes
// the file step and acknowledges.
func TestErasureCrashBetweenPhases(t *testing.T) {
	h := newLedgerHarness(t, erasureProvID)
	m := h.module(t)
	seedUsers(t, m, erasureVictim, erasureBystand)
	writePolicy(t, m, 0o600, policyFixture())
	id := h.provider.AddErasure(erasureVictim, "")
	ctx := context.Background()

	// Phase 1 only: exactly the state a crash leaves behind.
	if _, err := m.store.EraseUser(ctx, id, erasureVictim, ""); err != nil {
		t.Fatal(err)
	}
	owner := erasureOwner{m: m}
	if applied, err := owner.Applied(ctx, id); err != nil || applied {
		t.Fatalf("Applied after phase 1 = %v, %v; must be false", applied, err)
	}
	if n, err := owner.Verify(ctx, erasure.Tombstone{UserID: erasureVictim}); err != nil || n == 0 {
		t.Fatalf("Verify after phase 1 = %d, %v; the id is still in the file", n, err)
	}
	if f := readPolicy(t, m); !strings.Contains(strings.Join(f.AutoApproveUsers, ","), erasureVictim) {
		t.Fatal("fixture: victim not in the policy file")
	}
	// Late create is already refused: the erasure row exists.
	if _, err := m.RequestMovie(authCtx(erasureVictim), &requestmedia.RequestMovieRequest{TmdbId: 1, Title: "X"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("create between phases = %v", err)
	}

	// Restart: the process is gone; a new Module opens the same files.
	_ = m.Stop(ctx)
	m2 := h.module(t)
	if applied, _ := (erasureOwner{m: m2}).Applied(ctx, id); applied {
		t.Fatal("Applied is true after restart with the file step pending")
	}
	r := h.reconciler(t, m2)
	res := mustSweepOnce(t, r)
	if res.Applied != 1 || res.Acked != 1 {
		t.Fatalf("recovery sweep %+v", res)
	}
	a, ok := h.provider.Latest(id, erasureModID)
	if !ok || a.Outcome != authv1.ErasureOutcome_ERASURE_OUTCOME_OK {
		t.Fatalf("ack = %+v (%v)", a, ok)
	}
	// Counts of the original transaction survive the crash.
	if a.Counts[reqstore.CountRequestsDeleted] != 3 || a.Counts[reqstore.CountRequestsAnonymised] != 4 || a.Counts[reqstore.CountAutoApprove] != 1 {
		t.Errorf("counts = %v", a.Counts)
	}
	f := readPolicy(t, m2)
	if !reflect.DeepEqual(f.AutoApproveUsers, []string{"admin-9", erasureBystand}) {
		t.Errorf("autoApproveUsers = %v", f.AutoApproveUsers)
	}
	requireVictimDisposition(t, dbOwners(t, m2), "sqlite")
	if st, _ := os.Stat(m2.policyPath()); st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
	if done, _ := m2.store.ErasureComplete(ctx, id); !done {
		t.Error("record not complete after recovery")
	}
}

// TestErasurePolicyFileFailureIsRetried: the file step fails (corrupt JSON).
// The data is already erased, but the tombstone is acknowledged FAILED and not
// applied; once the file is readable the next sweep completes.
func TestErasurePolicyFileFailureIsRetried(t *testing.T) {
	h := newLedgerHarness(t, erasureProvID)
	m := h.module(t)
	seedUsers(t, m, erasureVictim, erasureBystand)
	if err := os.WriteFile(m.policyPath(), []byte(`{"autoApproveUsers": ["`+erasureVictim+`"`), 0o600); err != nil {
		t.Fatal(err)
	}
	id := h.provider.AddErasure(erasureVictim, "")
	r := h.reconciler(t, m)

	if _, err := sweepOnce(t, r); err == nil {
		t.Fatal("sweep succeeded with an unusable policy file")
	}
	a, ok := h.provider.Latest(id, erasureModID)
	if !ok || a.Outcome != authv1.ErasureOutcome_ERASURE_OUTCOME_FAILED || a.Detail != detailPolicyFile {
		t.Fatalf("ack = %+v (%v), want FAILED/%s", a, ok, detailPolicyFile)
	}
	if applied, _ := (erasureOwner{m: m}).Applied(context.Background(), id); applied {
		t.Fatal("applied although the file step failed")
	}
	requireVictimDisposition(t, dbOwners(t, m), "sqlite")

	writePolicy(t, m, 0o600, policyFixture())
	res := mustSweepOnce(t, r)
	if res.Applied != 1 {
		t.Fatalf("retry %+v", res)
	}
	if a, _ := h.provider.Latest(id, erasureModID); a.Outcome != authv1.ErasureOutcome_ERASURE_OUTCOME_OK {
		t.Fatalf("ack after retry = %+v", a)
	}
	if f := readPolicy(t, m); strings.Contains(strings.Join(f.AutoApproveUsers, ","), erasureVictim) {
		t.Fatalf("policy file after retry: %+v", f)
	}
}

// TestErasureMidTransactionFailure injects a failure at the end of the SQLite
// transaction (through a second connection; the production code has no
// seam). Nothing may change, including the policy file and the cache, and the
// next sweep completes.
func TestErasureMidTransactionFailure(t *testing.T) {
	h := newLedgerHarness(t, erasureProvID)
	m := h.module(t)
	seedUsers(t, m, erasureVictim, erasureBystand)
	writePolicy(t, m, 0o600, policyFixture())
	id := h.provider.AddErasure(erasureVictim, "")
	r := h.reconciler(t, m)
	dbBefore, memBefore := dbOwners(t, m), memOwners(m)

	raw, err := sql.Open("sqlite", filepath.Join(h.dir, "requests.db")+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	if _, err := raw.Exec(`CREATE TRIGGER fail_erasure BEFORE INSERT ON erasure_applied
		BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := sweepOnce(t, r); err == nil {
		t.Fatal("sweep succeeded despite the injected failure")
	}
	a, ok := h.provider.Latest(id, erasureModID)
	if !ok || a.Outcome != authv1.ErasureOutcome_ERASURE_OUTCOME_FAILED || a.Detail != erasure.DetailApplyFailed {
		t.Fatalf("ack = %+v (%v)", a, ok)
	}
	if !reflect.DeepEqual(dbOwners(t, m), dbBefore) {
		t.Error("SQLite rows changed despite the rollback")
	}
	if !reflect.DeepEqual(memOwners(m), memBefore) {
		t.Error("in-memory cache changed despite the rollback")
	}
	if f := readPolicy(t, m); !reflect.DeepEqual(f.AutoApproveUsers, policyFixture().AutoApproveUsers) {
		t.Errorf("policy file changed despite the rollback: %v", f.AutoApproveUsers)
	}
	if erased, _ := m.store.UserErased(context.Background(), erasureVictim); erased {
		t.Error("rolled-back erasure is recorded")
	}

	if _, err := raw.Exec(`DROP TRIGGER fail_erasure`); err != nil {
		t.Fatal(err)
	}
	mustSweepOnce(t, r)
	requireVictimDisposition(t, dbOwners(t, m), "sqlite")
	if a, _ := h.provider.Latest(id, erasureModID); a.Outcome != authv1.ErasureOutcome_ERASURE_OUTCOME_OK {
		t.Fatalf("ack after retry = %+v", a)
	}
}

// TestErasureRestartPersistence: the record and the refusal survive a restart,
// and a re-listed tombstone is not applied again.
func TestErasureRestartPersistence(t *testing.T) {
	h := newLedgerHarness(t, erasureProvID)
	m := h.module(t)
	seedUsers(t, m, erasureVictim, erasureBystand)
	id := h.provider.AddErasure(erasureVictim, "")
	mustSweepOnce(t, h.reconciler(t, m))
	_ = m.Stop(context.Background())

	m2 := h.module(t)
	if applied, err := (erasureOwner{m: m2}).Applied(context.Background(), id); err != nil || !applied {
		t.Fatalf("Applied after restart = %v, %v", applied, err)
	}
	requireVictimDisposition(t, memOwners(m2), "memory after restart")
	_, err := m2.RequestMovie(authCtx(erasureVictim), &requestmedia.RequestMovieRequest{TmdbId: 7, Title: "Late"})
	if status.Code(err) != codes.PermissionDenied || !strings.HasPrefix(status.Convert(err).Message(), CodeUserErased) {
		t.Fatalf("late create after restart = %v", err)
	}
	// Another sweep (the provider's ack already recorded) applies nothing.
	res := mustSweepOnce(t, h.reconciler(t, m2))
	if res.Applied != 0 || res.Skipped != 1 {
		t.Fatalf("sweep after restart %+v", res)
	}
}

// TestLateCreateAndApproveRefused covers every entry point an erased id could
// use, including a token that still resolves.
func TestLateCreateAndApproveRefused(t *testing.T) {
	h := newLedgerHarness(t, erasureProvID)
	m := h.module(t)
	// A bystander's pending request for the erased user to try to approve.
	seedUsers(t, m, erasureVictim, erasureBystand)
	h.provider.AddErasure(erasureVictim, "")
	mustSweepOnce(t, h.reconciler(t, m))

	wantRefused := func(what string, err error) {
		t.Helper()
		if status.Code(err) != codes.PermissionDenied || !strings.HasPrefix(status.Convert(err).Message(), CodeUserErased) {
			t.Errorf("%s = %v, want PermissionDenied %s", what, err, CodeUserErased)
		}
	}
	ctx := authCtx(erasureVictim)
	_, err := m.RequestMovie(ctx, &requestmedia.RequestMovieRequest{TmdbId: 1, Title: "A"})
	wantRefused("RequestMovie", err)
	_, err = m.RequestTV(ctx, &requestmedia.RequestTVRequest{TmdbId: 2, Title: "B"})
	wantRefused("RequestTV", err)
	_, err = m.AddToWatchlist(ctx, &requestmedia.AddToWatchlistRequest{TmdbId: 3, Title: "C"})
	wantRefused("AddToWatchlist", err)
	_, err = m.ApproveRequest(ctx, &requestmedia.ApproveRequestRequest{RequestId: erasureBystand + "-" + StatusPending})
	wantRefused("ApproveRequest", err)
	_, err = m.DenyRequest(ctx, &requestmedia.DenyRequestRequest{RequestId: erasureBystand + "-" + StatusPending})
	wantRefused("DenyRequest", err)
	_, err = m.RemoveFromWatchlist(ctx, &requestmedia.RemoveFromWatchlistRequest{RequestId: erasureBystand + "-" + StatusWatchlisted})
	wantRefused("RemoveFromWatchlist", err)

	// The bystander is unaffected by all of that.
	if got := dbOwners(t, m)[erasureBystand+"-"+StatusPending]; got != erasureBystand {
		t.Fatalf("bystander row = %q", got)
	}
	if _, err := m.RequestMovie(authCtx(erasureBystand), &requestmedia.RequestMovieRequest{TmdbId: 4, Title: "D"}); err != nil {
		t.Fatalf("bystander create: %v", err)
	}

	// The store refuses even a write that raced past the module check.
	late := &requestRecord{ID: "raced", ItemType: "movie", Status: StatusPending, RequestedBy: erasureVictim}
	wantRefused("saveRequest", m.saveRequest(late))
	if _, ok := memOwners(m)["raced"]; ok {
		t.Error("refused request is cached in memory")
	}
	if _, ok := dbOwners(t, m)["raced"]; ok {
		t.Error("refused request is stored")
	}

	// A token that still resolves for the erased user is not honoured.
	if id, err := m.identityResolver().ResolveToken(context.Background(), "tok-"+erasureVictim); id != nil || err != nil {
		t.Errorf("erased user's token resolved: %+v, %v", id, err)
	}
	if id, err := m.identityResolver().ResolveToken(context.Background(), "tok-"+erasureBystand); err != nil || id == nil || id.ID != erasureBystand {
		t.Errorf("bystander token = %+v, %v", id, err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/request", strings.NewReader(`{"tmdbId":5,"title":"E"}`))
	setBearer(r, erasureVictim)
	w := httptest.NewRecorder()
	m.handleRequest(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("HTTP create with the erased user's token = %d %s, want 401", w.Code, w.Body.String())
	}

	// The legacy header path never reaches the resolver; the handler refuses
	// with the stable code.
	t.Setenv("MUXCORE_INSECURE_DISABLE_TLS", "true")
	t.Setenv(authn.EnvTrustCallerHeader, "1")
	r = httptest.NewRequest(http.MethodPost, "/api/request", strings.NewReader(`{"tmdbId":6,"title":"F"}`))
	r.Header.Set("X-Caller-Id", erasureVictim)
	w = httptest.NewRecorder()
	m.handleRequest(w, r)
	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusForbidden || body["code"] != CodeUserErased {
		t.Errorf("legacy-header create = %d %s, want 403 %s", w.Code, w.Body.String(), CodeUserErased)
	}
}

// TestLedgerFromWrongProviderIsNeverActedOn: discovery names auth-local but the
// server's certificate CN is another module of the same mesh CA.
func TestLedgerFromWrongProviderIsNeverActedOn(t *testing.T) {
	h := newLedgerHarness(t, "evil-module")
	// The certificate must still be valid for auth-local as a SAN, so only the
	// CN pin can reject it: issue with that SAN explicitly.
	sc, sk := h.pki.Issue(t, "evil-module", erasureProvID, "localhost")
	addr := erasuretest.ServeTLS(t, h.provider, sc, sk, h.pki.CAFile)
	h.dialer.Discovery = erasuretest.NewDiscovery(erasuretest.Module(erasureProvID, addr))

	m := h.module(t)
	seedUsers(t, m, erasureVictim, erasureBystand)
	writePolicy(t, m, 0o600, policyFixture())
	id := h.provider.AddErasure(erasureVictim, "")
	dbBefore, memBefore := dbOwners(t, m), memOwners(m)

	res, err := sweepOnce(t, h.reconciler(t, m))
	if err == nil {
		t.Fatalf("sweep against a wrong-CN provider succeeded: %+v", res)
	}
	if !reflect.DeepEqual(dbOwners(t, m), dbBefore) || !reflect.DeepEqual(memOwners(m), memBefore) {
		t.Error("rows changed on the strength of an unverified ledger")
	}
	if f := readPolicy(t, m); !reflect.DeepEqual(f.AutoApproveUsers, policyFixture().AutoApproveUsers) {
		t.Errorf("policy file changed: %v", f.AutoApproveUsers)
	}
	if erased, _ := m.store.UserErased(context.Background(), erasureVictim); erased {
		t.Error("erasure recorded")
	}
	if _, ok := h.provider.Latest(id, erasureModID); ok {
		t.Error("an acknowledgement reached the wrong provider")
	}
	if list, ack := h.provider.Calls(); list != 0 || ack != 0 {
		t.Errorf("wrong provider saw %d list / %d ack calls; the handshake must fail first", list, ack)
	}
}

// fakeCore is core's DiscoveryService and EventService. Its Subscribe streams
// a forged identity.user.deleted event (and the hint event) on every
// subscription, whatever type was asked for.
type fakeCore struct {
	discoveryv1.UnimplementedDiscoveryServiceServer
	eventsv1.UnimplementedEventServiceServer
	identityAddr string
	forgedUser   string
	delivered    atomic.Int32
}

func (c *fakeCore) FindByCapability(_ context.Context, req *discoveryv1.FindByCapabilityRequest) (*discoveryv1.FindByCapabilityResponse, error) {
	if req.GetCapability() != "identity" {
		return &discoveryv1.FindByCapabilityResponse{}, nil
	}
	return &discoveryv1.FindByCapabilityResponse{Modules: []*discoveryv1.ModuleInfoProto{
		{Id: erasureProvID, HttpAddr: c.identityAddr, Capabilities: []string{"identity"}, State: "running"},
	}}, nil
}

func (c *fakeCore) Subscribe(_ *eventsv1.SubscribeRequest, s grpc.ServerStreamingServer[eventsv1.Event]) error {
	payload, _ := json.Marshal(map[string]string{"user_id": c.forgedUser, "erasure_id": "forged", "tenant_id": ""})
	for _, typ := range []string{"identity.user.deleted", "identity.erasure.recorded", "auth.user.deleted"} {
		if err := s.Send(&eventsv1.Event{Id: "forged-" + typ, Type: typ, Source: erasureProvID, Payload: payload}); err != nil {
			return err
		}
		c.delivered.Add(1)
	}
	<-s.Context().Done()
	return nil
}

// TestLifecycleReconcilesLedgerAndIgnoresForgedEvents runs the real Start/Stop
// path against a fake core (dev profile, plaintext): the module discovers the
// identity provider through core, erases the ledger's user at startup and
// never acts on forged identity.user.deleted events. Stop leaves no goroutine.
func TestLifecycleReconcilesLedgerAndIgnoresForgedEvents(t *testing.T) {
	t.Setenv("MUXCORE_INSECURE_DISABLE_TLS", "true")
	for _, k := range []string{"MUXCORE_TLS_CERT", "MUXCORE_TLS_KEY", "MUXCORE_TLS_CA", "MUXCORE_PROFILE",
		"MUXCORE_GRPC_INSECURE", "MUXCORE_DEV_TLS_SKIP", "REQUEST_AUTO_APPROVE_USERS", erasure.EnvSweepInterval} {
		t.Setenv(k, "")
	}
	provider := erasuretest.NewProvider()
	provider.Allowed = map[string]bool{erasureModID: true}
	provAddr := erasuretest.ServePlain(t, provider)
	// Forged event names the BYSTANDER; the ledger names only the victim.
	core := &fakeCore{identityAddr: provAddr, forgedUser: erasureBystand}
	coreAddr := startGRPC(t, func(s *grpc.Server) {
		discoveryv1.RegisterDiscoveryServiceServer(s, core)
		eventsv1.RegisterEventServiceServer(s, core)
	})
	t.Setenv("MUXCORE_GRPC_ADDR", coreAddr)

	noApproval := false
	m := NewModule(Config{
		ID: erasureModID, GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0", DataDir: t.TempDir(),
		RequireApproval: &noApproval, Authz: allowAllAuthz(), Identity: testResolver{},
	})
	if err := m.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	seedUsers(t, m, erasureVictim, erasureBystand)
	writePolicy(t, m, 0o600, policyFixture())
	id := provider.AddErasure(erasureVictim, "")

	// Let goroutines from earlier tests settle before taking the baseline.
	time.Sleep(200 * time.Millisecond)
	base := runtime.NumGoroutine()
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, "the startup sweep to acknowledge", func() bool {
		a, ok := provider.Latest(id, erasureModID)
		return ok && a.Outcome == authv1.ErasureOutcome_ERASURE_OUTCOME_OK
	})
	waitFor(t, "forged events to be delivered", func() bool { return core.delivered.Load() >= 3 })
	time.Sleep(100 * time.Millisecond) // let the module consume them

	requireVictimDisposition(t, dbOwners(t, m), "sqlite")
	if f := readPolicy(t, m); !reflect.DeepEqual(f.AutoApproveUsers, []string{"admin-9", erasureBystand}) {
		t.Errorf("policy file = %v", f.AutoApproveUsers)
	}
	if erased, _ := m.store.UserErased(context.Background(), erasureBystand); erased {
		t.Fatal("a forged event erased the bystander")
	}
	if _, ok := provider.Latest("forged", erasureModID); ok {
		t.Error("a forged erasure id was acknowledged")
	}

	m.erasureMu.Lock()
	done := m.erasureDone
	m.erasureMu.Unlock()
	if done == nil {
		t.Fatal("reconciler was not started")
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	default:
		t.Fatal("Stop returned before the reconciler goroutine exited")
	}
	waitFor(t, "goroutines to return to the baseline", func() bool { return runtime.NumGoroutine() <= base })
	if err := m.Stop(context.Background()); err != nil { // idempotent
		t.Fatal(err)
	}
}

// TestStartFailsOnBadSweepInterval: a bad ERASURE_SWEEP_INTERVAL fails Start
// instead of leaving erasure silently off, in every profile.
func TestStartFailsOnBadSweepInterval(t *testing.T) {
	h := newLedgerHarness(t, erasureProvID)
	t.Setenv("MUXCORE_PROFILE", "household")
	t.Setenv(erasure.EnvSweepInterval, "never")
	m := h.module(t)
	if err := m.Start(context.Background()); err == nil || !strings.Contains(err.Error(), erasure.EnvSweepInterval) {
		t.Fatalf("Start = %v, want an %s error", err, erasure.EnvSweepInterval)
	}
}

func TestStartRunsReconcilerInHousehold(t *testing.T) {
	h := newLedgerHarness(t, erasureProvID)
	t.Setenv("MUXCORE_PROFILE", "household")
	m := h.module(t)
	id := h.provider.AddErasure(erasureVictim, "")
	seedUsers(t, m, erasureVictim, erasureBystand)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, "acknowledgement over mTLS", func() bool {
		a, ok := h.provider.Latest(id, erasureModID)
		return ok && a.Outcome == authv1.ErasureOutcome_ERASURE_OUTCOME_OK
	})
	requireVictimDisposition(t, dbOwners(t, m), "sqlite")
}
