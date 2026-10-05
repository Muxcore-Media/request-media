package authz

import (
	"context"
	"fmt"
	"log/slog"

	authv1 "github.com/Muxcore-Media/core/proto/gen/muxcore/auth/v1"
	"github.com/Muxcore-Media/request-media/internal/grpctls"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	ResourceMediaRequest = "media.request"

	ActionCreate    = "create"
	ActionList      = "list"
	ActionApprove   = "approve"
	ActionDeny      = "deny"
	ActionWatchlist = "watchlist"
)

// CanFunc checks whether userID may perform action on resource.
type CanFunc func(ctx context.Context, userID, action, resource string) (bool, error)

// Checker performs permission checks via an injected function or mesh authorizer.
type Checker struct {
	can CanFunc
}

// Config configures permission checking.
type Config struct {
	// Can overrides mesh discovery when set (tests).
	Can CanFunc
	// FindAuthorizerAddr resolves the authorizer module gRPC address.
	FindAuthorizerAddr func(ctx context.Context) (string, error)
}

// New returns a Checker. When Can is nil and FindAuthorizerAddr is nil, checks allow all.
func New(cfg Config) *Checker {
	can := cfg.Can
	if can == nil && cfg.FindAuthorizerAddr != nil {
		can = meshCan(cfg.FindAuthorizerAddr)
	}
	if can == nil {
		can = func(context.Context, string, string, string) (bool, error) { return true, nil }
	}
	return &Checker{can: can}
}

func meshCan(findAddr func(ctx context.Context) (string, error)) CanFunc {
	return func(ctx context.Context, userID, action, resource string) (bool, error) {
		if userID == "" {
			return false, status.Error(codes.Unauthenticated, "authentication required")
		}
		addr, err := findAddr(ctx)
		if err != nil {
			slog.Warn("authorizer unavailable, denying action", "user_id", userID, "action", action, "resource", resource, "error", err)
			return false, nil
		}
		creds, err := grpctls.ClientCredentials()
		if err != nil {
			return false, fmt.Errorf("authorizer TLS: %w", err)
		}
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
		if err != nil {
			return false, fmt.Errorf("dial authorizer: %w", err)
		}
		defer conn.Close()
		resp, err := authv1.NewAuthServiceClient(conn).Can(ctx, &authv1.CanRequest{
			UserId:   userID,
			Action:   action,
			Resource: resource,
		})
		if err != nil {
			return false, fmt.Errorf("authorizer Can: %w", err)
		}
		return resp.GetAllowed(), nil
	}
}

type callerKey struct{}

// WithCallerID returns a context carrying the end-user caller ID.
func WithCallerID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, callerKey{}, id)
}

// CallerID returns the end-user caller ID set by WithCallerID, or "" when unset.
func CallerID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(callerKey{}).(string)
	return id
}

type modulePrincipalKey struct{}

// WithModulePrincipal records a verified mesh module identity (client cert CN).
func WithModulePrincipal(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, modulePrincipalKey{}, name)
}

// ModulePrincipal returns the verified module identity, or "".
func ModulePrincipal(ctx context.Context) string {
	s, _ := ctx.Value(modulePrincipalKey{}).(string)
	return s
}

// moduleActions are the only actions an allowlisted module principal may perform
// without a user token (media-library-maintainer: list + deny).
func moduleAllowed(ctx context.Context, userID, action string) bool {
	if userID != "" || ModulePrincipal(ctx) == "" {
		return false
	}
	return action == ActionList || action == ActionDeny
}

func (c *Checker) require(ctx context.Context, userID, action, resource string) error {
	if moduleAllowed(ctx, userID, action) {
		return nil
	}
	if userID == "" {
		return status.Error(codes.Unauthenticated, "authentication required")
	}
	allowed, err := c.can(ctx, userID, action, resource)
	if err != nil {
		return err
	}
	if !allowed {
		return status.Error(codes.PermissionDenied, "access denied")
	}
	return nil
}

func (c *Checker) RequireCreate(ctx context.Context, userID string) error {
	return c.require(ctx, userID, ActionCreate, ResourceMediaRequest)
}

func (c *Checker) RequireList(ctx context.Context, userID string) error {
	return c.require(ctx, userID, ActionList, ResourceMediaRequest)
}

// RequireViewStatus gates read access to a single request (uses list permission).
func (c *Checker) RequireViewStatus(ctx context.Context, userID string) error {
	return c.require(ctx, userID, ActionList, ResourceMediaRequest)
}

func (c *Checker) RequireApprove(ctx context.Context, userID string) error {
	return c.require(ctx, userID, ActionApprove, ResourceMediaRequest)
}

func (c *Checker) RequireDeny(ctx context.Context, userID string) error {
	return c.require(ctx, userID, ActionDeny, ResourceMediaRequest)
}

func (c *Checker) RequireWatchlist(ctx context.Context, userID string) error {
	return c.require(ctx, userID, ActionWatchlist, ResourceMediaRequest)
}

// CanApprove returns whether the caller may approve requests (auto-approve path).
func (c *Checker) CanApprove(ctx context.Context, userID string) bool {
	if userID == "" {
		return false
	}
	allowed, err := c.can(ctx, userID, ActionApprove, ResourceMediaRequest)
	return err == nil && allowed
}
