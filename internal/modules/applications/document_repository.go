package applications

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"

	"student_dormitory/internal/platform/backup"
)

func (r *Repository) DocumentKeys(ctx context.Context) (map[string]bool, error) {
	refs, err := r.backupReferences(ctx)
	if err != nil {
		return nil, err
	}
	keys := map[string]bool{}
	for _, ref := range refs {
		keys[ref.Path[len("documents/"):]] = true
	}
	return keys, nil
}

// BackupReferences includes every immutable document version, including old and
// withdrawn applications. The composition root combines module-owned contracts.
func BackupReferences(ctx context.Context, db *sql.DB) ([]backup.Reference, error) {
	var present int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='application_document_versions'").Scan(&present); err != nil {
		return nil, err
	}
	if present == 0 {
		return nil, nil
	}
	return NewRepository(db).backupReferences(ctx)
}
func (r *Repository) backupReferences(ctx context.Context) ([]backup.Reference, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT storage_key,byte_size FROM application_document_versions ORDER BY storage_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refs []backup.Reference
	for rows.Next() {
		var key string
		var size int64
		if err := rows.Scan(&key, &size); err != nil {
			return nil, err
		}
		if !regexp.MustCompile(`^[a-zA-Z0-9_-]{32,64}\.(jpg|png|pdf)$`).MatchString(key) || size < 1 || size > MaxDocumentBytes {
			return nil, fmt.Errorf("invalid document reference")
		}
		refs = append(refs, backup.Reference{Path: "documents/" + key, Size: size})
	}
	return refs, rows.Err()
}
