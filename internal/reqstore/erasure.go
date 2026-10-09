package reqstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// AnonymisedUser replaces requested_by on requests that outlive an erased
// user (ADR-0035 §3). Provider user ids are random hex, so it cannot collide
// with a real id; EraseUser still refuses it as an erasure target.
const AnonymisedUser = "deleted-user"

// DeletedOnErasure lists the request states whose rows are the user's own
// working data and are deleted, with their request_ready_notified rows, when
// the user is erased. Every other state (requested, added, workflow and any
// legacy value such as available) is the household acquisition record: the
// row stays and requested_by is anonymised.
var DeletedOnErasure = []string{"watchlisted", "pending", "denied"}

// Errors returned by the erasure API.
var (
	// ErrUserErased: the requested_by user has an erasure record; the write
	// is refused so an erased id can never own new rows.
	ErrUserErased = errors.New("reqstore: user has been erased")
	// ErrInvalidErasure: the erasure id or user id is unusable.
	ErrInvalidErasure = errors.New("reqstore: invalid erasure target")
)

// Erasure counts reported to the identity provider.
const (
	CountRequestsDeleted    = "requests_deleted"
	CountRequestsAnonymised = "requests_anonymised"
	CountReadyDeleted       = "ready_notified_deleted"
	// CountAutoApprove is added by the module for the policy-file step.
	CountAutoApprove = "auto_approve_users"
)

// Erasure is one row of erasure_applied.
type Erasure struct {
	AppliedAt time.Time
	Counts    map[string]int64
	ErasureID string
	UserID    string
	TenantID  string
	// PolicyDone is the second phase of the two-phase application: the
	// user id was removed from request-policy.json (a file outside SQLite).
	// The erasure counts as applied only when it is true.
	PolicyDone bool
}

