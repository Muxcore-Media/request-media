// Package authn resolves end-user identity from bearer tokens (ADR-0019,
// NFR-SEC-007). Identity headers are never trusted on their own.
package authn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	authv1 "github.com/Muxcore-Media/core/proto/gen/muxcore/auth/v1"
	"github.com/Muxcore-Media/request-media/internal/authz"
	"github.com/Muxcore-Media/request-media/internal/grpctls"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

const (
	// CacheTTL is how long a successful token resolution is cached.
	CacheTTL = 30 * time.Second
	cacheMax = 1024

	// EnvTrustCallerHeader enables header-only identity (dev only; also requires
	// MUXCORE_INSECURE_DISABLE_TLS=true).
	EnvTrustCallerHeader = "REQUEST_TRUST_CALLER_HEADER"
	// EnvAuthAddr pins the identity provider gRPC address (skips discovery).
	EnvAuthAddr = "AUTH_LOCAL_GRPC_ADDR"
	// EnvModulePrincipals lists mesh module identities (client cert CN) that may
	// call the maintainer RPCs without a user token.
	EnvModulePrincipals = "REQUEST_MODULE_PRINCIPALS"
	// DefaultModulePrincipals is used when EnvModulePrincipals is unset.
	DefaultModulePrincipals = "media-library-maintainer"

	HeaderCallerID = "X-Caller-Id"
	// HeaderLegacyUser is only honoured on the legacy dev path.
	HeaderLegacyUser = "X-MuxCore-User"
)

// Identity is the authenticated end user behind a bearer token.
type Identity struct {
	ID     string
	Roles  []string
	Tenant string
}

// Resolver resolves a bearer token. It returns (nil, nil) for an unknown token
// and an error when the provider cannot be reached.
type Resolver interface {
	ResolveToken(ctx context.Context, token string) (*Identity, error)
}

