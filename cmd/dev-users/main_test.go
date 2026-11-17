package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"student_dormitory/internal/modules/auth"
)

func TestDevelopmentUserCommand(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DEV_LOGIN_ENABLED", "true")
	t.Setenv("DATABASE_PATH", filepath.Join(dir, "users.db"))
	t.Setenv("FILES_DIR", filepath.Join(dir, "files"))
	output := filepath.Join(dir, "private", "credentials.json")
	if err := run(output, []string{"dev-dorm-1"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(output)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("credential file is not private")
	}
	body, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var credentials []auth.DevelopmentCredential
	if err := json.Unmarshal(body, &credentials); err != nil || len(credentials) != 5 {
		t.Fatal("invalid credential output")
	}
	passwords := map[string]bool{}
	for _, credential := range credentials {
		if len(credential.Password) < 32 || passwords[credential.Password] {
			t.Fatal("shared or weak generated password")
		}
		passwords[credential.Password] = true
	}
	if err := run(output, []string{"dev-dorm-1"}); err == nil {
		t.Fatal("existing file overwritten")
	}
	if err := run(filepath.Join(dir, "duplicate.json"), []string{"dev-dorm-1"}); err == nil {
		t.Fatal("existing users silently changed")
	}
	if _, err := os.Stat(filepath.Join(dir, "duplicate.json")); !os.IsNotExist(err) {
		t.Fatal("failed output not removed")
	}
	t.Setenv("APP_ENV", "production")
	if err := run(filepath.Join(dir, "production.json"), []string{"dev-dorm-1"}); err == nil {
		t.Fatal("production provisioning accepted")
	}
}
