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
	ID               string
	ItemType         string
	ItemID           string
	TMDBID           int32
	Title            string
	Year             int32
	Poster           string
	Status           string
	RequestedBy      string
	DenyReason       string
	SeasonNumber     int32
	EpisodeNumber    int32
	Overview         string
	GenresJSON       string
	QualityProfileID string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// DeletedUserID replaces requested_by after identity.user.deleted (NFR-DATA-003).
const DeletedUserID = "deleted-user"

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
	for _, stmt := range []string{
		`ALTER TABLE requests ADD COLUMN requested_by TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE requests ADD COLUMN deny_reason TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE requests ADD COLUMN season_number INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE requests ADD COLUMN episode_number INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE requests ADD COLUMN overview TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE requests ADD COLUMN genres_json TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE requests ADD COLUMN quality_profile_id TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := s.db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate column: %w", err)
		}
	}
	return s.ensureReadyTable()
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
		INSERT INTO requests (
			id, item_type, item_id, tmdb_id, title, year, poster, status,
			requested_by, deny_reason, season_number, episode_number, overview, genres_json,
			quality_profile_id, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			item_type=excluded.item_type,
			item_id=excluded.item_id,
			tmdb_id=excluded.tmdb_id,
			title=excluded.title,
			year=excluded.year,
			poster=excluded.poster,
			status=excluded.status,
			requested_by=excluded.requested_by,
			deny_reason=excluded.deny_reason,
			season_number=excluded.season_number,
			episode_number=excluded.episode_number,
			overview=excluded.overview,
			genres_json=excluded.genres_json,
			quality_profile_id=excluded.quality_profile_id,
			updated_at=excluded.updated_at
	`, r.ID, r.ItemType, r.ItemID, r.TMDBID, r.Title, r.Year, r.Poster, r.Status,
		r.RequestedBy, r.DenyReason, r.SeasonNumber, r.EpisodeNumber, r.Overview, r.GenresJSON,
		r.QualityProfileID,
		r.CreatedAt.UTC().Format(time.RFC3339Nano),
		r.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("put request: %w", err)
	}
	return nil
}

// Delete removes a request by ID.
func (s *Store) Delete(id string) error {
	res, err := s.db.Exec(`DELETE FROM requests WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete request: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("request not found")
	}
	return nil
}

// AnonymiseRequester rewrites requested_by for one account to DeletedUserID.
// Missing rows are not an error. Repeating the call is a no-op.
func (s *Store) AnonymiseRequester(userID string) (int64, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return 0, fmt.Errorf("user_id is required")
	}
	if userID == DeletedUserID {
		return 0, nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.Exec(
		`UPDATE requests SET requested_by = ?, updated_at = ? WHERE requested_by = ?`,
		DeletedUserID, now, userID,
	)
	if err != nil {
		return 0, fmt.Errorf("anonymise requester: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}

// Get returns a request by ID.
func (s *Store) Get(id string) (*Record, error) {
	row := s.db.QueryRow(`
		SELECT id, item_type, item_id, tmdb_id, title, year, poster, status,
			requested_by, deny_reason, season_number, episode_number, overview, genres_json,
			quality_profile_id, created_at, updated_at
		FROM requests WHERE id = ?
	`, id)
	return scanRecord(row)
}

// List returns all requests newest first.
func (s *Store) List() ([]*Record, error) {
	return s.ListFiltered("", "")
}

// ListFiltered returns requests filtered by status and/or requester, newest first.
func (s *Store) ListFiltered(status, requestedBy string) ([]*Record, error) {
	query := `
		SELECT id, item_type, item_id, tmdb_id, title, year, poster, status,
			requested_by, deny_reason, season_number, episode_number, overview, genres_json,
			quality_profile_id, created_at, updated_at
		FROM requests WHERE 1=1`
	args := []any{}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	if requestedBy != "" {
		query += ` AND requested_by = ?`
		args = append(args, requestedBy)
	}
	query += ` ORDER BY created_at DESC`

	rows, err := s.db.Query(query, args...)
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
	if err := row.Scan(
		&r.ID, &r.ItemType, &r.ItemID, &r.TMDBID, &r.Title, &r.Year, &r.Poster, &r.Status,
		&r.RequestedBy, &r.DenyReason, &r.SeasonNumber, &r.EpisodeNumber, &r.Overview, &r.GenresJSON,
		&r.QualityProfileID, &created, &updated,
	); err != nil {
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
