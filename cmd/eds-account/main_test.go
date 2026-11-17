package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"student_dormitory/internal/modules/auth"
	"student_dormitory/internal/platform/database"
)

func edsCommandEnvironment(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "dormitory.db")
	for key, value := range map[string]string{"DATABASE_PATH": path, "FILES_DIR": filepath.Join(dir, "files"), "APP_ENV": "production", "DEV_LOGIN_ENABLED": "false", "NCANODE_URL": ""} {
		t.Setenv(key, value)
	}
	file := filepath.Join(dir, "iin.txt")
	if err := os.WriteFile(file, []byte("000000000001\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, file
}
func TestBootstrapAndLinkNCAIdentitiesWithoutPasswords(t *testing.T) {
	path, file := edsCommandEnvironment(t)
	if err := run(file, "", true); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	var admin string
	if err := db.QueryRowContext(context.Background(), `SELECT user_id FROM auth_external_identities WHERE provider='nca' AND subject='000000000001'`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM auth_user_roles WHERE user_id=? AND role_id='system_admin'`, admin).Scan(&count); err != nil || count != 1 {
		t.Fatal("first admin missing", count, err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO auth_users(id,display_name,enabled) VALUES ('staff','Existing name',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO auth_user_roles(user_id,role_id) VALUES ('staff','commandant')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO auth_user_dormitories(user_id,dormitory_id) VALUES ('staff','original-dorm')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := os.WriteFile(file, []byte("000000000002\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(file, "", true); !errors.Is(err, auth.ErrAdminExists) {
		t.Fatal("second bootstrap admin", err)
	}
	for range 2 {
		if err := run(file, "staff", false); err != nil {
			t.Fatal(err)
		}
	}
	db, err = database.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`SELECT count(*) FROM auth_users WHERE id='staff' AND display_name='Existing name' AND enabled=0`,
		`SELECT count(*) FROM auth_user_roles WHERE user_id='staff' AND role_id='commandant'`,
		`SELECT count(*) FROM auth_user_dormitories WHERE user_id='staff' AND dormitory_id='original-dorm'`,
		`SELECT count(*) FROM auth_external_identities WHERE provider='nca' AND user_id='staff' AND subject='000000000002'`,
		`SELECT count(*) FROM audit_events WHERE action='auth.nca_identity_linked' AND object_id='staff'`,
	} {
		if err := db.QueryRowContext(context.Background(), query).Scan(&count); err != nil || count != 1 {
			t.Fatal("existing state overwritten or duplicate event", query, count, err)
		}
	}
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM auth_dev_credentials`).Scan(&count); err != nil || count != 0 {
		t.Fatal("provisioning created passwords", err)
	}
	db.Close()
	if err := run(file, admin, false); !errors.Is(err, auth.ErrIdentityConflict) {
		t.Fatal("IIN reassigned", err)
	}
	if err := os.WriteFile(file, []byte("000000000003"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(file, "staff", false); !errors.Is(err, auth.ErrIdentityConflict) {
		t.Fatal("account linked to a second IIN", err)
	}
}
func TestNCACommandRollsBackAuditFailureAndRejectsPublicInputFile(t *testing.T) {
	path, file := edsCommandEnvironment(t)
	if err := os.Chmod(file, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(file, "", true); err == nil {
		t.Fatal("public IIN file accepted")
	}
	if err := os.Chmod(file, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `CREATE TRIGGER reject_bootstrap BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT,'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := run(file, "", true); err == nil {
		t.Fatal("audit failure ignored")
	}
	db, err = database.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM auth_users`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial bootstrap", count, err)
	}
}
