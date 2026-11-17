package database

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"student_dormitory/migrations"
)

// The initial deployment uses one SQLite connection and one application instance.
// DSN options below are applied by the driver to EVERY physical connection,
// including replacements created after a connection is discarded.
const (
	maxOpenConns = 1
	maxIdleConns = 1
	busyTimeout  = 5 * time.Second
)

func Open(ctx context.Context, path string) (*sql.DB, error) {
	if strings.TrimSpace(path) == "" || path == ":memory:" || strings.HasPrefix(path, "file:") {
		return nil, fmt.Errorf("database path must be a persistent filesystem path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("database path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, fmt.Errorf("database directory: %w", err)
	}
	file, err := os.OpenFile(abs, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("database file: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close database file: %w", err)
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	query := url.Values{
		"_foreign_keys": {"on"},
		"_busy_timeout": {fmt.Sprint(busyTimeout.Milliseconds())},
		"_journal_mode": {"WAL"},
		// Metadata deletion must survive power loss before its file is unlinked.
		"_synchronous": {"FULL"},
		"_txlock":      {"immediate"},
	}
	u.RawQuery = query.Encode()
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, fmt.Errorf("open SQLite: %w", err)
	}
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxIdleConns)
	db.SetConnMaxLifetime(0) // No lifetime limit; connections close with the DB.
	db.SetConnMaxIdleTime(0) // Keep the idle connection until shutdown.
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect SQLite: %w", err)
	}
	if err := Migrate(ctx, db, migrations.FS); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate SQLite: %w", err)
	}
	return db, nil
}
