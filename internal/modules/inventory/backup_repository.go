package inventory

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"

	"student_dormitory/internal/platform/backup"
)

// BackupReferences is the owning module's explicit contract for filesystem
// assets, including archived/unpublished objects. It never exposes file bytes
// through the public model or an HTTP route.
func BackupReferences(ctx context.Context, db *sql.DB) ([]backup.Reference, error) {
	return NewRepository(db).backupReferences(ctx)
}
func (r *Repository) backupReferences(ctx context.Context) ([]backup.Reference, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT storage_key,byte_size FROM inventory_photos ORDER BY storage_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refs []backup.Reference
	keyPattern := regexp.MustCompile(`^[a-zA-Z0-9_-]{32,64}\.(jpg|png)$`)
	for rows.Next() {
		var key string
		var size int64
		if err = rows.Scan(&key, &size); err != nil {
			return nil, err
		}
		if !keyPattern.MatchString(key) || size < 1 || size > MaxPhotoBytes {
			return nil, fmt.Errorf("invalid photo reference")
		}
		refs = append(refs, backup.Reference{Path: "photos/" + key, Size: size})
	}
	return refs, rows.Err()
}
