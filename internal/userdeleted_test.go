package internal

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Muxcore-Media/contracts-media/events"
	"github.com/Muxcore-Media/request-media/internal/reqstore"
)

func TestApplyUserDeleted(t *testing.T) {
	st, err := reqstore.Open(filepath.Join(t.TempDir(), "requests.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Now().UTC()
	if err := st.Put(&reqstore.Record{
		ID: "a", Title: "A", RequestedBy: "acct", Status: "pending", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(events.UserDeletedPayload{UserID: "acct"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyUserDeleted(st, raw); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get("a")
	if err != nil {
		t.Fatal(err)
	}
	if got.RequestedBy != reqstore.DeletedUserID {
		t.Fatalf("requested_by %q", got.RequestedBy)
	}
	if err := ApplyUserDeleted(st, []byte(`{}`)); err == nil {
		t.Fatal("expected missing user_id to fail")
	}
	if err := ApplyUserDeleted(st, []byte(`not-json`)); err == nil {
		t.Fatal("expected bad JSON to fail")
	}
	if err := ApplyUserDeleted(nil, raw); err == nil {
		t.Fatal("expected nil store to fail")
	}
}
