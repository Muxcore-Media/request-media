package reqquota

import (
	"testing"
	"time"
)

func TestParseUsers(t *testing.T) {
	t.Parallel()
	got := ParseUsers("Alice, bob; cara  cara")
	if len(got) != 3 || got[0] != "Alice" || got[1] != "bob" || got[2] != "cara" {
		t.Fatalf("%v", got)
	}
	if ParseUsers("  ") != nil {
		t.Fatal("empty")
	}
}

func TestDecideUnlimited(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	d := Decide(Policy{}, "alice", []Record{
		{RequestedBy: "alice", Status: "pending", CreatedAt: now},
	}, now)
	if !d.Allow || d.RemainingPending != -1 || d.RemainingWeek != -1 {
		t.Fatalf("%+v", d)
	}
}

func TestDecidePendingCap(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	recs := []Record{
		{RequestedBy: "alice", Status: "pending", CreatedAt: now.Add(-time.Hour)},
		{RequestedBy: "alice", Status: "pending", CreatedAt: now.Add(-2 * time.Hour)},
		{RequestedBy: "bob", Status: "pending", CreatedAt: now},
	}
	d := Decide(Policy{MaxPendingPerUser: 2}, "alice", recs, now)
	if d.Allow || d.Code != CodeQuotaPending || d.PendingUsed != 2 || d.RemainingPending != 0 {
		t.Fatalf("%+v", d)
	}
	ok := Decide(Policy{MaxPendingPerUser: 3}, "alice", recs, now)
	if !ok.Allow || ok.RemainingPending != 1 {
		t.Fatalf("%+v", ok)
	}
}

func TestDecideWeeklyCap(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	recs := []Record{
		{RequestedBy: "alice", Status: "requested", CreatedAt: now.Add(-24 * time.Hour)},
		{RequestedBy: "alice", Status: "pending", CreatedAt: now.Add(-48 * time.Hour)},
		{RequestedBy: "alice", Status: "denied", CreatedAt: now},
		{RequestedBy: "alice", Status: "requested", CreatedAt: now.Add(-10 * 24 * time.Hour)},
	}
	d := Decide(Policy{MaxPerWeek: 2}, "alice", recs, now)
	if d.Allow || d.Code != CodeQuotaWeek || d.WeekUsed != 2 {
		t.Fatalf("%+v", d)
	}
}

func TestDecideAutoApproveSkipsPendingCap(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	recs := []Record{
		{RequestedBy: "alice", Status: "pending", CreatedAt: now},
	}
	d := Decide(Policy{MaxPendingPerUser: 1, AutoApproveUsers: []string{"Alice"}}, "alice", recs, now)
	if !d.Allow || !d.AutoApprove {
		t.Fatalf("auto-approve should skip pending cap: %+v", d)
	}
	week := Decide(Policy{MaxPerWeek: 1, AutoApproveUsers: []string{"alice"}}, "alice", recs, now)
	if week.Allow || week.Code != CodeQuotaWeek {
		t.Fatalf("weekly cap still applies: %+v", week)
	}
}

func TestUserAutoApproved(t *testing.T) {
	t.Parallel()
	p := Policy{AutoApproveUsers: []string{"Sam", "lee"}}
	if !UserAutoApproved(p, "sam") || UserAutoApproved(p, "other") {
		t.Fatal("case-insensitive match")
	}
}
