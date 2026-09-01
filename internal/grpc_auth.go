package internal

import (
	"context"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	mdRolesKey = "x-muxcore-roles"
	mdUserKey  = "x-muxcore-user"
)

type ctxRolesKey struct{}

func withRoles(ctx context.Context, roles []string) context.Context {
	if len(roles) == 0 {
		return ctx
	}
	return context.WithValue(ctx, ctxRolesKey{}, roles)
}

func rolesFromContext(ctx context.Context) []string {
	if v, ok := ctx.Value(ctxRolesKey{}).([]string); ok && len(v) > 0 {
		return v
	}
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil
	}
	vals := md.Get(mdRolesKey)
	if len(vals) == 0 {
		return nil
	}
	return splitRoles(vals[0])
}

func userFromContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get(mdUserKey)
	if len(vals) == 0 {
		return ""
	}
	return strings.TrimSpace(vals[0])
}

func incomingContextWithUser(ctx context.Context, requestedBy string) context.Context {
	if strings.TrimSpace(requestedBy) != "" {
		return ctx
	}
	if user := userFromContext(ctx); user != "" {
		return context.WithValue(ctx, ctxRequestedByKey{}, user)
	}
	return ctx
}

type ctxRequestedByKey struct{}

func requestedByFromContext(ctx context.Context, fallback string) string {
	if v, ok := ctx.Value(ctxRequestedByKey{}).(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return strings.TrimSpace(fallback)
}

func (m *Module) requireRequestRoles(ctx context.Context) error {
	roles := rolesFromContext(ctx)
	if !m.rolesCanRequest(roles) {
		return status.Error(codes.PermissionDenied, "forbidden: your role cannot request media")
	}
	return nil
}

func requireApproveRoles(ctx context.Context) error {
	roles := rolesFromContext(ctx)
	if !rolesCanApprove(roles) {
		return status.Error(codes.PermissionDenied, "forbidden: admin or manager required")
	}
	return nil
}

func canCancelRequest(roles []string, actor, requestedBy string) bool {
	if rolesCanApprove(roles) {
		return true
	}
	actor = strings.TrimSpace(actor)
	requestedBy = strings.TrimSpace(requestedBy)
	return actor != "" && requestedBy != "" && actor == requestedBy
}
