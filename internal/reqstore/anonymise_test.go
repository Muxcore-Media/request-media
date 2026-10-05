package reqstore

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAnonymiseRequester(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "requests.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Now().UTC()
	for _, rec := range []*Record{
		{ID: "a", Title: "A", RequestedBy: "acct", Status: "pending", CreatedAt: now},
		{ID: "b", Title: "B", RequestedBy: "acct", Status: "added", CreatedAt: now},
		{ID: "c", Title: "C", RequestedBy: "other", Status: "pending", CreatedAt: now},
	} {
		if err := s.Put(rec); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.AnonymiseRequester("acct")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("updated %d, want 2", n)
	}
	for _, id := range []string{"a", "b"} {
		got, err := s.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if got.RequestedBy != DeletedUserID {
			t.Fatalf("%s requested_by %q", id, got.RequestedBy)
		}
	}
	other, err := s.Get("c")
	if err != nil {
		t.Fatal(err)
	}
	if other.RequestedBy != "other" {
		t.Fatalf("other %q", other.RequestedBy)
	}
	n, err = s.AnonymiseRequester("acct")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("second pass updated %d", n)
	}
	if _, err := s.AnonymiseRequester(" "); err == nil {
		t.Fatal("expected blank user id to fail")
	}
	n, err = s.AnonymiseRequester(DeletedUserID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("deleted-user pass updated %d", n)
	}
}
