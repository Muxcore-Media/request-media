package authn

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Muxcore-Media/request-media/internal/authz"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type countingResolver struct {
	n   int
	id  *Identity
	err error
}

func (c *countingResolver) ResolveToken(context.Context, string) (*Identity, error) {
	c.n++
	return c.id, c.err
}

func TestCachingTTL(t *testing.T) {
	inner := &countingResolver{id: &Identity{ID: "u1"}}
	c := NewCaching(inner)
	now := time.Unix(1000, 0)
	c.Now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if id, err := c.ResolveToken(context.Background(), "t"); err != nil || id.ID != "u1" {
			t.Fatalf("resolve: %v %v", id, err)
		}
	}
	if inner.n != 1 {
		t.Fatalf("calls within TTL = %d, want 1", inner.n)
	}
	now = now.Add(CacheTTL + time.Second)
	_, _ = c.ResolveToken(context.Background(), "t")
	if inner.n != 2 {
		t.Fatalf("calls after TTL = %d, want 2", inner.n)
	}
	// Unknown tokens and errors are not cached.
	inner.id = nil
	_, _ = c.ResolveToken(context.Background(), "other")
	_, _ = c.ResolveToken(context.Background(), "other")
	if inner.n != 4 {
		t.Fatalf("unknown token cached: calls = %d", inner.n)
	}
}

func run(t *testing.T, res Resolver, md metadata.MD) (string, error) {
	t.Helper()
	ctx := metadata.NewIncomingContext(context.Background(), md)
	var got string
	_, err := UnaryInterceptor(func() Resolver { return res }, nil)(ctx, nil, &grpc.UnaryServerInfo{},
		func(ctx context.Context, _ any) (any, error) {
			got = authz.CallerID(ctx)
			return nil, nil
		})
	return got, err
}

func TestUnaryInterceptor(t *testing.T) {
	ok := &countingResolver{id: &Identity{ID: "u1"}}
	if got, err := run(t, ok, metadata.Pairs("authorization", "Bearer abc")); err != nil || got != "u1" {
		t.Fatalf("valid: %q %v", got, err)
	}
	if got, err := run(t, ok, metadata.Pairs("x-caller-id", "admin")); err != nil || got != "" {
		t.Fatalf("no token must stay anonymous: %q %v", got, err)
	}
	if _, err := run(t, ok, metadata.Pairs("authorization", "Bearer abc", "x-caller-id", "admin")); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("mismatch: %v", err)
	}
	if _, err := run(t, &countingResolver{}, metadata.Pairs("authorization", "Bearer abc")); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unknown: %v", err)
	}
	if _, err := run(t, &countingResolver{err: errors.New("down")}, metadata.Pairs("authorization", "Bearer abc")); status.Code(err) != codes.Unavailable {
		t.Fatalf("down: %v", err)
	}
}
