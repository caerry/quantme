package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"quantme/internal/event"
	"quantme/internal/storage"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type SQLiteStore struct {
	db     *sql.DB
	dbPath string
}

func NewSQLiteStore(dbPath string) storage.Storage {
	return &SQLiteStore{dbPath: dbPath}
}

const createEventsTableSQL = `
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
`

func (s *SQLiteStore) Init(ctx context.Context) error {
	// Ensure directory exists
	dir := filepath.Dir(s.dbPath)
	// Use 0750 for directory permissions
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("failed to create db directory %s: %w", dir, err)
	}

	log.Printf("Initializing SQLite database at: %s", s.dbPath)
	// The blank import above ensures that when this line is called,
	// the "sqlite3" driver has already registered itself with database/sql.
	db, err := sql.Open("sqlite3", s.dbPath+"?_journal=WAL&_timeout=5000&_fk=true")
	if err != nil {
		// The error "unknown driver" happens here if the import is missing.
		return fmt.Errorf("failed to open sqlite database: %w", err)
	}
	s.db = db

	// Set connection pool settings (optional but recommended)
	s.db.SetMaxOpenConns(1) // SQLite is often best with a single writer connection
	s.db.SetMaxIdleConns(1)
	s.db.SetConnMaxLifetime(time.Minute * 5)

	if err := s.db.PingContext(ctx); err != nil {
		s.db.Close() // Close the connection if ping fails
		return fmt.Errorf("failed to ping database: %w", err)
	}

	if _, err := s.db.ExecContext(ctx, createEventsTableSQL); err != nil {
		s.db.Close() // Close the connection if table creation fails
		return fmt.Errorf("failed to create events table: %w", err)
	}
	log.Println("Database initialized successfully.")
	return nil
}

func (s *SQLiteStore) SaveEvent(ctx context.Context, e event.Event) (int64, error) {
	query := `INSERT INTO events (timestamp, type, app_name, window_title, value, tag, notes)
	          VALUES (?, ?, ?, ?, ?, ?, ?)`
	res, err := s.db.ExecContext(ctx, query, e.Timestamp, e.Type, e.AppName, e.WindowTitle, e.Value, e.Tag, e.Notes)
	if err != nil {
		return 0, fmt.Errorf("failed to insert event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get last insert ID: %w", err)
	}
	// log.Printf("Saved event: Type=%s, App=%s, Title=%s", e.Type, e.AppName, e.WindowTitle)
	return id, nil
}

func (s *SQLiteStore) GetEvents(ctx context.Context, start, end time.Time, eventTypes ...event.EventType) ([]event.Event, error) {
	query := `SELECT id, timestamp, type, app_name, window_title, value, tag, notes
	          FROM events
	          WHERE timestamp >= ? AND timestamp <= ?`
	args := []interface{}{start, end}

	if len(eventTypes) > 0 {
		placeholders := strings.Repeat("?,", len(eventTypes)-1) + "?"
		query += fmt.Sprintf(" AND type IN (%s)", placeholders)
		for _, et := range eventTypes {
			args = append(args, et)
		}
	}

	query += " ORDER BY timestamp ASC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query events: %w", err)
	}
	defer rows.Close()

	var events []event.Event
	for rows.Next() {
		var e event.Event
		var appName sql.NullString
		var windowTitle sql.NullString
		var value sql.NullFloat64
		var tag sql.NullString
		var notes sql.NullString

		if err := rows.Scan(&e.ID, &e.Timestamp, &e.Type, &appName, &windowTitle, &value, &tag); err != nil {
			return nil, fmt.Errorf("failed to scan event row: %w", err)
		}
		e.AppName = appName.String
		e.WindowTitle = windowTitle.String
		e.Value = value.Float64
		e.Tag = tag.String
		e.Notes = notes.String
		events = append(events, e)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating event rows: %w", err)
	}

	return events, nil
}

func (s *SQLiteStore) Close() error {
	if s.db != nil {
		log.Println("Closing database connection.")
		return s.db.Close()
	}
	return nil
}
