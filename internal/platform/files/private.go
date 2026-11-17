package files

import (
	"errors"
	"os"
	"path/filepath"
)

// CreatePrivate creates a new private file without following an existing
// symlink or replacing a file. The caller rolls it back if its DB transaction fails.
func CreatePrivate(path string, body []byte) (rollback func() error, err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	rollback = func() error { return os.Remove(path) }
	closed := false
	defer func() {
		if err != nil {
			if !closed {
				err = errors.Join(err, file.Close())
			}
			err = errors.Join(err, rollback())
		}
	}()
	if _, err = file.Write(body); err != nil {
		return rollback, err
	}
	if err = file.Sync(); err != nil {
		return rollback, err
	}
	err = file.Close()
	closed = true
	return rollback, err
}
