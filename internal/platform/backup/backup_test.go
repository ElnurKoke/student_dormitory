package backup_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"student_dormitory/internal/modules/inventory"
	"student_dormitory/internal/platform/backup"
	"student_dormitory/internal/platform/database"
	"student_dormitory/internal/platform/files"
	"student_dormitory/internal/platform/maintenance"
)

type source struct {
	db                    *sql.DB
	dbPath, filesDir, key string
	pixels                []byte
}

func sourceFixture(t *testing.T) *source {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	s := &source{dbPath: filepath.Join(dir, "source.db"), filesDir: filepath.Join(dir, "files"), key: strings.Repeat("A", 40) + ".png"}
	var err error
	s.db, err = database.Open(ctx, s.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.db.Close() })
	if _, err = s.db.ExecContext(ctx, `PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	store, err := files.NewLocal(filepath.Join(s.filesDir, "photos"))
	if err != nil {
		t.Fatal(err)
	}
	var pixels bytes.Buffer
	if err = png.Encode(&pixels, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	s.pixels = pixels.Bytes()
	for _, key := range []string{s.key, strings.Repeat("B", 40) + ".png"} {
		if err = store.Put(ctx, key, s.pixels); err != nil {
			t.Fatal(err)
		}
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO inventory_dormitories(id,name,address,operator,use_basis,contacts,published) VALUES('wal-only','Қазақша, русский','Адрес','Operator','Basis','Contacts',1)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO inventory_photos(id,dormitory_id,storage_key,media_type,byte_size,width,height,position) VALUES('photo','wal-only',?,'image/png',?,3,2,0)`, s.key, len(s.pixels))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestBackupIncludesCommittedWALAndOnlyReferencedPhotos(t *testing.T) {
	ctx := context.Background()
	s := sourceFixture(t)
	info, err := os.Stat(s.dbPath + "-wal")
	if err != nil || info.Size() <= 32 {
		t.Fatal("test needs uncheckpointed WAL")
	}
	// A read that intentionally ignores WAL sees an older main database. This is
	// evidence that copying just source.db would lose the committed fixture rows.
	raw, err := sql.Open("sqlite3", "file:"+s.dbPath+"?mode=ro&immutable=1")
	if err != nil {
		t.Fatal(err)
	}
	var stale int
	err = raw.QueryRowContext(ctx, `SELECT count(*) FROM inventory_dormitories`).Scan(&stale)
	raw.Close()
	if err != nil || stale != 0 {
		t.Fatal("fixture data was checkpointed", err)
	}
	output := filepath.Join(t.TempDir(), "bundle")
	manifest, err := backup.Create(ctx, s.dbPath, s.filesDir, output, inventory.BackupReferences)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Assets) != 1 || manifest.Assets[0].Path != "photos/"+s.key {
		t.Fatal("orphan file was copied")
	}
	if _, err = backup.Verify(ctx, output, inventory.BackupReferences); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if _, err = backup.Restore(ctx, output, restored, inventory.BackupReferences); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(ctx, filepath.Join(restored, backup.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name string
	if err = db.QueryRowContext(ctx, `SELECT name FROM inventory_dormitories WHERE id='wal-only'`).Scan(&name); err != nil || name != "Қазақша, русский" {
		t.Fatal("WAL data was lost", err)
	}
	pixels, err := os.ReadFile(filepath.Join(restored, "files", "photos", s.key))
	if err != nil || !bytes.Equal(pixels, s.pixels) {
		t.Fatal("photo bytes changed")
	}
	if err = database.CheckIntegrity(ctx, db); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{output, filepath.Join(output, "files"), restored, filepath.Join(restored, "files", "photos")} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("directory is not private: %s", path)
		}
	}
	for _, path := range []string{filepath.Join(output, backup.DatabaseName), filepath.Join(output, backup.ManifestName), filepath.Join(restored, backup.DatabaseName), filepath.Join(restored, "files", "photos", s.key)} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("file is not private", path)
		}
	}
	if _, err = backup.Restore(ctx, output, restored, inventory.BackupReferences); err == nil {
		t.Fatal("existing restored directory replaced")
	}
	if _, err = backup.Create(ctx, s.dbPath, s.filesDir, output, inventory.BackupReferences); err == nil {
		t.Fatal("existing backup replaced")
	}
	if _, err = backup.Verify(ctx, output, inventory.BackupReferences); err != nil {
		t.Fatal("refused overwrite damaged existing backup")
	}
}
func TestBackupRejectsCorruptionTraversalAndIncompleteBundles(t *testing.T) {
	for _, failure := range []string{"photo_bytes", "database_bytes", "missing_photo", "photo_symlink", "directory_symlink", "path_traversal", "database_sidecar", "incomplete", "manifest_extra"} {
		t.Run(failure, func(t *testing.T) {
			s := sourceFixture(t)
			ctx := context.Background()
			output := filepath.Join(t.TempDir(), "bundle")
			manifest, err := backup.Create(ctx, s.dbPath, s.filesDir, output, inventory.BackupReferences)
			if err != nil {
				t.Fatal(err)
			}
			photo := filepath.Join(output, "files", "photos", s.key)
			switch failure {
			case "photo_bytes", "database_bytes":
				path := photo
				if failure == "database_bytes" {
					path = filepath.Join(output, backup.DatabaseName)
				}
				file, err := os.OpenFile(path, os.O_RDWR, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = file.WriteAt([]byte{0xff}, 10); err != nil {
					t.Fatal(err)
				}
				file.Close()
			case "missing_photo":
				if err = os.Remove(photo); err != nil {
					t.Fatal(err)
				}
			case "photo_symlink":
				if err = os.Remove(photo); err != nil {
					t.Fatal(err)
				}
				if err = os.Symlink(filepath.Join(s.filesDir, "photos", s.key), photo); err != nil {
					t.Fatal(err)
				}
			case "directory_symlink":
				if err = os.Rename(filepath.Join(output, "files"), filepath.Join(output, "saved-files")); err != nil {
					t.Fatal(err)
				}
				if err = os.Symlink(filepath.Join(output, "saved-files"), filepath.Join(output, "files")); err != nil {
					t.Fatal(err)
				}
			case "path_traversal":
				manifest.Assets[0].Path = "../escape.png"
				body, _ := json.Marshal(manifest)
				if err = os.WriteFile(filepath.Join(output, backup.ManifestName), body, 0600); err != nil {
					t.Fatal(err)
				}
			case "database_sidecar":
				if err = os.WriteFile(filepath.Join(output, backup.DatabaseName+"-wal"), []byte("unverified frames"), 0600); err != nil {
					t.Fatal(err)
				}
			case "incomplete":
				if err = os.WriteFile(filepath.Join(output, maintenance.IncompleteMarker), []byte("incomplete"), 0600); err != nil {
					t.Fatal(err)
				}
			case "manifest_extra":
				f, err := os.OpenFile(filepath.Join(output, backup.ManifestName), os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				f.WriteString("{} ")
				f.Close()
			}
			if _, err = backup.Verify(ctx, output, inventory.BackupReferences); err == nil {
				t.Fatal("unsafe backup accepted")
			}
			destination := filepath.Join(t.TempDir(), "must-not-be-created")
			if _, err = backup.Restore(ctx, output, destination, inventory.BackupReferences); err == nil {
				t.Fatal("unsafe backup restored")
			}
			if _, err = os.Stat(destination); !os.IsNotExist(err) {
				t.Fatal("invalid backup created restore destination")
			}
		})
	}
}
func TestMaintenanceAndFailedBackupAreFailClosed(t *testing.T) {
	s := sourceFixture(t)
	ctx := context.Background()
	guard, err := maintenance.Acquire(s.dbPath, s.filesDir)
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "locked")
	if _, err = backup.Create(ctx, s.dbPath, s.filesDir, output, inventory.BackupReferences); !errors.Is(err, maintenance.ErrBusy) {
		t.Fatal("backup can race server", err)
	}
	guard.Close()
	if _, err = backup.Create(ctx, s.dbPath, s.filesDir, filepath.Join(s.filesDir, "bundle"), inventory.BackupReferences); err == nil {
		t.Fatal("backup inside working storage accepted")
	}
	if err = os.Remove(filepath.Join(s.filesDir, "photos", s.key)); err != nil {
		t.Fatal(err)
	}
	output = filepath.Join(t.TempDir(), "missing-file")
	if _, err = backup.Create(ctx, s.dbPath, s.filesDir, output, inventory.BackupReferences); err == nil {
		t.Fatal("missing referenced file accepted")
	}
	if _, err = backup.Verify(ctx, output, inventory.BackupReferences); !errors.Is(err, maintenance.ErrIncomplete) {
		t.Fatal("partial copy marked complete", err)
	}
	if _, err = maintenance.Acquire(filepath.Join(output, backup.DatabaseName), filepath.Join(output, "files")); !errors.Is(err, maintenance.ErrIncomplete) {
		t.Fatal("server can open incomplete backup")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = backup.Create(cancelled, s.dbPath, s.filesDir, filepath.Join(t.TempDir(), "cancelled"), inventory.BackupReferences); err == nil {
		t.Fatal("cancelled backup succeeded")
	}
}

func TestBackupFreezesWritersAndRestoreCannotModifySource(t *testing.T) {
	ctx := context.Background()
	s := sourceFixture(t)
	if _, err := s.db.ExecContext(ctx, `PRAGMA busy_timeout=30`); err != nil {
		t.Fatal(err)
	}
	blocked := false
	reader := func(ctx context.Context, snapshot *sql.DB) ([]backup.Reference, error) {
		if _, err := s.db.ExecContext(ctx, `UPDATE inventory_dormitories SET name='concurrent write' WHERE id='wal-only'`); err != nil {
			blocked = true
		} else {
			t.Error("write succeeded while snapshot/photos were being copied")
		}
		return inventory.BackupReferences(ctx, snapshot)
	}
	output := filepath.Join(t.TempDir(), "bundle")
	if _, err := backup.Create(ctx, s.dbPath, s.filesDir, output, reader); err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Fatal("snapshot did not freeze SQLite writers")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE inventory_dormitories SET name='after backup' WHERE id='wal-only'`); err != nil {
		t.Fatal("snapshot lock was not released", err)
	}
	if _, err := backup.Restore(ctx, output, filepath.Join(output, "restore"), inventory.BackupReferences); err == nil {
		t.Fatal("restore inside source accepted")
	}
	if _, err := backup.Verify(ctx, output, inventory.BackupReferences); err != nil {
		t.Fatal("refused restore damaged source")
	}
}
