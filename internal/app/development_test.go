package app

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"student_dormitory/internal/modules/auth"
	"student_dormitory/internal/platform/config"
	"student_dormitory/internal/platform/database"
)

func readDevelopmentCredentials(t *testing.T, path string) map[string]auth.DevelopmentCredential {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`Роль: ([^\n]+)\nЛогин: ([^\n]+)\nПароль: ([^\n]+)\nID: ([^\n]+)`)
	credentials := make(map[string]auth.DevelopmentCredential)
	for _, match := range pattern.FindAllStringSubmatch(string(body), -1) {
		credentials[match[1]] = auth.DevelopmentCredential{Role: match[1], Login: match[2], Password: match[3], UserID: match[4]}
	}
	if len(credentials) != 5 {
		t.Fatal("expected credentials for all five roles")
	}
	return credentials
}

func TestStartupDevelopmentUsersAndRestart(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{DatabasePath: filepath.Join(dir, "app.db"), FilesDir: filepath.Join(dir, "files"),
		AppEnv: "development", DevLoginEnabled: true, DevUsersFile: filepath.Join(dir, "private", "dev-users.txt"), SessionTTL: time.Hour}
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	a, err := New(context.Background(), cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	credentials := readDevelopmentCredentials(t, cfg.DevUsersFile)
	info, err := os.Stat(cfg.DevUsersFile)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("credentials are not private")
	}
	info, err = os.Stat(filepath.Dir(cfg.DevUsersFile))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("credential directory is not private")
	}
	before, err := os.ReadFile(cfg.DevUsersFile)
	if err != nil {
		t.Fatal(err)
	}
	provider := auth.NewDevelopmentProvider(auth.NewRepository(a.db))
	passwords := map[string]bool{}
	for _, credential := range credentials {
		if len(credential.Password) < 32 || passwords[credential.Password] {
			t.Fatal("weak or shared password")
		}
		passwords[credential.Password] = true
		identity, err := provider.Attempt(credential.Login, credential.Password).Verify(context.Background())
		if err != nil || identity.Subject != credential.Login {
			t.Fatal("generated credentials do not work")
		}
		if strings.Contains(logs.String(), credential.Password) {
			t.Fatal("password in startup logs")
		}
	}
	// Restart must not restore a revoked grant or enable a disabled account.
	if _, err := a.db.ExecContext(context.Background(), "DELETE FROM auth_user_dormitories WHERE user_id = ?", credentials["registry_staff"].UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.ExecContext(context.Background(), "UPDATE auth_users SET enabled = 0 WHERE id = ?", credentials["student"].UserID); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(context.Background(), cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	a = restarted
	after, err := os.ReadFile(cfg.DevUsersFile)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("credentials changed on restart")
	}
	for query, want := range map[string]int{
		"SELECT count(*) FROM auth_users":                                           5,
		"SELECT count(*) FROM audit_events WHERE action = 'auth.dev_users.created'": 1,
		"SELECT count(*) FROM auth_users WHERE enabled = 0":                         1,
		"SELECT count(*) FROM auth_user_dormitories":                                2,
	} {
		var got int
		if err := a.db.QueryRowContext(context.Background(), query).Scan(&got); err != nil || got != want {
			t.Fatalf("restart state: got %d, want %d, error %v", got, want, err)
		}
	}
	// The stored user IDs must still match the original credential file.
	for _, credential := range credentials {
		var id string
		if err := a.db.QueryRowContext(context.Background(), "SELECT user_id FROM auth_external_identities WHERE provider = 'development' AND subject = ?", credential.Login).Scan(&id); err != nil || id != credential.UserID {
			t.Fatal("local user ID changed on restart")
		}
	}
}

func TestStartupDevelopmentFailureRollsBack(t *testing.T) {
	for _, failure := range []string{"existing_file", "unwritable_path", "audit", "partial_batch"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			cfg := config.Config{DatabasePath: filepath.Join(dir, "app.db"), FilesDir: filepath.Join(dir, "files"),
				AppEnv: "development", DevLoginEnabled: true, DevUsersFile: filepath.Join(dir, "dev-users.txt"), SessionTTL: time.Hour}
			ctx := context.Background()
			db, err := database.Open(ctx, cfg.DatabasePath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			wantUsers := 0
			switch failure {
			case "existing_file", "unwritable_path":
				if err := os.WriteFile(cfg.DevUsersFile, []byte("keep-existing-secret"), 0o600); err != nil {
					t.Fatal(err)
				}
				if failure == "unwritable_path" {
					cfg.DevUsersFile = filepath.Join(cfg.DevUsersFile, "child.txt")
				}
			case "audit":
				if _, err := db.ExecContext(ctx, `CREATE TRIGGER reject_seed BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'sensitive-error-marker'); END`); err != nil {
					t.Fatal(err)
				}
			case "partial_batch":
				if _, err := db.ExecContext(ctx, "INSERT INTO auth_users (id, display_name) VALUES (?, ?)", "existing", "student"); err != nil {
					t.Fatal(err)
				}
				if _, err := db.ExecContext(ctx, "INSERT INTO auth_external_identities (provider, subject, user_id) VALUES ('development', ?, ?)", "dev_student", "existing"); err != nil {
					t.Fatal(err)
				}
				wantUsers = 1
			}
			_, err = New(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err == nil || strings.Contains(err.Error(), "sensitive-error-marker") || strings.Contains(err.Error(), "keep-existing-secret") {
				t.Fatal("startup should fail without exposing sensitive errors")
			}
			for table, want := range map[string]int{"auth_users": wantUsers, "auth_dev_credentials": 0, "auth_user_roles": 0, "auth_user_dormitories": 0, "audit_events": 0} {
				var got int
				if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&got); err != nil || got != want {
					t.Fatalf("%s not rolled back: %d %v", table, got, err)
				}
			}
			if failure == "existing_file" {
				body, err := os.ReadFile(cfg.DevUsersFile)
				if err != nil || string(body) != "keep-existing-secret" {
					t.Fatal("existing credentials overwritten or removed")
				}
			} else if failure != "unwritable_path" {
				if _, err := os.Stat(cfg.DevUsersFile); !os.IsNotExist(err) {
					t.Fatal("credential file left after rollback")
				}
			}
		})
	}
}

func TestStartupDoesNotSeedWithoutDevelopmentLogin(t *testing.T) {
	for _, env := range []string{"development", "production"} {
		t.Run(env, func(t *testing.T) {
			dir := t.TempDir()
			cfg := config.Config{DatabasePath: filepath.Join(dir, "app.db"), FilesDir: filepath.Join(dir, "files"),
				AppEnv: env, DevUsersFile: filepath.Join(dir, "dev-users.txt"), SessionTTL: time.Hour}
			a, err := New(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			var count int
			if err := a.db.QueryRowContext(context.Background(), "SELECT count(*) FROM auth_users").Scan(&count); err != nil || count != 0 {
				t.Fatal("test users created with development login disabled")
			}
			if _, err := os.Stat(cfg.DevUsersFile); !os.IsNotExist(err) {
				t.Fatal("unexpected credential file")
			}
		})
	}
}
