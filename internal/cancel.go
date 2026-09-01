package internal

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Muxcore-Media/core/pkg/tenant"
	requestmedia "github.com/Muxcore-Media/request-media/proto/requestmedia"
)

func (m *Module) CancelRequest(ctx context.Context, req *requestmedia.CancelRequestRequest) (*requestmedia.CancelRequestResponse, error) {
	id := strings.TrimSpace(req.GetRequestId())
	if id == "" {
		return &requestmedia.CancelRequestResponse{Error: "request_id is required"}, nil
	}
	m.mu.RLock()
	rec, ok := m.requests[id]
	m.mu.RUnlock()
	if !ok {
		return &requestmedia.CancelRequestResponse{RequestId: id, Error: "request not found"}, nil
	}
	if tenant.Enabled() {
		want := m.resolveTenant(ctx, nil)
		if rec.TenantID != "" && rec.TenantID != want {
			return &requestmedia.CancelRequestResponse{RequestId: id, Error: "request not found"}, nil
		}
	}
	cancelledBy := strings.TrimSpace(req.GetCancelledBy())
	if cancelledBy == "" {
		cancelledBy = userFromContext(ctx)
	}
	if !canCancelRequest(rolesFromContext(ctx), cancelledBy, rec.RequestedBy) {
		return nil, status.Error(codes.PermissionDenied, "forbidden")
	}
	switch rec.Status {
	case "available", "denied":
		return &requestmedia.CancelRequestResponse{
			RequestId: id, Status: rec.Status,
			Error: fmt.Sprintf("cannot cancel request in status %s", rec.Status),
		}, nil
	case "pending":
		// retract pending intake
	default:
		return &requestmedia.CancelRequestResponse{
			RequestId: id, Status: rec.Status,
			Error: fmt.Sprintf("cannot cancel request in status %s", rec.Status),
		}, nil
	}

	m.mu.Lock()
	delete(m.requests, id)
	m.mu.Unlock()
	if m.store != nil {
		if err := m.store.Delete(id, rec.TenantID); err != nil {
			return &requestmedia.CancelRequestResponse{RequestId: id, Error: err.Error()}, nil
		}
	}
	return &requestmedia.CancelRequestResponse{RequestId: id, Status: "cancelled"}, nil
}
