package internal

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	automationv1 "github.com/Muxcore-Media/contracts-automation/muxcore/automation/v1"
)

func statusRank(s string) int {
	switch s {
	case "available":
		return 6
	case "import_failed", "failed":
		return 5
	case "stalled":
		return 4
	case "downloading":
		return 3
	case "searching", "queued":
		return 2
	case "added":
		return 1
	case "requested", "workflow":
		return 0
	default:
		return 0
	}
}

func historyStatusPriority(histStatus string) int {
	switch histStatus {
	case "import_failed", "failed":
		return 3
	case "stalled":
		return 2
	case "sent":
		return 1
	default:
		return 0
	}
}

func historyToRequestStatus(histStatus string) string {
	switch histStatus {
	case "sent":
		return "downloading"
	case "stalled", "import_failed", "failed":
		return histStatus
	default:
		return ""
	}
}

func isInProgressRequestStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "available", "denied", "":
		return false
	default:
		return true
	}
}

func queueItemIDs(itemType string, tmdb int32, title string, queue []*automationv1.QueueItem) map[string]struct{} {
	title = strings.TrimSpace(title)
	idSet := map[string]struct{}{}
	for _, q := range queue {
		if q == nil || q.GetItemType() != itemType {
			continue
		}
		match := tmdb > 0 && q.GetTmdbId() == tmdb
		if !match && tmdb == 0 && title != "" && strings.EqualFold(strings.TrimSpace(q.GetTitle()), title) {
			match = true
		}
		if match {
			idSet[q.GetItemId()] = struct{}{}
		}
	}
	return idSet
}

func bestHistoryRecord(idSet map[string]struct{}, hist []*automationv1.DownloadRecord) *automationv1.DownloadRecord {
	if len(idSet) == 0 {
		return nil
	}
	var best *automationv1.DownloadRecord
	bestPri := 0
	for _, h := range hist {
		if h == nil {
			continue
		}
		if _, ok := idSet[h.GetWantedItemId()]; !ok {
			continue
		}
		switch h.GetStatus() {
		case "sent", "stalled", "import_failed", "failed":
			if p := historyStatusPriority(h.GetStatus()); p > bestPri {
				bestPri = p
				best = h
			}
		}
	}
	return best
}

func deriveHistoryStatusFields(itemType string, tmdb int32, title string, queue []*automationv1.QueueItem, hist []*automationv1.DownloadRecord) (detail, label string) {
	best := bestHistoryRecord(queueItemIDs(itemType, tmdb, title, queue), hist)
	if best == nil {
		return "", ""
	}
	return strings.TrimSpace(best.GetStatusDetail()), strings.TrimSpace(best.GetStatusLabel())
}

func seasonZeroPlaceholder(itemType string, q *automationv1.QueueItem) bool {
	return itemType == "tv" && q.GetSeasonNumber() == 0 && q.GetEpisodeNumber() >= 1
}

func deriveAcquisitionStatus(itemType string, tmdb int32, title string, queue []*automationv1.QueueItem, hist []*automationv1.DownloadRecord) string {
	title = strings.TrimSpace(title)
	var ids []string
	missing, owned := 0, 0
	for _, q := range queue {
		if q == nil {
			continue
		}
		if q.GetItemType() != itemType {
			continue
		}
		match := tmdb > 0 && q.GetTmdbId() == tmdb
		if !match && tmdb == 0 && title != "" && strings.EqualFold(strings.TrimSpace(q.GetTitle()), title) {
			match = true
		}
		if !match {
			continue
		}
		ids = append(ids, q.GetItemId())
		if seasonZeroPlaceholder(itemType, q) {
			continue
		}
		if q.GetMissing() {
			missing++
		} else {
			owned++
		}
	}
	if len(ids) == 0 {
		return ""
	}
	idSet := map[string]struct{}{}
	for _, id := range ids {
		idSet[id] = struct{}{}
	}
	var bestHistStatus string
	bestHistPri := 0
	hasCompleted := false
	for _, h := range hist {
		if h == nil {
			continue
		}
		if _, ok := idSet[h.GetWantedItemId()]; !ok {
			continue
		}
		switch h.GetStatus() {
		case "sent", "stalled", "import_failed", "failed":
			if p := historyStatusPriority(h.GetStatus()); p > bestHistPri {
				bestHistPri = p
				bestHistStatus = h.GetStatus()
			}
		case "completed":
			hasCompleted = true
		}
	}
	if owned > 0 && missing == 0 {
		return "available"
	}
	if bestHistStatus != "" {
		return historyToRequestStatus(bestHistStatus)
	}
	if (owned > 0 && missing > 0) || (hasCompleted && missing > 0) {
		return "downloading"
	}
	if missing > 0 {
		return "searching"
	}
	return ""
}

