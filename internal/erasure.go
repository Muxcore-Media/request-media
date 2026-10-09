package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Muxcore-Media/core/sdk/go/module/erasure"
	"github.com/Muxcore-Media/request-media/internal/authn"
	"github.com/Muxcore-Media/request-media/internal/reqstore"
)

// ADR-0035 user erasure. The identity provider's ledger is the ONLY thing
// that erases data here: no event, header or HTTP request reaches this code.
// erasure.Reconciler (sdk/go/module/erasure) reads the ledger from the
// provider of the "identity" capability, verified by certificate CN, and
// calls erasureOwner.
//
// Disposition (ADR-0035 §3, reqstore.DeletedOnErasure):
//
//	requests  watchlisted | pending | denied  -> deleted, with their
//	                                             request_ready_notified rows
//	requests  every other state               -> requested_by = "deleted-user"
//	request-policy.json autoApproveUsers      -> the id is removed
//
// Two-phase application. The policy file lives outside SQLite, so one
// transaction cannot cover both:
//
//  1. ONE SQLite transaction deletes/anonymises the rows AND inserts the
//     erasure_applied row (policy_done = 0). From this commit on the user id
//     is refused for create/approve, and the data is gone.
//  2. After the commit the id is removed from the in-memory list and from
//     request-policy.json (atomic temp-file rename, idempotent), then
//     policy_done is set to 1.
//
// Applied reports true only when policy_done = 1. A crash (or file error)
// between the phases leaves policy_done = 0: the next sweep calls Apply
// again, phase 1 returns the stored record without touching anything, and
// phase 2 runs. A tombstone is therefore never acknowledged OK while the id
// is still in the policy file.

// CodeUserErased is the stable error code returned (HTTP 403 JSON "code",
// gRPC PermissionDenied message prefix) when an erased user id tries to
// create or approve requests.
const CodeUserErased = "request.user_erased"

// errUserErased is the refusal returned to callers.
func errUserErased() error {
	return status.Error(codes.PermissionDenied, CodeUserErased+": this account has been erased")
}

// Detail codes (erasure.WithDetail) specific to this module.
const (
	detailPolicyFile  = "policy_file_failed"
	detailInvalidUser = "invalid_user_id"
	detailNoStore     = "store_unavailable"
)

// policyTempPrefix names the temp files of atomic policy-file writes.
const policyTempPrefix = ".request-policy.json.tmp-"

// refuseErased fails closed when the authenticated caller has an erasure
// record. The table is read per call, so it survives restarts.
func (m *Module) refuseErased(ctx context.Context, caller string) error {
	if caller == "" || m.store == nil {
		return nil
	}
	erased, err := m.store.UserErased(ctx, caller)
	if err != nil {
		slog.Warn("request-media: cannot read erasure state; refusing", "error", err)
		return status.Error(codes.Unavailable, "cannot verify account state")
	}
	if erased {
		return errUserErased()
	}
	return nil
}

// erasedGuard wraps the identity resolver so a token that still resolves for
// an erased user (provider revocation lags, or the 30 s authn cache) is not
// honoured: the caller is treated as unauthenticated.
type erasedGuard struct {
	next authn.Resolver
	m    *Module
}

func (g erasedGuard) ResolveToken(ctx context.Context, token string) (*authn.Identity, error) {
	id, err := g.next.ResolveToken(ctx, token)
	if err != nil || id == nil {
		return id, err
	}
	if g.m.store == nil {
		return id, nil
	}
	erased, err := g.m.store.UserErased(ctx, id.ID)
	if err != nil {
		return nil, fmt.Errorf("erasure state: %w", err)
	}
	if erased {
		return nil, nil
	}
	return id, nil
}

// erasureOwner implements erasure.Owner and erasure.Verifier for the module.
type erasureOwner struct{ m *Module }

var (
	_ erasure.Owner    = erasureOwner{}
	_ erasure.Verifier = erasureOwner{}
)

func (o erasureOwner) ModuleID() string { return o.m.id }

// Applied is true only when BOTH phases are done (see the package comment).
func (o erasureOwner) Applied(ctx context.Context, erasureID string) (bool, error) {
	if o.m.store == nil {
		return false, errors.New("request store is not open")
	}
	return o.m.store.ErasureComplete(ctx, erasureID)
}

