package reqstore

import (
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
