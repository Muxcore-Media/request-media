package internal

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Muxcore-Media/request-media/internal/reqquota"
)

func TestHandleRequest_RoutesTVAndQuality(t *testing.T) {
	m := testModule(t)
	body := `{"tmdbId":1396,"title":"Breaking Bad","year":2008,"mediaType":"tv","qualityProfile":"4K","overview":"chemist"}`
	r := httptest.NewRequest(http.MethodPost, "/api/request", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Caller-Id", "test-user")
	w := httptest.NewRecorder()
	m.handleRequest(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("json: %v", err)
	}
	if out["status"] != "requested" {
		t.Fatalf("status %q want requested (no tv library)", out["status"])
	}
	id := out["requestId"]
	if !strings.HasPrefix(id, "req_tv_") {
		t.Fatalf("requestId %q", id)
	}
	m.mu.RLock()
	rec := m.requests[id]
	m.mu.RUnlock()
	if rec == nil || rec.ItemType != "tv" || rec.QualityProfileID != "4k" || rec.Title != "Breaking Bad" {
		t.Fatalf("record %+v", rec)
	}
}

func TestHandleRequest_MovieQualityHD(t *testing.T) {
	m := testModule(t)
	body := `{"tmdbId":550,"title":"Fight Club","year":1999,"mediaType":"movie","qualityProfile":"hd"}`
	r := httptest.NewRequest(http.MethodPost, "/api/request", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Caller-Id", "test-user")
	w := httptest.NewRecorder()
	m.handleRequest(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("json: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var rec *requestRecord
	for time.Now().Before(deadline) {
		m.mu.RLock()
		rec = m.requests[out["requestId"]]
		m.mu.RUnlock()
		if rec != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if rec == nil || rec.ItemType != "movie" || rec.QualityProfileID != "hd" {
		t.Fatalf("record %+v", rec)
	}
}

func TestHandleRequest_WeeklyQuota(t *testing.T) {
	m := testModule(t)
	m.setQuotaPolicy(reqquota.Policy{MaxPerWeek: 1})
	body := `{"tmdbId":550,"title":"Fight Club","year":1999,"mediaType":"movie"}`
	first := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/request", strings.NewReader(body))
	req.Header.Set("X-Caller-Id", "quota-user")
	m.handleRequest(first, req)
	if first.Code != http.StatusOK {
		t.Fatalf("first %d %s", first.Code, first.Body.String())
	}
	second := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/request", strings.NewReader(body))
	req2.Header.Set("X-Caller-Id", "quota-user")
	m.handleRequest(second, req2)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second %d %s", second.Code, second.Body.String())
	}
	if !strings.Contains(second.Body.String(), "request.quota") {
		t.Fatalf("body %s", second.Body.String())
	}
}