func (o erasureOwner) Apply(ctx context.Context, t erasure.Tombstone) (erasure.Counts, error) {
	m := o.m
	if m.store == nil {
		return nil, erasure.WithDetail(detailNoStore, errors.New("request store is not open"))
	}
	rec, err := m.store.EraseUser(ctx, t.ErasureID, t.UserID, t.TenantID)
	if errors.Is(err, reqstore.ErrInvalidErasure) {
		return nil, erasure.WithDetail(detailInvalidUser, err)
	}
	if err != nil {
		return nil, err // rolled back: nothing changed
	}
	// Phase 2. Memory first, so no request is served from the stale cache
	// while the file is edited.
	m.forgetUser(t.UserID)
	if rec.PolicyDone {
		return erasure.Counts(rec.Counts), nil
	}
	removed, err := m.removeAutoApprove(t.UserID)
	if err != nil {
		return nil, erasure.WithDetail(detailPolicyFile, err)
	}
	rec.Counts[reqstore.CountAutoApprove] = int64(removed)
	if err := m.store.MarkPolicyDone(ctx, t.ErasureID, rec.Counts); err != nil {
		return nil, err
	}
	return erasure.Counts(rec.Counts), nil
}

// Verify counts what the disposition should have removed: requests still
// naming the user, plus the user's presence in the in-memory and persisted
// auto-approve list. It must be 0 after Apply.
func (o erasureOwner) Verify(ctx context.Context, t erasure.Tombstone) (int, error) {
	m := o.m
	if m.store == nil {
		return 0, errors.New("request store is not open")
	}
	n, err := m.store.CountUserRows(ctx, t.UserID)
	if err != nil {
		return 0, err
	}
	m.cfgMu.RLock()
	for _, u := range m.autoApproveUsers {
		if strings.TrimSpace(u) == t.UserID {
			n++
		}
	}
	m.cfgMu.RUnlock()
	f, ok, err := m.readPolicyFile()
	if err != nil {
		return 0, err
	}
	if ok {
		for _, u := range f.AutoApproveUsers {
			if strings.TrimSpace(u) == t.UserID {
				n++
			}
		}
	}
	return n, nil
}

