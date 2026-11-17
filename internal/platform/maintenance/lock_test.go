//go:build darwin || linux

package maintenance

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLockAcrossProcessesAndBothResources(t *testing.T) {
	if os.Getenv("DORM_LOCK_HELPER") == "1" {
		lock, err := Acquire(os.Getenv("DORM_LOCK_DB"), os.Getenv("DORM_LOCK_FILES"))
		if err == nil {
			lock.Close()
			t.Fatal("subprocess acquired parent lock")
		}
		if !errors.Is(err, ErrBusy) {
			t.Fatal(err)
		}
		return
	}
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "app.db")
	filesDir := filepath.Join(dir, "files")
	lock, err := Acquire(dbPath, filesDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	for _, entry := range []struct{ db, files string }{{dbPath, filepath.Join(dir, "other-files")}, {filepath.Join(dir, "other.db"), filesDir}} {
		command := exec.Command(os.Args[0], "-test.run=^TestLockAcrossProcessesAndBothResources$")
		command.Env = append(os.Environ(), "DORM_LOCK_HELPER=1", "DORM_LOCK_DB="+entry.db, "DORM_LOCK_FILES="+entry.files)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("cross-process lock failed: %v %s", err, output)
		}
	}
	if err = lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err = lock.Close(); err != nil {
		t.Fatal("close must be idempotent")
	}
	next, err := Acquire(dbPath, filesDir)
	if err != nil {
		t.Fatal("released lock remained busy", err)
	}
	next.Close()
	if err = os.WriteFile(filepath.Join(dir, IncompleteMarker), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Acquire(dbPath, filesDir); !errors.Is(err, ErrIncomplete) {
		t.Fatal("incomplete data accepted")
	}
	restoring, err := AcquireForRestore(dbPath, filesDir)
	if err != nil {
		t.Fatal(err)
	}
	restoring.Close()
}
