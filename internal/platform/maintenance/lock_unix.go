//go:build darwin || linux

// Package maintenance coordinates the server and offline commands. Locks are
// advisory: external SQLite/file tools must also be stopped during maintenance.
package maintenance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

var ErrBusy = errors.New("data is in use; stop the server and other commands")
var ErrIncomplete = errors.New("incomplete maintenance operation; use a complete backup or a new directory")

const IncompleteMarker = ".maintenance-incomplete"

type Lock struct {
	files []*os.File
	once  sync.Once
	err   error
}

func canonicalDir(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(abs, 0700); err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}
func Acquire(databasePath, filesDir string) (*Lock, error) {
	return acquire(databasePath, filesDir, false)
}

// AcquireForRestore accepts an existing incomplete marker; only the restore tool uses it.
func AcquireForRestore(databasePath, filesDir string) (*Lock, error) {
	return acquire(databasePath, filesDir, true)
}
func acquire(databasePath, filesDir string, allowIncomplete bool) (*Lock, error) {
	if databasePath == "" || filesDir == "" || databasePath == ":memory:" || strings.HasPrefix(databasePath, "file:") {
		return nil, fmt.Errorf("invalid data paths")
	}
	parent, err := canonicalDir(filepath.Dir(databasePath))
	if err != nil {
		return nil, err
	}
	storage, err := canonicalDir(filesDir)
	if err != nil {
		return nil, err
	}
	// Do not allow a database symlink to bypass the lock on the real file.
	dbPath := filepath.Join(parent, filepath.Base(databasePath))
	if info, err := os.Lstat(dbPath); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("database must be a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	lock := &Lock{}
	ok := false
	defer func() {
		if !ok {
			lock.Close()
		}
	}()
	for _, path := range []string{dbPath + ".lock", filepath.Join(storage, ".maintenance.lock")} {
		fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
		if err != nil {
			return nil, err
		}
		file := os.NewFile(uintptr(fd), path)
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			file.Close()
			return nil, fmt.Errorf("invalid lock file")
		}
		if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			file.Close()
			return nil, ErrBusy
		}
		lock.files = append(lock.files, file)
	}
	if !allowIncomplete {
		if _, err := os.Lstat(filepath.Join(parent, IncompleteMarker)); err == nil {
			return nil, ErrIncomplete
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	ok = true
	return lock, nil
}
func (l *Lock) Close() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		for i := len(l.files) - 1; i >= 0; i-- {
			l.err = errors.Join(l.err, l.files[i].Close())
		}
	})
	return l.err
}
