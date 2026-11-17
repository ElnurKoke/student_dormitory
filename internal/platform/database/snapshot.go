package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"student_dormitory/migrations"
)

// OpenReadOnly never creates a DB or applies migrations. SQLite reads committed
// WAL frames when present; immutable=1 would incorrectly ignore an active WAL.
func OpenReadOnly(ctx context.Context, path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("database must be a regular file")
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	q := url.Values{"mode": {"ro"}, "_foreign_keys": {"on"}, "_busy_timeout": {"5000"}, "_synchronous": {"FULL"}}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Snapshot uses SQLite rather than copying the main file. VACUUM INTO includes
// committed WAL content in a standalone destination; caller holds maintenance locks.
func Snapshot(ctx context.Context, db *sql.DB, destination string) error {
	file, err := os.OpenFile(destination, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if _, err = db.ExecContext(ctx, `VACUUM INTO ?`, destination); err != nil {
		return err
	}
	file, err = os.OpenFile(destination, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}
func CheckIntegrity(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return err
	}
	count := 0
	valid := true
	for rows.Next() {
		var result string
		if err = rows.Scan(&result); err != nil {
			rows.Close()
			return err
		}
		count++
		valid = valid && result == "ok"
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if count != 1 || !valid {
		return fmt.Errorf("SQLite integrity check failed")
	}
	rows, err = db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	hasRow := rows.Next()
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if hasRow {
		return fmt.Errorf("SQLite foreign key check failed")
	}
	return nil
}

// CheckCurrentSchema verifies a backup against the binary's embedded migration
// ledger without applying anything to the source or the restored copy.
func CheckCurrentSchema(ctx context.Context, db *sql.DB) error {
	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, `SELECT version,checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return err
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		var name, checksum string
		if err := rows.Scan(&name, &checksum); err != nil {
			return err
		}
		if i >= len(names) || name != names[i] {
			return fmt.Errorf("incompatible migration history")
		}
		body, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			return err
		}
		if checksum != fmt.Sprintf("%x", sha256.Sum256(body)) {
			return fmt.Errorf("migration checksum mismatch")
		}
		i++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if i != len(names) {
		return fmt.Errorf("schema must match the current application version")
	}
	return nil
}

// FreezeWriters takes SQLite's own BEGIN IMMEDIATE lock without changing rows.
// The snapshot is read through a different read-only connection, so VACUUM INTO
// is not run inside this transaction. It also excludes older app writers that
// do not yet participate in the process-level maintenance locks.
func FreezeWriters(ctx context.Context, path string) (func() error, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	q := url.Values{"mode": {"rw"}, "_foreign_keys": {"on"}, "_busy_timeout": {"5000"}, "_txlock": {"immediate"}, "_synchronous": {"FULL"}}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		db.Close()
		return nil, err
	}
	return func() error {
		err := tx.Rollback()
		if errors.Is(err, sql.ErrTxDone) {
			err = nil
		}
		return errors.Join(err, db.Close())
	}, nil
}
