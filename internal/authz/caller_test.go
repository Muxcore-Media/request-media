package authz

import (
	"context"
	"testing"
)

func TestCallerIDRoundTrip(t *testing.T) {
	if got := CallerID(context.Background()); got != "" {
		t.Fatalf("unset caller = %q, want empty", got)
	}
	//nolint:staticcheck // deliberately exercising nil-safety
	if got := CallerID(nil); got != "" {
		t.Fatalf("nil ctx caller = %q, want empty", got)
	}
	if got := CallerID(WithCallerID(context.Background(), "u1")); got != "u1" {
		t.Fatalf("caller = %q, want u1", got)
	}
	//nolint:staticcheck // deliberately exercising nil-safety
	if got := CallerID(WithCallerID(nil, "u2")); got != "u2" {
		t.Fatalf("caller = %q, want u2", got)
	}
}

func TestModulePrincipalOnlyListAndDeny(t *testing.T) {
	c := New(Config{Can: func(context.Context, string, string, string) (bool, error) { return false, nil }})
	ctx := WithModulePrincipal(context.Background(), "media-library-maintainer")
	if err := c.RequireList(ctx, ""); err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := c.RequireDeny(ctx, ""); err != nil {
		t.Fatalf("deny: %v", err)
	}
	if err := c.RequireApprove(ctx, ""); err == nil {
		t.Fatal("approve must stay denied for module principals")
	}
	if err := c.RequireCreate(ctx, ""); err == nil {
		t.Fatal("create must stay denied for module principals")
	}
	if err := c.RequireList(context.Background(), ""); err == nil {
		t.Fatal("anonymous list must fail")
	}
}
