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
		);
