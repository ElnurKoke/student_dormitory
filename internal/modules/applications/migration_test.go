package applications

import (
	"context"
	"database/sql"
	"encoding/json"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"

	"student_dormitory/internal/platform/database"
	"student_dormitory/migrations"
)

func TestUpgradeExistingSubmittedApplication(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "old.db")+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	old := fstest.MapFS{}
	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name >= "000009" {
			continue
		}
		body, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		old[name] = &fstest.MapFile{Data: body}
	}
	if err := database.Migrate(ctx, db, old); err != nil {
		t.Fatal(err)
	}
	f := fields(valid(), Fields{}, "2026-11-15T00:00:00Z")
	body, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO auth_users(id,display_name) VALUES ('legacy','Legacy')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO applications(id,user_id,status,fields_json,version,submit_key,submitted_version,submitted_at,consent_version,consent_accepted_at,created_at) VALUES ('old','legacy','submitted',?,3,'legacy-key',2,'2026-11-15T01:00:00Z','test-v1','2026-11-15T01:00:00Z','2026-11-15T00:00:00Z')`, string(body)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO application_registrations(application_id) VALUES ('old')`); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal("second migration", err)
	}
	r := NewRepository(db)
	a, err := r.Get(ctx, "legacy", "old")
	if err != nil || a.Registration != 1 || a.Fields != f || a.ReviewerID != "" || a.Version != 3 || a.SubmittedAt != "2026-11-15T01:00:00Z" {
		t.Fatal("existing application changed", a, err)
	}
	key, consent, err := r.Submission(ctx, "old", 2)
	if err != nil || key != "legacy-key" || consent != ConsentVersion {
		t.Fatal("old replay metadata lost", err)
	}
	notifications, err := r.Notifications(ctx, "legacy")
	if err != nil || len(notifications) != 1 || notifications[0].Kind != "registered" {
		t.Fatal("upgrade notifications", notifications, err)
	}
}
