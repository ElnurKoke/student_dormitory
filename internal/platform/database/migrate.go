package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
)

var migrationName = regexp.MustCompile(`^[0-9]{6}_[a-z0-9_]+\.sql$`)

// Migrate applies a forward-only sequence atomically and rejects edited history.
// The first migration bootstraps the journal and must remain idempotent.
func Migrate(ctx context.Context, db *sql.DB, source fs.FS) error {
	names, err := fs.Glob(source, "*.sql")
	if err != nil {
		return err
	}
	if len(names) == 0 || names[0] != "000001_schema_migrations.sql" {
		return fmt.Errorf("missing bootstrap migration")
	}
	for i, name := range names {
		if !migrationName.MatchString(name) || name[:6] != fmt.Sprintf("%06d", i+1) {
			return fmt.Errorf("invalid migration sequence at %s", name)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var journalExists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations')").Scan(&journalExists); err != nil {
		return err
	}
	if !journalExists {
		bootstrap, err := fs.ReadFile(source, names[0])
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(bootstrap)); err != nil {
			return fmt.Errorf("bootstrap journal: %w", err)
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT version, checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return err
	}
	applied := make(map[string]string)
	for rows.Next() {
		var version, checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			rows.Close()
			return err
		}
		if len(applied) >= len(names) || names[len(applied)] != version {
			rows.Close()
			return fmt.Errorf("database migration history is incompatible at %s", version)
		}
		applied[version] = checksum
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	for _, name := range names {
		body, err := fs.ReadFile(source, name)
		if err != nil {
			return err
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256(body))
		if previous, ok := applied[name]; ok {
			if previous != checksum {
				return fmt.Errorf("applied migration %s has changed", name)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version, checksum) VALUES (?, ?)", name, checksum); err != nil {
			return err
		}
	}
	return tx.Commit()
}
