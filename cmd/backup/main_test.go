package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"student_dormitory/internal/platform/database"
)

func TestBackupCommandCreateVerifyRestoreAndArgumentFailures(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "source.db")
	storage := filepath.Join(dir, "files")
	db, err := database.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err = os.Mkdir(storage, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_PATH", dbPath)
	t.Setenv("FILES_DIR", storage)
	t.Setenv("APP_ENV", "production")
	t.Setenv("DEV_LOGIN_ENABLED", "false")
	bundle := filepath.Join(dir, "backup")
	restored := filepath.Join(dir, "restore")
	if err = run(ctx, []string{"create", "-out", bundle}); err != nil {
		t.Fatal(err)
	}
	if err = run(ctx, []string{"verify", "-from", bundle}); err != nil {
		t.Fatal(err)
	}
	if err = run(ctx, []string{"restore", "-from", bundle, "-out", restored}); err != nil {
		t.Fatal(err)
	}
	copy, err := database.OpenReadOnly(ctx, filepath.Join(restored, "dormitory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	if err = database.CheckCurrentSchema(ctx, copy); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"invalid"}, {"create"}, {"create", "-out", bundle}, {"verify"}, {"verify", "-from", bundle, "-out", restored}, {"restore", "-from", bundle}, {"restore", "-from", bundle, "-out", restored}} {
		if err = run(ctx, args); err == nil {
			t.Fatalf("invalid/overwriting command succeeded: %v", args)
		}
	}
}
