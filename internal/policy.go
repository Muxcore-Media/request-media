package internal

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Muxcore-Media/request-media/internal/authz"
	"github.com/Muxcore-Media/request-media/internal/reqquota"
)

type requestPolicyFile struct {
	MaxPendingPerUser int      `json:"maxPendingPerUser"`
	MaxPerWeek        int      `json:"maxPerWeek"`
	AutoApproveUsers  []string `json:"autoApproveUsers"`
}

func (m *Module) policyPath() string {
	if strings.TrimSpace(m.dataDir) == "" {
		return ""
	}
	return filepath.Join(m.dataDir, "request-policy.json")
}

func (m *Module) loadPolicyFile() {
	path := m.policyPath()
	if path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var f requestPolicyFile
	if json.Unmarshal(b, &f) != nil {
		return
	}
	m.cfgMu.Lock()
	m.quotaMaxPending = f.MaxPendingPerUser
	m.quotaMaxPerWeek = f.MaxPerWeek
	m.autoApproveUsers = append([]string(nil), f.AutoApproveUsers...)
	m.cfgMu.Unlock()
}

func (m *Module) persistPolicyFile() {
	path := m.policyPath()
	if path == "" {
		return
	}
	m.policyFileMu.Lock()
	defer m.policyFileMu.Unlock()
	p := m.getQuotaPolicy()
	if err := writePolicyFileAtomic(path, requestPolicyFile{
		MaxPendingPerUser: p.MaxPendingPerUser,
		MaxPerWeek:        p.MaxPerWeek,
		AutoApproveUsers:  p.AutoApproveUsers,
	}); err != nil {
		slog.Warn("request-media: persist policy file failed", "error", err)
	}
}

func (m *Module) getQuotaPolicy() reqquota.Policy {
	m.cfgMu.RLock()
	defer m.cfgMu.RUnlock()
	return reqquota.Policy{
		MaxPendingPerUser: m.quotaMaxPending,
		MaxPerWeek:        m.quotaMaxPerWeek,
		AutoApproveUsers:  append([]string(nil), m.autoApproveUsers...),
	}
}

func (m *Module) setQuotaPolicy(p reqquota.Policy) {
	// An erased id never re-enters the auto-approve list (ADR-0035).
	if m.store != nil {
		var kept []string
		for _, u := range p.AutoApproveUsers {
			if erased, err := m.store.UserErased(context.Background(), u); err == nil && !erased {
				kept = append(kept, u)
			}
		}
		p.AutoApproveUsers = kept
	}
	m.cfgMu.Lock()
	m.quotaMaxPending = p.MaxPendingPerUser
	m.quotaMaxPerWeek = p.MaxPerWeek
	m.autoApproveUsers = append([]string(nil), p.AutoApproveUsers...)
	m.cfgMu.Unlock()
	m.persistPolicyFile()
}

func (m *Module) quotaRecords() []reqquota.Record {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]reqquota.Record, 0, len(m.requests))
	for _, rec := range m.requests {
		if rec == nil {
			continue
		}
		out = append(out, reqquota.Record{
			RequestedBy: rec.RequestedBy,
			Status:      rec.Status,
			CreatedAt:   rec.CreatedAt,
		})
	}
	return out
}

func (m *Module) decideQuota(userID string) reqquota.Decision {
	return reqquota.Decide(m.getQuotaPolicy(), userID, m.quotaRecords(), time.Now().UTC())
}

func (m *Module) enforceQuota(userID string, autoApprove bool) error {
	d := m.decideQuota(userID)
	if autoApprove && d.Code == reqquota.CodeQuotaPending {
		return nil
	}
	if !d.Allow {
		return status.Error(codes.ResourceExhausted, d.Reason)
	}
	return nil
}

func (m *Module) handleRequestPolicy(w http.ResponseWriter, r *http.Request) {
	ctx, ok := m.httpCallerCtx(w, r)
	if !ok {
		return
	}
	caller := authz.CallerID(ctx)
	switch r.Method {
	case http.MethodGet:
		if err := m.authz.RequireCreate(ctx, caller); err != nil {
			if err2 := m.authz.RequireList(ctx, caller); err2 != nil {
				writeGRPCError(w, err)
				return
			}
		}
		d := m.decideQuota(caller)
		p := m.getQuotaPolicy()
		canEdit := m.authz.CanApprove(ctx, caller)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"maxPendingPerUser": p.MaxPendingPerUser,
			"maxPerWeek":        p.MaxPerWeek,
			"autoApproveUsers":  p.AutoApproveUsers,
			"pendingUsed":       d.PendingUsed,
			"weekUsed":          d.WeekUsed,
			"remainingPending":  d.RemainingPending,
			"remainingWeek":     d.RemainingWeek,
			"canRequest":        d.Allow,
			"autoApprove":       d.AutoApprove || canEdit,
			"canEdit":           canEdit,
			"code":              d.Code,
			"reason":            d.Reason,
		})
	case http.MethodPut, http.MethodPost:
		if err := m.authz.RequireApprove(ctx, caller); err != nil {
			writeGRPCError(w, err)
			return
		}
		var in struct {
			MaxPendingPerUser *int     `json:"maxPendingPerUser"`
			MaxPerWeek        *int     `json:"maxPerWeek"`
			AutoApproveUsers  []string `json:"autoApproveUsers"`
			AutoApproveCSV    string   `json:"autoApproveUsersCsv"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		cur := m.getQuotaPolicy()
		if in.MaxPendingPerUser != nil && *in.MaxPendingPerUser >= 0 {
			cur.MaxPendingPerUser = *in.MaxPendingPerUser
		}
		if in.MaxPerWeek != nil && *in.MaxPerWeek >= 0 {
			cur.MaxPerWeek = *in.MaxPerWeek
		}
		if in.AutoApproveUsers != nil {
			cur.AutoApproveUsers = reqquota.ParseUsers(strings.Join(in.AutoApproveUsers, ","))
		} else if in.AutoApproveCSV != "" {
			cur.AutoApproveUsers = reqquota.ParseUsers(in.AutoApproveCSV)
		}
		m.setQuotaPolicy(cur)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"maxPendingPerUser": cur.MaxPendingPerUser,
			"maxPerWeek":        cur.MaxPerWeek,
			"autoApproveUsers":  cur.AutoApproveUsers,
			"canEdit":           true,
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func policyFromEnv() reqquota.Policy {
	p := reqquota.Policy{}
	if v := strings.TrimSpace(os.Getenv("REQUEST_MAX_PENDING_PER_USER")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			p.MaxPendingPerUser = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("REQUEST_MAX_PER_WEEK")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			p.MaxPerWeek = n
		}
	}
	p.AutoApproveUsers = reqquota.ParseUsers(os.Getenv("REQUEST_AUTO_APPROVE_USERS"))
	return p
}
