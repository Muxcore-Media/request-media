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
