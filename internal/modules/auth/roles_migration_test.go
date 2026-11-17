package auth

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"

	"student_dormitory/internal/platform/database"
	"student_dormitory/migrations"
)

func TestRoleMigrationPreservesExistingAccounts(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "legacy.db")+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	previous := fstest.MapFS{}
	for _, name := range names {
		if name >= "000010" {
			continue
		}
		data, err := migrations.FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		previous[name] = &fstest.MapFile{Data: data}
	}
	if err := database.Migrate(ctx, db, previous); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO auth_users(id,display_name,enabled) VALUES ('legacy-user','Existing account',0)`,
		`INSERT INTO auth_user_roles(user_id,role_id) VALUES ('legacy-user','registry_staff'),('legacy-user','commandant')`,
		`INSERT INTO auth_user_dormitories(user_id,dormitory_id) VALUES ('legacy-user','original-dorm')`,
		`INSERT INTO auth_external_identities(provider,subject,user_id) VALUES ('development','legacy-login','legacy-user')`,
		`INSERT INTO auth_dev_credentials(provider,subject,password_hash) VALUES ('development','legacy-login','original-test-hash')`,
		`INSERT INTO auth_sessions(id,token_hash,user_id,identity_provider,csrf_token,expires_at,created_at) VALUES ('legacy-session','original-test-token','legacy-user','development','original-test-csrf',2000000000,1000000000)`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := database.Migrate(ctx, db, migrations.FS); err != nil {
			t.Fatal(err)
		}
	}
	repo := NewRepository(db)
	user, err := repo.managedUser(ctx, "legacy-user")
	if err != nil || user.ID != "legacy-user" || user.DisplayName != "Existing account" || len(user.Roles) != 2 || user.RoleVersion != 1 {
		t.Fatal("account reset during upgrade", user, err)
	}
	for _, query := range []string{
		`SELECT count(*) FROM auth_users WHERE id='legacy-user' AND enabled=0`,
		`SELECT count(*) FROM auth_user_dormitories WHERE user_id='legacy-user' AND dormitory_id='original-dorm'`,
		`SELECT count(*) FROM auth_external_identities WHERE user_id='legacy-user' AND subject='legacy-login' AND provider='development'`,
		`SELECT count(*) FROM auth_dev_credentials WHERE subject='legacy-login' AND password_hash='original-test-hash'`,
		`SELECT count(*) FROM auth_sessions WHERE user_id='legacy-user' AND token_hash='original-test-token' AND csrf_token='original-test-csrf'`,
		`SELECT count(*) FROM auth_role_permissions WHERE permission_id='auth.users.roles.write' AND role_id='system_admin'`,
	} {
		if roleCount(t, db, query) != 1 {
			t.Fatal("upgrade changed existing state", query)
		}
	}
	if roleCount(t, db, `SELECT count(*) FROM auth_role_permissions WHERE permission_id='auth.users.roles.write' AND role_id<>'system_admin'`) != 0 {
		t.Fatal("role management granted to business roles")
	}
	if roleCount(t, db, `SELECT count(*) FROM auth_role_notifications`) != 0 {
		t.Fatal("upgrade fabricated a role change")
	}
}
