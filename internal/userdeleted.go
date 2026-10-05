package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Muxcore-Media/contracts-media/events"
	"github.com/Muxcore-Media/request-media/internal/reqstore"
)

// ApplyUserDeleted rewrites requested_by to deleted-user for the account in
// an identity.user.deleted payload.
func ApplyUserDeleted(st *reqstore.Store, payload []byte) error {
	if st == nil {
		return fmt.Errorf("store is nil")
	}
	var body events.UserDeletedPayload
	if err := json.Unmarshal(payload, &body); err != nil {
		return fmt.Errorf("decode identity.user.deleted: %w", err)
	}
	if strings.TrimSpace(body.UserID) == "" {
		return fmt.Errorf("identity.user.deleted missing user_id")
	}
	_, err := st.AnonymiseRequester(body.UserID)
	return err
}

func (m *Module) consumeUserDeleted(ctx context.Context) {
	if m.mc == nil || m.store == nil {
		return
	}
	ch, cancel, err := m.mc.Events.Subscribe(ctx, events.EventIdentityUserDeleted)
	if err != nil {
		slog.Warn("request-media: subscribe identity.user.deleted", "error", err)
		return
	}
	defer cancel()
	slog.Info("request-media subscribed to identity.user.deleted")
	for evt := range ch {
		if evt == nil {
			continue
		}
		if err := ApplyUserDeleted(m.store, evt.Payload); err != nil {
			slog.Warn("request-media: apply identity.user.deleted", "error", err)
		}
	}
}
