package reqstore

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Record is a persisted media request.
type Record struct {
	ID        string
	ItemType  string
	ItemID    string
	TMDBID    int32
	Title     string
	Year      int32
	Poster    string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Store persists media requests in SQLite.
type Store struct {
	db *sql.DB
}

// Open opens or creates the SQLite database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS requests (
			id         TEXT PRIMARY KEY,
			item_type  TEXT NOT NULL,
			item_id    TEXT NOT NULL DEFAULT '',
			tmdb_id    INTEGER NOT NULL DEFAULT 0,
			title      TEXT NOT NULL DEFAULT '',
			year       INTEGER NOT NULL DEFAULT 0,
			poster     TEXT NOT NULL DEFAULT '',
			status     TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)
	`)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := s.pruneDuplicates(); err != nil {
		return fmt.Errorf("prune duplicate requests: %w", err)
	}
	return nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// Put inserts or replaces a request record.
func (s *Store) Put(r *Record) error {
	if r == nil || r.ID == "" {
		return fmt.Errorf("record id is required")
	}
	now := time.Now().UTC()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	r.UpdatedAt = now
	_, err := s.db.Exec(`
		INSERT INTO requests (id, item_type, item_id, tmdb_id, title, year, poster, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			item_type=excluded.item_type,
			item_id=excluded.item_id,
			tmdb_id=excluded.tmdb_id,
			title=excluded.title,
			year=excluded.year,
			poster=excluded.poster,
			status=excluded.status,
			updated_at=excluded.updated_at
	`, r.ID, r.ItemType, r.ItemID, r.TMDBID, r.Title, r.Year, r.Poster, r.Status,
		r.CreatedAt.UTC().Format(time.RFC3339Nano),
		r.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("put request: %w", err)
	}
	return nil
}

// Get returns a request by ID.
func (s *Store) Get(id string) (*Record, error) {
	row := s.db.QueryRow(`
		SELECT id, item_type, item_id, tmdb_id, title, year, poster, status, created_at, updated_at
		FROM requests WHERE id = ?
	`, id)
	return scanRecord(row)
}

// List returns all requests newest first.
func (s *Store) List() ([]*Record, error) {
	rows, err := s.db.Query(`
		SELECT id, item_type, item_id, tmdb_id, title, year, poster, status, created_at, updated_at
		FROM requests ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list requests: %w", err)
	}
	defer rows.Close()
	var out []*Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Delete removes a request by ID.
func (s *Store) Delete(id string) error {
	if id == "" {
		return fmt.Errorf("id is required")
	}
	_, err := s.db.Exec(`DELETE FROM requests WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete request: %w", err)
	}
	return nil
}

// FindByTMDB returns the existing request for item type + TMDB id, if any.
func (s *Store) FindByTMDB(itemType string, tmdbID int32) (*Record, error) {
	if tmdbID <= 0 {
		return nil, nil
	}
	row := s.db.QueryRow(`
		SELECT id, item_type, item_id, tmdb_id, title, year, poster, status, created_at, updated_at
		FROM requests WHERE item_type = ? AND tmdb_id = ? ORDER BY created_at ASC LIMIT 1
	`, itemType, tmdbID)
	r, err := scanRecord(row)
	if err != nil {
		if strings.Contains(err.Error(), "request not found") {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

func requestDedupeKey(r *Record) string {
	if r == nil {
		return ""
	}
	kind := strings.ToLower(strings.TrimSpace(r.ItemType))
	if r.TMDBID > 0 {
		return fmt.Sprintf("%s:tmdb:%d", kind, r.TMDBID)
	}
	title := strings.ToLower(strings.TrimSpace(r.Title))
	return fmt.Sprintf("%s:title:%s:%d", kind, title, r.Year)
}

func betterRequest(a, b *Record) (keep, drop *Record) {
	if a == nil {
		return b, a
	}
	if b == nil {
		return a, b
	}
	aHas := strings.TrimSpace(a.ItemID) != ""
	bHas := strings.TrimSpace(b.ItemID) != ""
	if aHas != bHas {
		if aHas {
			return a, b
		}
		return b, a
	}
	if a.CreatedAt.Before(b.CreatedAt) || (a.CreatedAt.Equal(b.CreatedAt) && a.ID <= b.ID) {
		return a, b
	}
	return b, a
}

func (s *Store) pruneDuplicates() error {
	list, err := s.List()
	if err != nil {
		return err
	}
	keep := make(map[string]*Record, len(list))
	var drop []string
	for _, r := range list {
		k := requestDedupeKey(r)
		if k == "" {
			continue
		}
		prev, ok := keep[k]
		if !ok {
			keep[k] = r
			continue
		}
		winner, loser := betterRequest(prev, r)
		keep[k] = winner
		if loser != nil && loser.ID != "" {
			drop = append(drop, loser.ID)
		}
	}
	for _, id := range drop {
		if _, err := s.db.Exec(`DELETE FROM requests WHERE id = ?`, id); err != nil {
			return fmt.Errorf("delete duplicate %s: %w", id, err)
		}
	}
	return nil
}

// LoadAll returns a map of all requests keyed by ID.
func (s *Store) LoadAll() (map[string]*Record, error) {
	list, err := s.List()
	if err != nil {
		return nil, err
	}
	out := make(map[string]*Record, len(list))
	for _, r := range list {
		out[r.ID] = r
	}
	return out, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRecord(row rowScanner) (*Record, error) {
	var r Record
	var created, updated string
	if err := row.Scan(&r.ID, &r.ItemType, &r.ItemID, &r.TMDBID, &r.Title, &r.Year, &r.Poster, &r.Status, &created, &updated); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("request not found")
		}
		return nil, err
	}
	var err error
	r.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		r.CreatedAt, err = time.Parse(time.RFC3339, created)
		if err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
	}
	r.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		r.UpdatedAt, err = time.Parse(time.RFC3339, updated)
		if err != nil {
			return nil, fmt.Errorf("parse updated_at: %w", err)
		}
	}
	return &r, nil
}