func (s *Store) ensureErasureTable() error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS erasure_applied (
			erasure_id  TEXT PRIMARY KEY,
			user_id     TEXT NOT NULL,
			tenant_id   TEXT NOT NULL DEFAULT '',
			applied_at  TEXT NOT NULL,
			counts_json TEXT NOT NULL DEFAULT '{}',
			policy_done INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_erasure_applied_user ON erasure_applied(user_id)`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate erasure table: %w", err)
		}
	}
	return nil
}

// inList renders a SQL placeholder list for n values.
func inList(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func statusArgs() []any {
	out := make([]any, len(DeletedOnErasure))
	for i, st := range DeletedOnErasure {
		out[i] = st
	}
	return out
}

// EraseUser applies the ADR-0035 disposition for userID in ONE transaction:
// rows in DeletedOnErasure states are deleted together with their
// request_ready_notified rows, every other row is anonymised, and the
// erasure is recorded in erasure_applied with policy_done = 0. Either all of
// it happens or none of it does.
//
// It is idempotent per erasureID: when the erasure is already recorded the
// stored record is returned and nothing changes. The record is written inside
// the same transaction, so after a crash the data and the record are
// consistent; the policy-file phase is finished by the caller and committed
// with MarkPolicyDone.
func (s *Store) EraseUser(ctx context.Context, erasureID, userID, tenantID string) (Erasure, error) {
	erasureID, userID = strings.TrimSpace(erasureID), strings.TrimSpace(userID)
	if erasureID == "" || userID == "" || userID == AnonymisedUser {
		return Erasure{}, ErrInvalidErasure
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Erasure{}, fmt.Errorf("erase user: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if rec, ok, err := loadErasure(ctx, tx, erasureID); err != nil {
		return Erasure{}, err
	} else if ok {
		return rec, nil
	}

	statuses := statusArgs()
	args := append([]any{userID}, statuses...)
	list := inList(len(statuses))
	counts := map[string]int64{}

	res, err := tx.ExecContext(ctx, `DELETE FROM request_ready_notified WHERE request_id IN (
		SELECT id FROM requests WHERE requested_by = ? AND status IN (`+list+`))`, args...)
	if err != nil {
		return Erasure{}, fmt.Errorf("erase user: ready rows: %w", err)
	}
	if counts[CountReadyDeleted], err = res.RowsAffected(); err != nil {
		return Erasure{}, err
	}
	res, err = tx.ExecContext(ctx, `DELETE FROM requests WHERE requested_by = ? AND status IN (`+list+`)`, args...)
	if err != nil {
		return Erasure{}, fmt.Errorf("erase user: delete requests: %w", err)
	}
	if counts[CountRequestsDeleted], err = res.RowsAffected(); err != nil {
		return Erasure{}, err
	}
	res, err = tx.ExecContext(ctx, `UPDATE requests SET requested_by = ? WHERE requested_by = ?`, AnonymisedUser, userID)
	if err != nil {
		return Erasure{}, fmt.Errorf("erase user: anonymise requests: %w", err)
	}
	if counts[CountRequestsAnonymised], err = res.RowsAffected(); err != nil {
		return Erasure{}, err
	}

	raw, err := json.Marshal(counts)
	if err != nil {
		return Erasure{}, err
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO erasure_applied(erasure_id, user_id, tenant_id, applied_at, counts_json, policy_done)
		VALUES (?, ?, ?, ?, ?, 0)`, erasureID, userID, tenantID, now.Format(time.RFC3339Nano), string(raw)); err != nil {
		return Erasure{}, fmt.Errorf("erase user: record: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Erasure{}, fmt.Errorf("erase user: commit: %w", err)
	}
	return Erasure{ErasureID: erasureID, UserID: userID, TenantID: tenantID, AppliedAt: now, Counts: counts}, nil
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func loadErasure(ctx context.Context, q queryRower, erasureID string) (Erasure, bool, error) {
	var (
		rec        Erasure
		applied    string
		countsJSON string
		policyDone int
	)
	err := q.QueryRowContext(ctx, `SELECT erasure_id, user_id, tenant_id, applied_at, counts_json, policy_done
		FROM erasure_applied WHERE erasure_id = ?`, erasureID).
		Scan(&rec.ErasureID, &rec.UserID, &rec.TenantID, &applied, &countsJSON, &policyDone)
	if errors.Is(err, sql.ErrNoRows) {
		return Erasure{}, false, nil
	}
	if err != nil {
		return Erasure{}, false, fmt.Errorf("read erasure record: %w", err)
	}
	rec.PolicyDone = policyDone != 0
	rec.AppliedAt, _ = time.Parse(time.RFC3339Nano, applied)
	rec.Counts = map[string]int64{}
	if countsJSON != "" {
		if err := json.Unmarshal([]byte(countsJSON), &rec.Counts); err != nil {
			return Erasure{}, false, fmt.Errorf("read erasure counts: %w", err)
		}
	}
	return rec, true, nil
}

// Erasure returns the record for erasureID.
func (s *Store) Erasure(ctx context.Context, erasureID string) (Erasure, bool, error) {
	return loadErasure(ctx, s.db, erasureID)
}

// ErasureComplete reports whether erasureID is recorded AND its policy-file
// phase is finished. This is what erasure.Owner.Applied returns: a record
// with policy_done = 0 means the process stopped between the SQLite
// transaction and the file edit, and the next sweep must finish it.
func (s *Store) ErasureComplete(ctx context.Context, erasureID string) (bool, error) {
	rec, ok, err := s.Erasure(ctx, erasureID)
	if err != nil || !ok {
		return false, err
	}
	return rec.PolicyDone, nil
}

// MarkPolicyDone commits the second phase of an erasure and stores the final
// counts. It is idempotent.
func (s *Store) MarkPolicyDone(ctx context.Context, erasureID string, counts map[string]int64) error {
	raw, err := json.Marshal(counts)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE erasure_applied SET policy_done = 1, counts_json = ? WHERE erasure_id = ?`,
		string(raw), erasureID)
	if err != nil {
		return fmt.Errorf("mark policy done: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("mark policy done: erasure %s is not recorded", erasureID)
	}
	return nil
}

// UserErased reports whether any erasure record names userID. It reads the
// table on every call, so the answer survives restarts and a restored copy.
func (s *Store) UserErased(ctx context.Context, userID string) (bool, error) {
	if strings.TrimSpace(userID) == "" {
		return false, nil
	}
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM erasure_applied WHERE user_id = ? LIMIT 1`, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check erased user: %w", err)
	}
	return true, nil
}

// CountUserRows counts requests still carrying userID as requested_by. After
// EraseUser it must be 0.
func (s *Store) CountUserRows(ctx context.Context, userID string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests WHERE requested_by = ?`, userID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count user rows: %w", err)
	}
	return n, nil
}
