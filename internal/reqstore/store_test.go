package reqstore

import (
	"path/filepath"
	"testing"
	"time"
)

func TestPutGetList(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "requests.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	rec := &Record{
		ID: "req1", ItemType: "movie", ItemID: "mv1", TMDBID: 42,
		Title: "Test", Year: 2020, Poster: "/p.jpg", Status: "added",
		CreatedAt: time.Now().UTC().Add(-time.Minute),
	}
	if err := s.Put(rec); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := s.Get("req1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != "Test" || got.Status != "added" || got.TMDBID != 42 {
		t.Fatalf("unexpected record: %+v", got)
	}

	rec.Status = "workflow"
	if err := s.Put(rec); err != nil {
		t.Fatalf("Put update: %v", err)
	}
	got, err = s.Get("req1")
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got.Status != "workflow" {
		t.Fatalf("status = %q, want workflow", got.Status)
	}

	if err := s.Put(&Record{
		ID: "req2", ItemType: "tv", Title: "Show", Status: "requested",
	}); err != nil {
		t.Fatalf("Put 2: %v", err)
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List len = %d, want 2", len(list))
	}
}

func TestLoadAllPersistsAcrossOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "requests.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Put(&Record{ID: "a", ItemType: "movie", Title: "A", Status: "requested"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	s.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	all, err := s2.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if all["a"] == nil || all["a"].Title != "A" {
		t.Fatalf("LoadAll missing: %+v", all)
	}
}

func TestPruneDuplicateTMDBRows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "requests.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	old := time.Now().UTC().Add(-time.Hour)
	newer := time.Now().UTC()
	if err := s.Put(&Record{ID: "star-a", ItemType: "tv", TMDBID: 253, Title: "Star Trek", Status: "requested", CreatedAt: old}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(&Record{ID: "star-b", ItemType: "tv", TMDBID: 253, Title: "Star Trek", ItemID: "series-1", Status: "added", CreatedAt: newer}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(&Record{ID: "star-movie", ItemType: "movie", TMDBID: 253, Title: "Star Trek", Status: "requested", CreatedAt: old}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	all, err := s2.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("after prune len=%d want 2 (tv+movie), got %+v", len(all), all)
	}
	if all["star-b"] == nil {
		t.Fatalf("should keep tv row with item_id, got %+v", all)
	}
	if all["star-movie"] == nil {
		t.Fatalf("movie row must remain, got %+v", all)
	}
}
