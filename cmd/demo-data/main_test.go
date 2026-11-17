package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"student_dormitory/internal/modules/inventory"
	"student_dormitory/internal/platform/database"
)

func TestDemoCommandIsExplicitIsolatedAndDoesNotOverwrite(t *testing.T) {
	ctx := context.Background()
	output := filepath.Join(t.TempDir(), "demo")
	t.Setenv("APP_ENV", "production")
	t.Setenv("DEV_LOGIN_ENABLED", "false")
	if err := run(ctx, output); err == nil {
		t.Fatal("production seed accepted")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("production invocation touched destination")
	}
	t.Setenv("APP_ENV", "development")
	t.Setenv("DEV_LOGIN_ENABLED", "true")
	// A conflicting ambient DB/file path must not redirect the isolated command.
	t.Setenv("DATABASE_PATH", filepath.Join(t.TempDir(), "untouched.db"))
	t.Setenv("FILES_DIR", filepath.Join(t.TempDir(), "untouched-files"))
	if err := run(ctx, output); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(output, "demo.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest inventory.DemoManifest
	if err = json.Unmarshal(body, &manifest); err != nil || len(manifest.Locations) != 13 || len(manifest.PhotoIDs) != 3 || manifest.ActivePlaces != 5 {
		t.Fatal("demo manifest incomplete", err, len(manifest.Locations))
	}
	db, err := database.OpenReadOnly(ctx, filepath.Join(output, "dormitory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = database.CheckIntegrity(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err = run(ctx, output); err == nil {
		t.Fatal("existing demo overwritten")
	}
	after, err := os.ReadFile(filepath.Join(output, "demo.json"))
	if err != nil || string(after) != string(body) {
		t.Fatal("existing manifest changed")
	}
	if _, err = os.Stat(os.Getenv("DATABASE_PATH")); !os.IsNotExist(err) {
		t.Fatal("ambient database was touched")
	}
}
