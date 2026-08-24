package internal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Muxcore-Media/core/pkg/tenant"
	requestmedia "github.com/Muxcore-Media/request-media/proto/requestmedia"
)

func TestTenantIsolation_Requests(t *testing.T) {
	t.Setenv("TENANT_MODE", "1")
	m := testModule(t)

	ctxA := tenant.WithID(context.Background(), "tenant-a")
	ctxB := tenant.WithID(context.Background(), "tenant-b")

	respA, err := m.RequestMovie(ctxA, &requestmedia.RequestMovieRequest{
		TmdbId: 550, Title: "Fight Club", Year: 1999, RequestedBy: "alice",
	})
	if err != nil {
		t.Fatalf("tenant-a create: %v", err)
	}

	listB, err := m.ListRequests(ctxB, &requestmedia.ListRequestsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listB.GetRequests()) != 0 {
		t.Fatalf("tenant-b must not see tenant-a requests, got %d", len(listB.GetRequests()))
	}

	listA, err := m.ListRequests(ctxA, &requestmedia.ListRequestsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listA.GetRequests()) != 1 || listA.GetRequests()[0].GetRequestId() != respA.GetRequestId() {
		t.Fatalf("tenant-a list = %+v", listA.GetRequests())
	}

	_, err = m.GetStatus(ctxB, &requestmedia.GetStatusRequest{RequestId: respA.GetRequestId()})
	if err == nil {
		t.Fatal("tenant-b must not GetStatus tenant-a request")
	}

	// Same TMDB under another tenant is allowed (scoped dedupe).
	respB, err := m.RequestMovie(ctxB, &requestmedia.RequestMovieRequest{
		TmdbId: 550, Title: "Fight Club", Year: 1999, RequestedBy: "bob",
	})
	if err != nil {
		t.Fatalf("tenant-b create: %v", err)
	}
	if respB.GetRequestId() == respA.GetRequestId() {
		t.Fatal("tenants must get distinct request ids")
	}

	m.mu.RLock()
	recA := m.requests[respA.GetRequestId()]
	m.mu.RUnlock()
	if recA == nil || recA.TenantID != "tenant-a" {
		t.Fatalf("persisted tenant = %#v", recA)
	}

	pathA := m.store.PathFor("tenant-a")
	pathB := m.store.PathFor("tenant-b")
	if pathA == pathB {
		t.Fatalf("expected per-tenant DB files, got %q", pathA)
	}
	if _, err := m.store.Get(respA.GetRequestId(), "tenant-b"); err == nil {
		t.Fatal("cross-tenant store Get must fail")
	}
}

func TestTenantDefaultsWhenMissing(t *testing.T) {
	t.Setenv("TENANT_MODE", "1")
	m := testModule(t)

	resp, err := m.RequestMovie(context.Background(), &requestmedia.RequestMovieRequest{
		TmdbId: 218, Title: "The Terminator", Year: 1984,
	})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.RLock()
	rec := m.requests[resp.GetRequestId()]
	m.mu.RUnlock()
	if rec.TenantID != "default" {
		t.Fatalf("expected default tenant, got %q", rec.TenantID)
	}
}

func TestHTTPRequestResolvesTenantHeader(t *testing.T) {
	t.Setenv("TENANT_MODE", "1")
	m := testModule(t)
	body := `{"tmdbId":42,"title":"X","year":2020,"mediaType":"movie"}`
	r := httptest.NewRequest(http.MethodPost, "/api/request", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Tenant-ID", "org-z")
	r.Header.Set("X-MuxCore-Roles", "user")
	r.Header.Set("X-MuxCore-User", "tester")
	r = r.WithContext(tenant.WithID(r.Context(), "org-z"))
	w := httptest.NewRecorder()
	m.handleRequest(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var out map[string]string
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	m.mu.RLock()
	rec := m.requests[out["requestId"]]
	m.mu.RUnlock()
	if rec == nil || rec.TenantID != "org-z" {
		t.Fatalf("tenant=%#v", rec)
	}
}

func TestHTTPCrossTenantApproveDeniedWithoutAdmin(t *testing.T) {
	t.Setenv("TENANT_MODE", "1")
	m := testModule(t)

	ctxA := tenant.WithID(context.Background(), "tenant-a")
	respA, err := m.RequestMovie(ctxA, &requestmedia.RequestMovieRequest{
		TmdbId: 99, Title: "Cross", Year: 2020, RequestedBy: "alice",
	})
	if err != nil {
		t.Fatal(err)
	}

	body := `{"by":"eve"}`
	r := httptest.NewRequest(http.MethodPost, "/api/requests/"+respA.GetRequestId()+"/approve", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Tenant-ID", "tenant-b")
	r.Header.Set("X-Auth-Claims-Tenant", "tenant-b")
	r.Header.Set("X-MuxCore-Roles", "user")
	r = r.WithContext(tenant.WithID(r.Context(), "tenant-b"))
	w := httptest.NewRecorder()
	m.handleRequestAction(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestHTTPCrossTenantApproveAllowedForAdmin(t *testing.T) {
	t.Setenv("TENANT_MODE", "1")
	m := testModule(t)

	ctxA := tenant.WithID(context.Background(), "tenant-a")
	respA, err := m.RequestMovie(ctxA, &requestmedia.RequestMovieRequest{
		TmdbId: 100, Title: "AdminCross", Year: 2021, RequestedBy: "alice", IsAdmin: false,
	})
	if err != nil {
		t.Fatal(err)
	}

	body := `{"by":"root"}`
	r := httptest.NewRequest(http.MethodPost, "/api/requests/"+respA.GetRequestId()+"/approve", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Tenant-ID", "tenant-b")
	r.Header.Set("X-Auth-Claims-Tenant", "tenant-b")
	r.Header.Set("X-MuxCore-Roles", "admin")
	r = r.WithContext(tenant.WithID(r.Context(), "tenant-b"))
	w := httptest.NewRecorder()
	m.handleRequestAction(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("admin cross-tenant want 200, got %d body=%s", w.Code, w.Body.String())
	}
}
