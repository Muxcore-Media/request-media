package reqstore

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPartitions_TenantDBPathsAndCrossGet(t *testing.T) {
	t.Setenv("TENANT_MODE", "1")
	root := t.TempDir()
	p, err := OpenPartitions(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })

	pathA := p.PathFor("tenant-a")
	pathB := p.PathFor("tenant-b")
	if pathA == pathB {
		t.Fatalf("expected distinct DB paths, got %q", pathA)
	}
	wantA := filepath.Join(root, "tenants", "tenant-a", "requests.db")
	wantB := filepath.Join(root, "tenants", "tenant-b", "requests.db")
	if pathA != wantA || pathB != wantB {
		t.Fatalf("pathA=%q pathB=%q", pathA, pathB)
	}

	rec := &Record{
		ID: "req-1", ItemType: "movie", Title: "X", Status: "requested",
		TenantID: "tenant-a", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := p.Put(rec); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pathA); err != nil {
		t.Fatalf("tenant-a db missing: %v", err)
	}

	got, err := p.Get("req-1", "tenant-a")
	if err != nil || got == nil || got.Title != "X" {
		t.Fatalf("Get same tenant: got=%v err=%v", got, err)
	}
	_, err = p.Get("req-1", "tenant-b")
	if err == nil {
		t.Fatal("cross-tenant Get must fail")
	}

	stB, err := p.ForTenant("tenant-b")
	if err != nil {
		t.Fatal(err)
	}
	_, err = stB.Get("req-1")
	if err == nil {
		t.Fatal("tenant-b store must not contain tenant-a row")
	}
}

func TestPartitions_SingleTenantSharedDB(t *testing.T) {
	t.Setenv("TENANT_MODE", "")
	root := t.TempDir()
	p, err := OpenPartitions(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	path := p.PathFor("ignored")
	if path != filepath.Join(root, "requests.db") {
		t.Fatalf("got %q", path)
	}
	if err := p.Put(&Record{ID: "1", Title: "a", Status: "x", CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	got, err := p.Get("1", "")
	if err != nil || got.Title != "a" {
		t.Fatalf("%v %v", got, err)
	}
}
