package reqquota

import (
	"strings"
	"time"
)

// Policy is household request limits (Seerr-style). Zero means unlimited.
type Policy struct {
	MaxPendingPerUser int
	MaxPerWeek        int
	AutoApproveUsers  []string
}

// Record is the subset of a media request needed to evaluate quotas.
type Record struct {
	RequestedBy string
	Status      string
	CreatedAt   time.Time
}

// Decision is the result of evaluating a user's quota.
type Decision struct {
	Allow             bool
	AutoApprove       bool
	Code              string
	Reason            string
	PendingUsed       int
	WeekUsed          int
	RemainingPending  int // -1 unlimited
	RemainingWeek     int // -1 unlimited
	MaxPendingPerUser int
	MaxPerWeek        int
}

const (
	CodeOK           = ""
	CodeQuotaPending = "request.quota_pending"
	CodeQuotaWeek    = "request.quota_week"
)

// UserAutoApproved reports whether userID is on the trusted auto-approve list.
func UserAutoApproved(p Policy, userID string) bool {
	want := normalizeUser(userID)
	if want == "" {
		return false
	}
	for _, raw := range p.AutoApproveUsers {
		if normalizeUser(raw) == want {
			return true
		}
	}
	return false
}

// ParseUsers splits a comma/whitespace list of household user ids.
func ParseUsers(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\t' || r == ' '
	})
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || seen[normalizeUser(p)] {
			continue
		}
		seen[normalizeUser(p)] = true
		out = append(out, p)
	}
	return out
}

func normalizeUser(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func countsTowardWeek(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "denied", "watchlisted", "":
		return false
	default:
		return true
	}
}

func isPending(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), "pending")
}

// Decide evaluates whether userID may submit another request at now.
// Auto-approved users skip the pending cap but still honor the weekly cap.
func Decide(p Policy, userID string, recs []Record, now time.Time) Decision {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	weekStart := now.UTC().Add(-7 * 24 * time.Hour)
	want := normalizeUser(userID)
	pending := 0
	week := 0
	for _, rec := range recs {
		if normalizeUser(rec.RequestedBy) != want {
			continue
		}
		if isPending(rec.Status) {
			pending++
		}
		if countsTowardWeek(rec.Status) && !rec.CreatedAt.IsZero() && !rec.CreatedAt.Before(weekStart) {
			week++
		}
	}

	d := Decision{
		Allow:             true,
		AutoApprove:       UserAutoApproved(p, userID),
		PendingUsed:       pending,
		WeekUsed:          week,
		RemainingPending:  -1,
		RemainingWeek:     -1,
		MaxPendingPerUser: p.MaxPendingPerUser,
		MaxPerWeek:        p.MaxPerWeek,
	}
	if p.MaxPendingPerUser > 0 {
		d.RemainingPending = p.MaxPendingPerUser - pending
		if d.RemainingPending < 0 {
			d.RemainingPending = 0
		}
	}
	if p.MaxPerWeek > 0 {
		d.RemainingWeek = p.MaxPerWeek - week
		if d.RemainingWeek < 0 {
			d.RemainingWeek = 0
		}
	}

	if p.MaxPerWeek > 0 && week >= p.MaxPerWeek {
		d.Allow = false
		d.Code = CodeQuotaWeek
		d.Reason = "Weekly request limit reached. Try again next week or ask a household admin."
		return d
	}
	if !d.AutoApprove && p.MaxPendingPerUser > 0 && pending >= p.MaxPendingPerUser {
		d.Allow = false
		d.Code = CodeQuotaPending
		d.Reason = "You already have the maximum number of pending requests. Wait for approval before requesting more."
		return d
	}
	return d
}
