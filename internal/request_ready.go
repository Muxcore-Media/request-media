package internal

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	eventsv1 "github.com/Muxcore-Media/core/proto/gen/muxcore/events/v1"
)

// EventRequestReady is published once when a user-requested title becomes
// playable (library has_file / file-added). Same semantics as media-ui ready
// toasts — not status=available. Consumed by playback-monitor Discord/webhooks.
const EventRequestReady = "media.request.ready"

var readyFileEventTypes = []string{
	"media.movie.file.added",
	"media.movie.file_added",
	"media.tv.episode.file.added",
	"media.tv.episode_file_added",
	"media.file.imported",
}

type fileReadyHint struct {
	MovieID  string
	SeriesID string
	ItemID   string
	ItemType string
	TMDBID   int32
}

func (m *Module) runReadySubscriptions(ctx context.Context) {
	if m.mc == nil {
		return
	}
	var wg sync.WaitGroup
	for _, et := range readyFileEventTypes {
		ch, cancel, err := m.mc.Events.Subscribe(ctx, et)
		if err != nil {
			slog.Debug("request-media: subscribe file-ready failed", "type", et, "error", err)
			continue
		}
		wg.Add(1)
		go func(events <-chan *eventsv1.Event, eventType string, cancel context.CancelFunc) {
			defer wg.Done()
			defer cancel()
			for evt := range events {
				m.handleFileReadyEvent(ctx, eventType, evt)
			}
		}(ch, et, cancel)
	}
	wg.Wait()
}

func (m *Module) handleFileReadyEvent(ctx context.Context, eventType string, evt *eventsv1.Event) {
	if evt == nil || len(evt.Payload) == 0 {
		return
	}
	hint, ok := parseFileReadyHint(eventType, evt.Payload)
	if !ok {
		return
	}
	m.publishReadyForHint(ctx, hint)
}

func parseFileReadyHint(eventType string, raw []byte) (fileReadyHint, bool) {
	var loose map[string]any
	if err := json.Unmarshal(raw, &loose); err != nil {
		return fileReadyHint{}, false
	}
	hint := fileReadyHint{
		MovieID:  jsonMapString(loose, "movie_id"),
		SeriesID: jsonMapString(loose, "series_id"),
		ItemID:   firstNonBlank(jsonMapString(loose, "item_id"), jsonMapString(loose, "movie_id"), jsonMapString(loose, "series_id")),
		TMDBID:   jsonMapInt32(loose, "tmdb_id"),
	}
	switch eventType {
	case "media.movie.file.added", "media.movie.file_added":
		hint.ItemType = "movie"
	case "media.tv.episode.file.added", "media.tv.episode_file_added":
		hint.ItemType = "tv"
	case "media.file.imported":
		hint.ItemType = strings.ToLower(jsonMapString(loose, "media_type"))
	}
	if hint.MovieID == "" && hint.SeriesID == "" && hint.ItemID == "" && hint.TMDBID == 0 {
		return fileReadyHint{}, false
	}
	return hint, true
}

func jsonMapString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func jsonMapInt32(m map[string]any, key string) int32 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch t := v.(type) {
	case float64:
		return int32(t)
	case json.Number:
		n, _ := t.Int64()
		return int32(n)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 32)
		return int32(n)
	default:
		return 0
	}
}

func firstNonBlank(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func isWatchableForReady(rec *requestRecord) bool {
	if rec == nil {
		return false
	}
	switch rec.Status {
	case StatusDenied, StatusWatchlisted, StatusPending:
		return false
	default:
		return true
	}
}

func requestMatchesFileReady(rec *requestRecord, hint fileReadyHint) bool {
	if !isWatchableForReady(rec) {
		return false
	}
	if rec.ItemID != "" {
		if hint.MovieID != "" && rec.ItemID == hint.MovieID {
			return true
		}
		if hint.SeriesID != "" && rec.ItemID == hint.SeriesID {
			return true
		}
		if hint.ItemID != "" && rec.ItemID == hint.ItemID {
			return true
		}
	}
	if rec.TMDBID != 0 && hint.TMDBID != 0 && rec.TMDBID == hint.TMDBID {
		if hint.ItemType == "" || strings.EqualFold(hint.ItemType, rec.ItemType) {
			return true
		}
	}
	return false
}

func (m *Module) publishReadyForHint(ctx context.Context, hint fileReadyHint) {
	m.mu.RLock()
	matches := make([]*requestRecord, 0)
	for _, rec := range m.requests {
		if requestMatchesFileReady(rec, hint) {
			cp := *rec
			matches = append(matches, &cp)
		}
	}
	m.mu.RUnlock()
	for _, rec := range matches {
		m.publishRequestReadyOnce(ctx, rec)
	}
}

func (m *Module) publishRequestReadyOnce(ctx context.Context, rec *requestRecord) bool {
	if rec == nil || rec.ID == "" {
		return false
	}
	if m.store != nil {
		claimed, err := m.store.ClaimReadyNotified(rec.ID)
		if err != nil {
			slog.Debug("request-media: ready dedupe failed", "request_id", rec.ID, "error", err)
			return false
		}
		if !claimed {
			return false
		}
	}
	m.publish(ctx, EventRequestReady, map[string]interface{}{
		"request_id":   rec.ID,
		"requested_by": rec.RequestedBy,
		"title":        rec.Title,
		"year":         rec.Year,
		"item_type":    rec.ItemType,
		"tmdb_id":      rec.TMDBID,
		"item_id":      rec.ItemID,
	})
	return true
}
