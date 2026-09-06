package reqstore

import (
	"fmt"
	"strings"
	"time"
)

func (s *Store) ensureReadyTable() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS request_ready_notified (
			request_id TEXT PRIMARY KEY,
			notified_at TEXT NOT NULL
		)`)
	if err != nil {
		return fmt.Errorf("migrate ready table: %w", err)
	}
	return nil
}

// ClaimReadyNotified records that media.request.ready was published for id.
// Returns true on the first claim, false if already notified.
func (s *Store) ClaimReadyNotified(id string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, nil
	}
	res, err := s.db.Exec(`
		INSERT INTO request_ready_notified(request_id, notified_at)
		VALUES (?, ?)
		ON CONFLICT(request_id) DO NOTHING`,
		id, time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return false, fmt.Errorf("claim ready: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
