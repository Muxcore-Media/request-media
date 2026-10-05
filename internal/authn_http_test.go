package internal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Muxcore-Media/request-media/internal/authn"
)

// testResolver maps "tok-<user>" to <user>; "bad" is unknown; "down" errors.
type testResolver struct{}

func (testResolver) ResolveToken(_ context.Context, tok string) (*authn.Identity, error) {
	switch {
	case tok == "down":
		return nil, context.DeadlineExceeded
	case strings.HasPrefix(tok, "tok-"):
		return &authn.Identity{ID: strings.TrimPrefix(tok, "tok-")}, nil
	}
	return nil, nil
}

func setBearer(r *http.Request, user string) {
	r.Header.Set("Authorization", "Bearer tok-"+user)
}

func TestHTTPCaller_Matrix(t *testing.T) {
	t.Setenv("MUXCORE_INSECURE_DISABLE_TLS", "")
	t.Setenv(authn.EnvTrustCallerHeader, "")
	m := testModule(t)
	do := func(h map[string]string) int {
		r := httptest.NewRequest(http.MethodGet, "/api/requests", nil)
		for k, v := range h {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		m.handleRequests(w, r)
		return w.Code
	}
	if c := do(map[string]string{"X-Caller-Id": "admin"}); c != http.StatusUnauthorized {
		t.Fatalf("spoofed header without token = %d, want 401", c)
	}
	if c := do(map[string]string{"X-MuxCore-User": "admin"}); c != http.StatusUnauthorized {
		t.Fatalf("X-MuxCore-User without token = %d, want 401", c)
	}
	if c := do(map[string]string{"Authorization": "Bearer tok-u1"}); c != http.StatusOK {
		t.Fatalf("valid token = %d, want 200", c)
	}
	if c := do(map[string]string{"Authorization": "Bearer tok-u1", "X-Caller-Id": "u1"}); c != http.StatusOK {
		t.Fatalf("matching X-Caller-Id = %d, want 200", c)
	}
	if c := do(map[string]string{"Authorization": "Bearer tok-u1", "X-Caller-Id": "admin"}); c != http.StatusForbidden {
		t.Fatalf("mismatched X-Caller-Id = %d, want 403", c)
	}
	if c := do(map[string]string{"Authorization": "Bearer bad"}); c != http.StatusUnauthorized {
		t.Fatalf("unknown token = %d, want 401", c)
	}
	if c := do(map[string]string{"Authorization": "Bearer down"}); c != http.StatusServiceUnavailable {
		t.Fatalf("provider down = %d, want 503", c)
	}
}

func TestHTTPCaller_LegacyGate(t *testing.T) {
	m := testModule(t)
	do := func() int {
		r := httptest.NewRequest(http.MethodGet, "/api/requests", nil)
		r.Header.Set("X-Caller-Id", "legacy")
		w := httptest.NewRecorder()
		m.handleRequests(w, r)
		return w.Code
	}
	// Only one of the two switches: still rejected.
	t.Setenv("MUXCORE_INSECURE_DISABLE_TLS", "true")
	t.Setenv(authn.EnvTrustCallerHeader, "")
	if c := do(); c != http.StatusUnauthorized {
		t.Fatalf("insecure only = %d, want 401", c)
	}
	t.Setenv("MUXCORE_INSECURE_DISABLE_TLS", "")
	t.Setenv(authn.EnvTrustCallerHeader, "1")
	if c := do(); c != http.StatusUnauthorized {
		t.Fatalf("trust flag only = %d, want 401", c)
	}
	t.Setenv("MUXCORE_INSECURE_DISABLE_TLS", "true")
	if c := do(); c != http.StatusOK {
		t.Fatalf("both set = %d, want 200", c)
	}
}
