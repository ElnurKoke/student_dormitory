package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

// Local owns a dedicated, private namespace; no caller-supplied path is used.
// Keep one namespace per database. It is never mounted as a static directory.
type Local struct {
	directory string
	documents bool
}

func NewLocal(directory string) (*Local, error) {
	if err := Prepare(directory); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	return &Local{directory: absolute}, nil
}

// NewDocumentLocal owns a separate private namespace, including PDF attachments.
func NewDocumentLocal(directory string) (*Local, error) {
	s, err := NewLocal(directory)
	if err != nil {
		return nil, err
	}
	s.documents = true
	return s, nil
}
func (s *Local) validKey(key string) bool {
	if s.documents {
		return regexp.MustCompile(`^[a-zA-Z0-9_-]{32,64}\.(jpg|png|pdf)$`).MatchString(key)
	}
	return validKey(key)
}
func validKey(key string) bool {
	return regexp.MustCompile(`^[a-zA-Z0-9_-]{32,64}\.(jpg|png)$`).MatchString(key)
}
func (s *Local) root(key string) (*os.Root, error) {
	if !s.validKey(key) {
		return nil, fmt.Errorf("invalid storage key")
	}
	return os.OpenRoot(s.directory)
}
func syncDirectory(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// Put creates a new file exclusively, and syncs its contents and directory before
// the caller may commit metadata. A partial write remains unreferenced for sweep.
func (s *Local) Put(ctx context.Context, key string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := s.root(key)
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := root.OpenFile(key, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	return errors.Join(writeErr, syncErr, closeErr, ctx.Err(), syncDirectory(root))
}
func (s *Local) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := s.root(key)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat(key)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("storage entry is not a regular file")
	}
	return root.Open(key)
}
func (s *Local) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := s.root(key)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Remove(key); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(root)
}
func (s *Local) Keys(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, entry := range entries {
		if s.validKey(entry.Name()) {
			if !entry.Type().IsRegular() {
				return nil, fmt.Errorf("photo storage contains a non-regular entry")
			}
			keys = append(keys, entry.Name())
		}
	}
	return keys, nil
}
