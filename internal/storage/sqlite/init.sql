CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp DATETIME NOT NULL,
    type TEXT NOT NULL,
    app_name TEXT,
    window_title TEXT,
    value REAL,
    tag TEXT,
    notes TEXT
);

CREATE INDEX IF NOT EXISTS idx_events_timestamp ON events (timestamp);

CREATE INDEX IF NOT EXISTS idx_events_type ON events (type);
