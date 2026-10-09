package reqstore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "requests.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func mustPut(t *testing.T, s *Store, id, status, by string) {
	t.Helper()
	if err := s.Put(&Record{ID: id, ItemType: "movie", Title: "T-" + id, Status: status, RequestedBy: by}); err != nil {
		t.Fatalf("put %s: %v", id, err)
	}
}

// seedVictim stores one request per state for victim and bystander, and
// claims the ready notification for every approved-side row.
func seedVictim(t *testing.T, s *Store) {
	t.Helper()
	for _, u := range []string{"victim", "bystander"} {
		for _, st := range []string{"watchlisted", "pending", "denied", "requested", "added", "workflow", "available"} {
			id := u + "-" + st
			mustPut(t, s, id, st, u)
			if _, err := s.ClaimReadyNotified(id); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func statusByID(t *testing.T, s *Store) map[string]string {
	t.Helper()
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, r := range list {
		out[r.ID] = r.RequestedBy
	}
	return out
}

func TestEraseUserDisposition(t *testing.T) {
	s, _ := openTemp(t)
	seedVictim(t, s)
	ctx := context.Background()

	rec, err := s.EraseUser(ctx, "er-1", "victim", "")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{CountReadyDeleted: 3, CountRequestsDeleted: 3, CountRequestsAnonymised: 4}
	if !reflect.DeepEqual(rec.Counts, want) {
		t.Fatalf("counts = %v, want %v", rec.Counts, want)
	}
	rows := statusByID(t, s)
	for _, st := range DeletedOnErasure {
		if _, ok := rows["victim-"+st]; ok {
			t.Errorf("victim %s row survived", st)
		}
	}
	for _, st := range []string{"requested", "added", "workflow", "available"} {
		if got := rows["victim-"+st]; got != AnonymisedUser {
			t.Errorf("victim %s requested_by = %q, want %q", st, got, AnonymisedUser)
		}
	}
	for _, st := range append([]string{"requested", "added", "workflow", "available"}, DeletedOnErasure...) {
		if got := rows["bystander-"+st]; got != "bystander" {
			t.Errorf("bystander %s requested_by = %q", st, got)
		}
	}
	// request_ready_notified: the victim's deleted rows are gone, anonymised
	// rows stay claimed so media.request.ready is not published again, and
	// the bystander is untouched.
	for id, wantClaimedAlready := range map[string]bool{
		"victim-pending": false, "victim-denied": false, "victim-watchlisted": false,
		"victim-added": true, "victim-requested": true, "bystander-pending": true,
	} {
		first, err := s.ClaimReadyNotified(id)
		if err != nil {
			t.Fatal(err)
		}
		if first == wantClaimedAlready {
			t.Errorf("ClaimReadyNotified(%s) first=%v, want %v", id, first, !wantClaimedAlready)
		}
	}
	if n, err := s.CountUserRows(ctx, "victim"); err != nil || n != 0 {
		t.Fatalf("remaining = %d, %v", n, err)
	}
	if done, err := s.ErasureComplete(ctx, "er-1"); err != nil || done {
		t.Fatalf("complete before phase 2 = %v, %v", done, err)
	}
	if erased, err := s.UserErased(ctx, "victim"); err != nil || !erased {
		t.Fatalf("UserErased(victim) = %v, %v", erased, err)
	}
	if erased, err := s.UserErased(ctx, "bystander"); err != nil || erased {
		t.Fatalf("UserErased(bystander) = %v, %v", erased, err)
	}
}

func TestEraseUserIsIdempotent(t *testing.T) {
	s, _ := openTemp(t)
	seedVictim(t, s)
	ctx := context.Background()
	first, err := s.EraseUser(ctx, "er-1", "victim", "")
	if err != nil {
		t.Fatal(err)
	}
	before := statusByID(t, s)
	again, err := s.EraseUser(ctx, "er-1", "victim", "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again.Counts, first.Counts) || again.PolicyDone {
		t.Fatalf("second call = %+v, want stored record %+v", again, first)
	}
	if !reflect.DeepEqual(statusByID(t, s), before) {
		t.Fatal("second EraseUser changed rows")
	}
	if err := s.MarkPolicyDone(ctx, "er-1", map[string]int64{"x": 1}); err != nil {
		t.Fatal(err)
	}
	if done, err := s.ErasureComplete(ctx, "er-1"); err != nil || !done {
		t.Fatalf("complete after phase 2 = %v, %v", done, err)
	}
	third, err := s.EraseUser(ctx, "er-1", "victim", "")
	if err != nil || !third.PolicyDone || third.Counts["x"] != 1 {
		t.Fatalf("third call = %+v, %v", third, err)
	}
	if err := s.MarkPolicyDone(ctx, "unknown", nil); err == nil {
		t.Fatal("MarkPolicyDone for an unknown erasure succeeded")
	}
}

func TestEraseUserRejectsUnusableTargets(t *testing.T) {
	s, _ := openTemp(t)
	mustPut(t, s, "a", "added", AnonymisedUser)
	ctx := context.Background()
	for _, c := range []struct{ erasure, user string }{{"", "u"}, {"e", ""}, {"e", "  "}, {"e", AnonymisedUser}} {
		if _, err := s.EraseUser(ctx, c.erasure, c.user, ""); !errors.Is(err, ErrInvalidErasure) {
			t.Errorf("EraseUser(%q,%q) = %v, want ErrInvalidErasure", c.erasure, c.user, err)
		}
	}
	if got := statusByID(t, s)["a"]; got != AnonymisedUser {
		t.Fatalf("row changed: %q", got)
	}
}

// rawDB opens a second connection to the same file, used to inject faults
// the production code has no seam for.
func rawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestEraseUserMidTransactionFailureRollsBack makes the final INSERT of the
// transaction fail, after the DELETEs and the UPDATE already ran: nothing may
// remain changed, and a retry completes.
func TestEraseUserMidTransactionFailureRollsBack(t *testing.T) {
	s, path := openTemp(t)
	seedVictim(t, s)
	ctx := context.Background()
	before := statusByID(t, s)

	db := rawDB(t, path)
	if _, err := db.Exec(`CREATE TRIGGER fail_erasure BEFORE INSERT ON erasure_applied
		BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EraseUser(ctx, "er-1", "victim", ""); err == nil {
		t.Fatal("EraseUser succeeded with an injected failure")
	}
	if !reflect.DeepEqual(statusByID(t, s), before) {
		t.Fatal("failed EraseUser left changes behind")
	}
	for _, id := range []string{"victim-pending", "victim-added"} {
		if first, err := s.ClaimReadyNotified(id); err != nil || first {
			t.Fatalf("ready row of %s lost on rollback: first=%v err=%v", id, first, err)
		}
	}
	if erased, _ := s.UserErased(ctx, "victim"); erased {
		t.Fatal("rolled-back erasure is recorded")
	}
	if _, err := db.Exec(`DROP TRIGGER fail_erasure`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EraseUser(ctx, "er-1", "victim", ""); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if n, _ := s.CountUserRows(ctx, "victim"); n != 0 {
		t.Fatalf("remaining after retry = %d", n)
	}
}

func TestPutRefusedForErasedUser(t *testing.T) {
	s, path := openTemp(t)
	mustPut(t, s, "keep", "added", "victim")
	if _, err := s.EraseUser(context.Background(), "er-1", "victim", ""); err != nil {
		t.Fatal(err)
	}
	err := s.Put(&Record{ID: "late", ItemType: "movie", Status: "pending", RequestedBy: "victim"})
	if !errors.Is(err, ErrUserErased) {
		t.Fatalf("late Put = %v, want ErrUserErased", err)
	}
	// An update of an existing row cannot hand the id back either.
	err = s.Put(&Record{ID: "keep", ItemType: "movie", Status: "added", RequestedBy: "victim"})
	if !errors.Is(err, ErrUserErased) {
		t.Fatalf("Put over an anonymised row = %v, want ErrUserErased", err)
	}
	if _, err := s.Get("late"); err == nil {
		t.Fatal("refused row was stored")
	}
	if got := statusByID(t, s)["keep"]; got != AnonymisedUser {
		t.Fatalf("anonymised row = %q", got)
	}
	mustPut(t, s, "other", "pending", "bystander") // others are unaffected

	// The refusal is read from the table, so it survives a restart.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	if err := s2.Put(&Record{ID: "late2", ItemType: "movie", Status: "pending", RequestedBy: "victim"}); !errors.Is(err, ErrUserErased) {
		t.Fatalf("Put after restart = %v, want ErrUserErased", err)
	}
	if erased, err := s2.UserErased(context.Background(), "victim"); err != nil || !erased {
		t.Fatalf("UserErased after restart = %v, %v", erased, err)
	}
}
