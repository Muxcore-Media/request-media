package authz

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestMeshCan_FailClosedWhenAuthorizerUnavailable(t *testing.T) {
	checker := New(Config{
		FindAuthorizerAddr: func(context.Context) (string, error) {
			return "", errors.New("no authorizer module")
		},
	})
	ctx := context.Background()
	cases := []struct {
		name string
		fn   func(context.Context, string) error
	}{
		{"create", checker.RequireCreate},
		{"list", checker.RequireList},
		{"approve", checker.RequireApprove},
		{"deny", checker.RequireDeny},
		{"watchlist", checker.RequireWatchlist},
		{"view status", checker.RequireViewStatus},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.fn(ctx, "user-1")
			if err == nil {
				t.Fatal("expected permission denied when authorizer unavailable")
			}
			if status.Code(err) != codes.PermissionDenied {
				t.Fatalf("err = %v, want PermissionDenied", err)
			}
		})
	}
}

func TestMeshCan_RequiresCallerID(t *testing.T) {
	checker := New(Config{
		FindAuthorizerAddr: func(context.Context) (string, error) {
			return "127.0.0.1:1", nil
		},
	})
	err := checker.RequireCreate(context.Background(), "")
	if err == nil {
		t.Fatal("expected unauthenticated error for empty caller")
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("err = %v, want Unauthenticated", err)
	}
}

func TestRequireViewStatus_UnauthenticatedWithoutCaller(t *testing.T) {
	checker := New(Config{
		Can: func(context.Context, string, string, string) (bool, error) { return true, nil },
	})
	if err := checker.RequireViewStatus(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty caller")
	}
}