// BearerToken extracts the token from an Authorization header value.
func BearerToken(h string) string {
	h = strings.TrimSpace(h)
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// LegacyHeaderTrusted reports whether header-only identity is allowed: it needs
// both MUXCORE_INSECURE_DISABLE_TLS=true and REQUEST_TRUST_CALLER_HEADER=1.
func LegacyHeaderTrusted() bool {
	return os.Getenv("MUXCORE_INSECURE_DISABLE_TLS") == "true" &&
		os.Getenv(EnvTrustCallerHeader) == "1"
}

var legacyWarn sync.Once

func warnLegacy() {
	legacyWarn.Do(func() {
		slog.Warn("request-media: trusting caller identity headers without a token (legacy dev mode)",
			"env", EnvTrustCallerHeader, "requires", "MUXCORE_INSECURE_DISABLE_TLS=true")
	})
}

// Caching caches successful resolutions for CacheTTL keyed by sha256(token).
type Caching struct {
	next Resolver
	mu   sync.Mutex
	m    map[string]cached
	// Now is overridable in tests.
	Now func() time.Time
	TTL time.Duration
}

type cached struct {
	id  Identity
	exp time.Time
}

// NewCaching wraps next with a 30 s cache.
func NewCaching(next Resolver) *Caching {
	return &Caching{next: next, m: map[string]cached{}, Now: time.Now, TTL: CacheTTL}
}

func (c *Caching) ResolveToken(ctx context.Context, token string) (*Identity, error) {
	sum := sha256.Sum256([]byte(token))
	key := hex.EncodeToString(sum[:])
	now := c.Now()
	c.mu.Lock()
	if e, ok := c.m[key]; ok && now.Before(e.exp) {
		c.mu.Unlock()
		id := e.id
		return &id, nil
	}
	c.mu.Unlock()
	id, err := c.next.ResolveToken(ctx, token)
	if err != nil || id == nil {
		return id, err
	}
	c.mu.Lock()
	if len(c.m) >= cacheMax {
		for k, e := range c.m {
			if !now.Before(e.exp) {
				delete(c.m, k)
			}
		}
		if len(c.m) >= cacheMax {
			c.m = map[string]cached{}
		}
	}
	c.m[key] = cached{id: *id, exp: now.Add(c.TTL)}
	c.mu.Unlock()
	return id, nil
}

// GRPCResolver resolves tokens through the identity provider's ExtractIdentity.
type GRPCResolver struct {
	// FindAddr discovers the identity provider (capability "identity") via core.
	FindAddr func(ctx context.Context) (string, error)

	mu    sync.Mutex
	conn  *grpc.ClientConn
	addr  string
	creds credentials.TransportCredentials
}

func (g *GRPCResolver) client(ctx context.Context) (authv1.AuthServiceClient, error) {
	addr := strings.TrimSpace(os.Getenv(EnvAuthAddr))
	if addr == "" {
		if g.FindAddr == nil {
			return nil, errors.New("identity provider address not configured")
		}
		var err error
		if addr, err = g.FindAddr(ctx); err != nil {
			return nil, fmt.Errorf("discover identity provider: %w", err)
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.conn != nil && g.addr == addr {
		return authv1.NewAuthServiceClient(g.conn), nil
	}
	creds, err := grpctls.ClientCredentials()
	if err != nil {
		return nil, err
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("dial identity provider %s: %w", addr, err)
	}
	if g.conn != nil {
		_ = g.conn.Close()
	}
	g.conn, g.addr = conn, addr
	return authv1.NewAuthServiceClient(conn), nil
}

func (g *GRPCResolver) ResolveToken(ctx context.Context, token string) (*Identity, error) {
	cli, err := g.client(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := cli.ExtractIdentity(ctx, &authv1.ExtractIdentityRequest{Token: token})
	if err != nil {
		return nil, fmt.Errorf("identity provider: %w", err)
	}
	if !resp.GetFound() || strings.TrimSpace(resp.GetId()) == "" {
		return nil, nil
	}
	return &Identity{ID: resp.GetId(), Roles: resp.GetRoles(), Tenant: resp.GetTenantId()}, nil
}

// Close releases the provider connection.
func (g *GRPCResolver) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.conn != nil {
		_ = g.conn.Close()
		g.conn = nil
	}
}

// HTTPCaller authenticates an HTTP request and returns a context carrying the
// caller. On failure it returns the status and message to send.
func HTTPCaller(r *http.Request, res Resolver) (context.Context, int, string) {
	token := BearerToken(r.Header.Get("Authorization"))
	claimed := strings.TrimSpace(r.Header.Get(HeaderCallerID))
	if token == "" {
		if LegacyHeaderTrusted() {
			warnLegacy()
			caller := claimed
			if caller == "" {
				caller = strings.TrimSpace(r.Header.Get(HeaderLegacyUser))
			}
			if caller != "" {
				return authz.WithCallerID(r.Context(), caller), 0, ""
			}
		}
		return nil, http.StatusUnauthorized, "missing bearer token"
	}
	id, code, msg := resolve(r.Context(), res, token)
	if code != 0 {
		return nil, code, msg
	}
	if claimed != "" && claimed != id.ID {
		return nil, http.StatusForbidden, "caller identity does not match token"
	}
	return authz.WithCallerID(r.Context(), id.ID), 0, ""
}

func resolve(ctx context.Context, res Resolver, token string) (*Identity, int, string) {
	if res == nil {
		return nil, http.StatusServiceUnavailable, "identity provider not configured"
	}
	id, err := res.ResolveToken(ctx, token)
	if err != nil {
		slog.Warn("request-media: identity resolution failed", "error", err)
		return nil, http.StatusServiceUnavailable, "identity provider unavailable"
	}
	if id == nil || strings.TrimSpace(id.ID) == "" {
		return nil, http.StatusUnauthorized, "invalid token"
	}
	return id, 0, ""
}

// ModulePrincipals parses the allowed mesh module identities.
func ModulePrincipals() []string {
	v, ok := os.LookupEnv(EnvModulePrincipals)
	if !ok {
		v = DefaultModulePrincipals
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// verifiedPeerCN returns the CN of the verified mTLS client certificate.
func verifiedPeerCN(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return ""
	}
	ti, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(ti.State.VerifiedChains) == 0 || len(ti.State.VerifiedChains[0]) == 0 {
		return ""
	}
	return ti.State.VerifiedChains[0][0].Subject.CommonName
}

// UnaryInterceptor resolves `authorization` metadata to a caller. Calls without a
// token continue unauthenticated (handlers fail closed) unless the verified mesh
// peer is one of allowedModules, which is recorded for the maintainer RPCs only
// (see authz.WithModulePrincipal). resolver is read per call via get.
func UnaryInterceptor(get func() Resolver, allowedModules []string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		var token, claimed string
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if v := md.Get("authorization"); len(v) > 0 {
				token = BearerToken(v[0])
			}
			if v := md.Get("x-caller-id"); len(v) > 0 {
				claimed = strings.TrimSpace(v[0])
			}
		}
		if token == "" {
			if cn := verifiedPeerCN(ctx); cn != "" {
				for _, a := range allowedModules {
					if a == cn {
						ctx = authz.WithModulePrincipal(ctx, cn)
						break
					}
				}
			}
			return h(ctx, req)
		}
		id, code, msg := resolve(ctx, get(), token)
		switch code {
		case http.StatusUnauthorized:
			return nil, status.Error(codes.Unauthenticated, msg)
		case http.StatusServiceUnavailable:
			return nil, status.Error(codes.Unavailable, msg)
		}
		if claimed != "" && claimed != id.ID {
			return nil, status.Error(codes.PermissionDenied, "caller identity does not match token")
		}
		return h(authz.WithCallerID(ctx, id.ID), req)
	}
}