// forgetUser applies the disposition to the in-memory request cache. Entries
// are replaced by copies, never mutated: a concurrent saveRequest may still
// hold the pointer, and its write is refused by the store.
func (m *Module) forgetUser(userID string) {
	deleted := map[string]struct{}{}
	for _, st := range reqstore.DeletedOnErasure {
		deleted[st] = struct{}{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, rec := range m.requests {
		if rec == nil || rec.RequestedBy != userID {
			continue
		}
		if _, ok := deleted[rec.Status]; ok {
			delete(m.requests, id)
			continue
		}
		cp := *rec
		cp.RequestedBy = reqstore.AnonymisedUser
		m.requests[id] = &cp
	}
}

// removeAutoApprove drops userID from the in-memory auto-approve list and
// from request-policy.json. It is idempotent and returns how many list entries
// naming the user it removed (the in-memory list mirrors the file, so the
// larger of the two, not their sum). A missing file is fine (the list may come from the environment
// only); an unreadable or unparsable file is an error so the erasure is
// retried instead of acknowledged.
func (m *Module) removeAutoApprove(userID string) (int, error) {
	m.cfgMu.Lock()
	kept, removed := withoutUser(m.autoApproveUsers, userID)
	m.autoApproveUsers = kept
	m.cfgMu.Unlock()

	path := m.policyPath()
	if path == "" {
		return removed, nil
	}
	m.policyFileMu.Lock()
	defer m.policyFileMu.Unlock()
	removeStalePolicyTemps(path)
	f, ok, err := m.readPolicyFileLocked()
	if err != nil {
		return removed, err
	}
	if !ok {
		return removed, nil
	}
	kept, n := withoutUser(f.AutoApproveUsers, userID)
	if n == 0 {
		return removed, nil
	}
	f.AutoApproveUsers = kept
	if err := writePolicyFileAtomic(path, f); err != nil {
		return removed, err
	}
	return max(removed, n), nil
}

func withoutUser(list []string, userID string) (kept []string, removed int) {
	for _, u := range list {
		if strings.TrimSpace(u) == userID {
			removed++
			continue
		}
		kept = append(kept, u)
	}
	return kept, removed
}

// readPolicyFile returns the persisted policy. ok is false when there is no
// file. A file that exists but cannot be read or parsed is an error.
func (m *Module) readPolicyFile() (requestPolicyFile, bool, error) {
	m.policyFileMu.Lock()
	defer m.policyFileMu.Unlock()
	return m.readPolicyFileLocked()
}

func (m *Module) readPolicyFileLocked() (requestPolicyFile, bool, error) {
	var f requestPolicyFile
	path := m.policyPath()
	if path == "" {
		return f, false, nil
	}
	b, err := os.ReadFile(path) //nolint:gosec // module data dir
	if errors.Is(err, fs.ErrNotExist) {
		return f, false, nil
	}
	if err != nil {
		return f, false, fmt.Errorf("read policy file: %w", err)
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return requestPolicyFile{}, false, fmt.Errorf("policy file is not valid JSON: %w", err)
	}
	return f, true, nil
}

// writePolicyFileAtomic writes the policy through a temp file in the same
// directory and renames it over the target, keeping the existing file mode
// (0600 for a new file, as before). A crash leaves either the old or the new
// file, never a torn one.
func writePolicyFileAtomic(path string, f requestPolicyFile) error {
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	mode := fs.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, policyTempPrefix+"*")
	if err != nil {
		return fmt.Errorf("write policy file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write policy file: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write policy file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write policy file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("write policy file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("write policy file: %w", err)
	}
	if d, err := os.Open(dir); err == nil { //nolint:gosec // module data dir
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// removeStalePolicyTemps deletes temp files an interrupted write left behind:
// they could hold an older auto-approve list that still names the user.
// Callers hold policyFileMu, so no live write owns one.
func removeStalePolicyTemps(path string) {
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), policyTempPrefix+"*"))
	if err != nil {
		return
	}
	for _, p := range matches {
		_ = os.Remove(p)
	}
}

// dropErasedAutoApprove removes erased ids from the in-memory auto-approve
// list. The list is seeded from REQUEST_AUTO_APPROVE_USERS, which the module
// cannot rewrite; this keeps an erased id out of it after every restart.
func (m *Module) dropErasedAutoApprove(ctx context.Context) {
	if m.store == nil {
		return
	}
	m.cfgMu.Lock()
	defer m.cfgMu.Unlock()
	var kept []string
	for _, u := range m.autoApproveUsers {
		erased, err := m.store.UserErased(ctx, strings.TrimSpace(u))
		if err != nil || erased {
			if err != nil {
				slog.Warn("request-media: cannot read erasure state for auto-approve list", "error", err)
			}
			continue
		}
		kept = append(kept, u)
	}
	m.autoApproveUsers = kept
}

// householdProfile reports whether MUXCORE_PROFILE names the household
// profile or its alias staging, the same test meshtls applies.
func householdProfile() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MUXCORE_PROFILE"))) {
	case "household", "staging":
		return true
	}
	return false
}

// startErasure starts the reconciler. dialer reaches the identity provider
// through core's DiscoveryService. It runs until Stop. The household profile
// requires it: a configuration error or a missing core connection there makes
// Start fail instead of leaving erasure silently off.
func (m *Module) startErasure(dialer *erasure.ProviderDialer) error {
	if m.store == nil {
		return errors.New("erasure: request store is not open")
	}
	rec, err := erasure.New(erasure.Config{
		Owner:  erasureOwner{m: m},
		Dialer: dialer,
		Logger: slog.Default(),
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	m.erasureMu.Lock()
	m.erasure, m.erasureCancel, m.erasureDone = rec, cancel, done
	m.erasureMu.Unlock()
	go func() {
		defer close(done)
		if err := rec.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("request-media: erasure reconciler stopped", "error", err)
		}
	}()
	slog.Info("request-media: erasure reconciler started")
	return nil
}

// stopErasure cancels the reconciler and waits for it, bounded by ctx. It
// must run before the store closes: Apply uses the store.
func (m *Module) stopErasure(ctx context.Context) {
	m.erasureMu.Lock()
	cancel, done := m.erasureCancel, m.erasureDone
	m.erasureCancel, m.erasureDone = nil, nil
	m.erasureMu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	t := time.NewTimer(30 * time.Second)
	defer t.Stop()
	select {
	case <-done:
	case <-ctx.Done():
	case <-t.C:
		slog.Warn("request-media: erasure reconciler did not stop in time")
	}
}
