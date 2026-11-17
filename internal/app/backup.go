package app

import (
	"context"
	"database/sql"

	"student_dormitory/internal/modules/applications"
	"student_dormitory/internal/modules/inventory"
	"student_dormitory/internal/platform/backup"
)

// BackupReferences composes module-owned file contracts without reading tables.
func BackupReferences(ctx context.Context, db *sql.DB) ([]backup.Reference, error) {
	photos, err := inventory.BackupReferences(ctx, db)
	if err != nil {
		return nil, err
	}
	documents, err := applications.BackupReferences(ctx, db)
	if err != nil {
		return nil, err
	}
	return append(photos, documents...), nil
}
