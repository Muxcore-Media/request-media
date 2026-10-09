package reqstore

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Muxcore-Media/core/sdk/go/module/moduletest"
)

func TestUpgradeFromSnapshots(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	type want struct {
		status, requestedBy, denyReason string
		season, episode                 int32
		created                         time.Time
	}
	// Columns present at v0.2.7 are always seeded; later columns are checked
	// against the seeded value when the snapshot has it, else the default.
	wants := map[string]want{
		"req-1": {"available", "alice@example.com", "", 0, 0, base},
		"req-2": {"pending", "bob@example.com", "", 0, 0, base.Add(time.Hour)},
		"req-3": {"denied", "carol@example.com", "already owned elsewhere", 2, 5, base.Add(2 * time.Hour)},
	}
	snapshots := []struct {
		name         string
		hasRequester bool // requested_by .. genres_json seeded (v0.3.0+)
		hasQuality   bool // quality_profile_id seeded (v0.3.2+)
		hasReady     bool // request_ready_notified seeded (v0.3.2+)
	}{
		{"v0.2.7", false, false, false},
		{"v0.3.0", true, false, false},
		{"v0.3.2", true, true, true},
		// The current release (ADR-0035 slice E4 starts from it): no
		// erasure_applied table yet.
		{"v0.3.7", true, true, true},
	}

	freshStore, err := Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = freshStore.Close() })
	fresh := moduletest.Schema(t, freshStore.db)

	for _, snap := range snapshots {
		t.Run(snap.name, func(t *testing.T) {
			path := moduletest.CopyFixture(t, filepath.Join("testdata", "upgrade", snap.name+".db"))

			// Open twice: startup migration must be idempotent.
			first, err := Open(path)
			if err != nil {
				t.Fatalf("first open: %v", err)
			}
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			s, err := Open(path)
			if err != nil {
				t.Fatalf("second open: %v", err)
			}
			t.Cleanup(func() { _ = s.Close() })

			moduletest.RequireSchemaSuperset(t, moduletest.Schema(t, s.db), fresh)
			if _, ok := moduletest.Schema(t, s.db).Tables["erasure_applied"]; !ok {
				t.Error("upgrade did not create erasure_applied")
			}

			list, err := s.List()
			if err != nil {
				t.Fatal(err)
			}
			if len(list) != len(wants) {
				t.Fatalf("rows = %d, want %d", len(list), len(wants))
			}
			for id, w := range wants {
				r, err := s.Get(id)
				if err != nil {
					t.Fatalf("get %s: %v", id, err)
				}
				if r.Status != w.status || !r.CreatedAt.Equal(w.created) ||
					!r.UpdatedAt.Equal(w.created.Add(time.Minute)) {
					t.Errorf("%s: status/time = %q %v %v", id, r.Status, r.CreatedAt, r.UpdatedAt)
				}
				if r.ItemType == "" || r.TMDBID == 0 || r.Title == "" || r.Year == 0 {
					t.Errorf("%s: base columns lost: %+v", id, r)
				}
				requestedBy, denyReason, season, episode := "", "", int32(0), int32(0)
				if snap.hasRequester {
					requestedBy, denyReason, season, episode = w.requestedBy, w.denyReason, w.season, w.episode
				}
				if r.RequestedBy != requestedBy || r.DenyReason != denyReason ||
					r.SeasonNumber != season || r.EpisodeNumber != episode {
					t.Errorf("%s: requester/deny/season/episode = %q %q %d %d, want %q %q %d %d", id,
						r.RequestedBy, r.DenyReason, r.SeasonNumber, r.EpisodeNumber,
						requestedBy, denyReason, season, episode)
				}
				if snap.hasRequester != (r.Overview != "") || snap.hasRequester != (r.GenresJSON != "") {
					t.Errorf("%s: overview/genres = %q %q (seeded=%v)", id, r.Overview, r.GenresJSON, snap.hasRequester)
				}
				// New columns carry their defaults for pre-existing rows.
				quality := ""
				if snap.hasQuality && id == "req-1" {
					quality = "hd-1080p"
				}
				if r.QualityProfileID != quality {
					t.Errorf("%s: quality_profile_id = %q, want %q", id, r.QualityProfileID, quality)
				}
			}

			// request_ready_notified exists everywhere; seeded rows stay claimed.
			for id, claimed := range map[string]bool{"req-1": snap.hasReady, "req-2": false, "req-3": snap.hasReady} {
				first, err := s.ClaimReadyNotified(id)
				if err != nil {
					t.Fatal(err)
				}
				if first == claimed {
					t.Errorf("ClaimReadyNotified(%s) first=%v, want %v", id, first, !claimed)
				}
			}

			// Upgraded store stays writable with the current API.
			if err := s.Put(&Record{ID: "req-new", ItemType: "movie", Title: "New", QualityProfileID: "q"}); err != nil {
				t.Fatalf("put after upgrade: %v", err)
			}
			moduletest.RequireIntegrity(t, s.db)
		})
	}
}

// TestUpgradeThenErase applies an erasure to every upgraded snapshot: the
// forward-only migration must leave a store that erases correctly.
func TestUpgradeThenErase(t *testing.T) {
	for _, name := range []string{"v0.2.7", "v0.3.0", "v0.3.2", "v0.3.7"} {
		t.Run(name, func(t *testing.T) {
			s, err := Open(moduletest.CopyFixture(t, filepath.Join("testdata", "upgrade", name+".db")))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			ctx := context.Background()
			// v0.2.7 predates requested_by: every row is '' and nothing matches.
			seeded := name != "v0.2.7"

			for i, c := range []struct {
				who                string
				deleted, anonymise int64
			}{
				{"alice@example.com", 0, 1}, // available -> household record
				{"bob@example.com", 1, 0},   // pending   -> deleted
				{"carol@example.com", 1, 0}, // denied    -> deleted
			} {
				rec, err := s.EraseUser(ctx, fmt.Sprintf("er-%d", i), c.who, "")
				if err != nil {
					t.Fatalf("erase %s: %v", c.who, err)
				}
				wantDeleted, wantAnon := c.deleted, c.anonymise
				if !seeded {
					wantDeleted, wantAnon = 0, 0
				}
				if rec.Counts[CountRequestsDeleted] != wantDeleted || rec.Counts[CountRequestsAnonymised] != wantAnon {
					t.Errorf("%s counts = %v, want deleted=%d anonymised=%d", c.who, rec.Counts, wantDeleted, wantAnon)
				}
				if n, err := s.CountUserRows(ctx, c.who); err != nil || n != 0 {
					t.Errorf("%s remaining = %d, %v", c.who, n, err)
				}
			}
			if !seeded {
				return
			}
			// alice's 'available' row is the household record: it stays,
			// anonymised; bob's pending and carol's denied rows are gone.
			r, err := s.Get("req-1")
			if err != nil || r.RequestedBy != AnonymisedUser || r.Title == "" {
				t.Errorf("req-1 = %+v, %v", r, err)
			}
			for _, id := range []string{"req-2", "req-3"} {
				if _, err := s.Get(id); err == nil {
					t.Errorf("%s survived erasure", id)
				}
			}
			moduletest.RequireIntegrity(t, s.db)
		})
	}
}
