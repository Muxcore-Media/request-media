CREATE TABLE requests (
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
		, requested_by TEXT NOT NULL DEFAULT '', deny_reason TEXT NOT NULL DEFAULT '', season_number INTEGER NOT NULL DEFAULT 0, episode_number INTEGER NOT NULL DEFAULT 0, overview TEXT NOT NULL DEFAULT '', genres_json TEXT NOT NULL DEFAULT '', quality_profile_id TEXT NOT NULL DEFAULT '');
CREATE TABLE request_ready_notified (
			request_id TEXT PRIMARY KEY,
			notified_at TEXT NOT NULL
		);