func (m *Module) statusLoop(ctx context.Context) {
	time.Sleep(8 * time.Second)
	m.refreshAcquisitionStatus(ctx)
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.refreshAcquisitionStatus(ctx)
		}
	}
}

func (m *Module) refreshAcquisitionStatus(ctx context.Context) {
	if err := m.ensureAutomation(ctx); err != nil {
		return
	}
	m.mu.RLock()
	ac := m.automationClient
	m.mu.RUnlock()
	if ac == nil {
		return
	}
	qctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	queue, err := fetchAllQueue(qctx, ac)
	if err != nil {
		slog.Debug("request-media: queue for status", "error", err)
		return
	}
	hist, err := fetchRecentHistory(qctx, ac)
	if err != nil {
		slog.Debug("request-media: history for status", "error", err)
		hist = nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, rec := range m.requests {
		if rec == nil {
			continue
		}
		switch rec.Status {
		case "pending", "denied":
			continue
		}
		next := deriveAcquisitionStatus(rec.ItemType, rec.TMDBID, rec.Title, queue, hist)
		if next == "" || statusRank(next) <= statusRank(rec.Status) {
			continue
		}
		rec.Status = next
		rec.UpdatedAt = time.Now().UTC()
		if m.store != nil {
			if err := m.store.Put(toStoreRecord(rec)); err != nil {
				slog.Warn("persist request status", "id", rec.ID, "status", next, "error", err)
			}
		}
		slog.Info("request status updated", "id", rec.ID, "title", rec.Title, "status", next)
	}
}

func fetchAllQueue(ctx context.Context, ac automationv1.AutomationServiceClient) ([]*automationv1.QueueItem, error) {
	var out []*automationv1.QueueItem
	page := int32(1)
	for {
		resp, err := ac.GetQueue(ctx, &automationv1.GetQueueRequest{Page: page, PageSize: 100})
		if err != nil {
			return nil, err
		}
		out = append(out, resp.GetItems()...)
		if len(resp.GetItems()) == 0 || int32(len(out)) >= resp.GetTotal() || len(resp.GetItems()) < 100 {
			break
		}
		page++
		if page > 50 {
			break
		}
	}
	return out, nil
}

func fetchRecentHistory(ctx context.Context, ac automationv1.AutomationServiceClient) ([]*automationv1.DownloadRecord, error) {
	var out []*automationv1.DownloadRecord
	page := int32(1)
	for page <= 8 {
		resp, err := ac.GetHistory(ctx, &automationv1.GetHistoryRequest{Page: page, PageSize: 100})
		if err != nil {
			return nil, err
		}
		out = append(out, resp.GetRecords()...)
		if len(resp.GetRecords()) == 0 || int32(len(out)) >= resp.GetTotal() || len(resp.GetRecords()) < 100 {
			break
		}
		page++
	}
	return out, nil
}

type automationSnapshot struct {
	queue []*automationv1.QueueItem
	hist  []*automationv1.DownloadRecord
}

func (m *Module) fetchAutomationSnapshot(ctx context.Context) (*automationSnapshot, error) {
	if err := m.ensureAutomation(ctx); err != nil {
		return nil, err
	}
	m.mu.RLock()
	ac := m.automationClient
	m.mu.RUnlock()
	if ac == nil {
		return nil, fmt.Errorf("automation client unavailable")
	}
	queue, err := fetchAllQueue(ctx, ac)
	if err != nil {
		return nil, err
	}
	hist, err := fetchRecentHistory(ctx, ac)
	if err != nil {
		return nil, err
	}
	return &automationSnapshot{queue: queue, hist: hist}, nil
}

func applyHistoryStatusFields(rec *requestRecord, snap *automationSnapshot) (detail, label string) {
	if rec == nil || snap == nil || !isInProgressRequestStatus(rec.Status) {
		return "", ""
	}
	return deriveHistoryStatusFields(rec.ItemType, rec.TMDBID, rec.Title, snap.queue, snap.hist)
}
