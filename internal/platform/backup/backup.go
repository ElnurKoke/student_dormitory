// Package backup creates and verifies offline bundles of SQLite plus referenced
// immutable files. It knows no business tables: references come from a consumer.
package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"student_dormitory/internal/platform/database"
	"student_dormitory/internal/platform/maintenance"
)

type Reference struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}
type File struct {
	Reference
	SHA256 string `json:"sha256"`
}
type Manifest struct {
	Version   int    `json:"version"`
	CreatedAt string `json:"created_at"`
	Database  File   `json:"database"`
	Assets    []File `json:"assets"`
}
type References func(context.Context, *sql.DB) ([]Reference, error)

const DatabaseName = "dormitory.db"
const ManifestName = "manifest.json"

func syncPath(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
func reserve(destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		return err
	}
	marker, err := os.OpenFile(filepath.Join(destination, maintenance.IncompleteMarker), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = io.WriteString(marker, "incomplete\n"); err != nil {
		marker.Close()
		return err
	}
	return errors.Join(marker.Sync(), marker.Close(), syncPath(destination), syncPath(filepath.Dir(destination)))
}
func finish(destination string) error {
	if err := os.Remove(filepath.Join(destination, maintenance.IncompleteMarker)); err != nil {
		return err
	}
	return syncPath(destination)
}
func validReference(ref Reference) bool {
	return fs.ValidPath(ref.Path) && !strings.Contains(ref.Path, "\\") && ref.Path != "." && ref.Size > 0
}
func openRegular(root *os.Root, path string) (*os.File, error) {
	if !fs.ValidPath(path) || strings.Contains(path, "\\") {
		return nil, fmt.Errorf("invalid bundle path")
	}
	parts := strings.Split(path, "/")
	for i := range parts {
		info, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return nil, err
		}
		if i < len(parts)-1 {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("invalid directory in bundle")
			}
		} else if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("asset must be a regular file")
		}
	}
	return root.Open(path)
}
func digest(ctx context.Context, root *os.Root, ref Reference) (File, error) {
	if !validReference(ref) {
		return File{}, fmt.Errorf("invalid file reference")
	}
	input, err := openRegular(root, ref.Path)
	if err != nil {
		return File{}, err
	}
	defer input.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(contextReader{ctx, input}, ref.Size+1))
	if err != nil {
		return File{}, err
	}
	if n != ref.Size {
		return File{}, fmt.Errorf("file size mismatch")
	}
	return File{Reference: ref, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
func copyAsset(ctx context.Context, source *os.Root, ref Reference, destRoot string) (File, error) {
	if !validReference(ref) {
		return File{}, fmt.Errorf("invalid file reference")
	}
	input, err := openRegular(source, ref.Path)
	if err != nil {
		return File{}, err
	}
	defer input.Close()
	destination := filepath.Join(destRoot, filepath.FromSlash(ref.Path))
	if err = os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return File{}, err
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return File{}, err
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(contextReader{ctx, input}, ref.Size+1))
	err = errors.Join(copyErr, output.Sync(), output.Close(), syncPath(filepath.Dir(destination)))
	if err != nil {
		return File{}, err
	}
	if n != ref.Size {
		return File{}, fmt.Errorf("asset size mismatch")
	}
	return File{Reference: ref, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}
func canonicalCandidate(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current := absolute
	var missing []string
	for {
		_, err = os.Lstat(current)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		next := filepath.Dir(current)
		if next == current {
			return "", err
		}
		current = next
	}
	real, err := filepath.EvalSymlinks(current)
	if err != nil {
		return "", err
	}
	for i := len(missing) - 1; i >= 0; i-- {
		real = filepath.Join(real, missing[i])
	}
	return real, nil
}
func within(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)))
}
func pathOutside(destination, filesDir, databasePath string) error {
	dest, err := canonicalCandidate(destination)
	if err != nil {
		return err
	}
	storage, err := canonicalCandidate(filesDir)
	if err != nil {
		return err
	}
	if within(storage, dest) {
		return fmt.Errorf("backup destination must be outside file storage")
	}
	dbPath, err := canonicalCandidate(databasePath)
	if err != nil {
		return err
	}
	if dest == dbPath {
		return fmt.Errorf("backup destination conflicts with database")
	}
	return nil
}

func Create(ctx context.Context, databasePath, filesDir, destination string, references References) (Manifest, error) {
	var manifest Manifest
	if references == nil {
		return manifest, fmt.Errorf("reference reader is required")
	}
	guard, err := maintenance.Acquire(databasePath, filesDir)
	if err != nil {
		return manifest, err
	}
	defer guard.Close()
	if err = pathOutside(destination, filesDir, databasePath); err != nil {
		return manifest, err
	}
	release, err := database.FreezeWriters(ctx, databasePath)
	if err != nil {
		return manifest, err
	}
	defer release()
	sourceDB, err := database.OpenReadOnly(ctx, databasePath)
	if err != nil {
		return manifest, err
	}
	defer sourceDB.Close()
	if err = database.CheckCurrentSchema(ctx, sourceDB); err != nil {
		return manifest, err
	}
	if err = database.CheckIntegrity(ctx, sourceDB); err != nil {
		return manifest, err
	}
	if err = reserve(destination); err != nil {
		return manifest, err
	}
	dbPath := filepath.Join(destination, DatabaseName)
	if err = database.Snapshot(ctx, sourceDB, dbPath); err != nil {
		return manifest, err
	}
	snapshot, err := database.OpenReadOnly(ctx, dbPath)
	if err != nil {
		return manifest, err
	}
	if err = database.CheckIntegrity(ctx, snapshot); err != nil {
		snapshot.Close()
		return manifest, err
	}
	refs, err := references(ctx, snapshot)
	closeErr := snapshot.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return manifest, err
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Path < refs[j].Path })
	sourceFiles, err := os.OpenRoot(filesDir)
	if err != nil {
		return manifest, err
	}
	defer sourceFiles.Close()
	assetDir := filepath.Join(destination, "files")
	if err = os.Mkdir(assetDir, 0700); err != nil {
		return manifest, err
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		if seen[ref.Path] {
			return manifest, fmt.Errorf("duplicate file reference")
		}
		seen[ref.Path] = true
		file, err := copyAsset(ctx, sourceFiles, ref, assetDir)
		if err != nil {
			return manifest, err
		}
		manifest.Assets = append(manifest.Assets, file)
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		return manifest, err
	}
	defer root.Close()
	info, err := os.Stat(dbPath)
	if err != nil {
		return manifest, err
	}
	manifest.Database, err = digest(ctx, root, Reference{Path: DatabaseName, Size: info.Size()})
	if err != nil {
		return manifest, err
	}
	manifest.Version = 1
	manifest.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return manifest, err
	}
	f, err := os.OpenFile(filepath.Join(destination, ManifestName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return manifest, err
	}
	_, writeErr := f.Write(body)
	if err = errors.Join(writeErr, f.Sync(), f.Close(), syncPath(assetDir), syncPath(destination), ctx.Err()); err != nil {
		return manifest, err
	}
	if err = finish(destination); err != nil {
		return manifest, err
	}
	return manifest, nil
}
func Verify(ctx context.Context, directory string, references References) (Manifest, error) {
	var manifest Manifest
	if references == nil {
		return manifest, fmt.Errorf("reference reader is required")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return manifest, err
	}
	defer root.Close()
	if _, err = root.Lstat(maintenance.IncompleteMarker); err == nil {
		return manifest, maintenance.ErrIncomplete
	} else if !errors.Is(err, os.ErrNotExist) {
		return manifest, err
	}
	input, err := openRegular(root, ManifestName)
	if err != nil {
		return manifest, err
	}
	decoder := json.NewDecoder(io.LimitReader(contextReader{ctx, input}, 16<<20))
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&manifest)
	var trailing any
	if err == nil && decoder.Decode(&trailing) != io.EOF {
		err = fmt.Errorf("invalid manifest suffix")
	}
	input.Close()
	if err != nil {
		return manifest, err
	}
	if manifest.Version != 1 || manifest.Database.Path != DatabaseName {
		return manifest, fmt.Errorf("unsupported backup format")
	}
	for _, file := range append([]File{manifest.Database}, manifest.Assets...) {
		if !validReference(file.Reference) || len(file.SHA256) != 64 {
			return manifest, fmt.Errorf("invalid manifest entry")
		}
		if _, err := hex.DecodeString(file.SHA256); err != nil {
			return manifest, fmt.Errorf("invalid checksum")
		}
	}
	actual, err := digest(ctx, root, manifest.Database.Reference)
	if err != nil || actual.SHA256 != manifest.Database.SHA256 {
		return manifest, fmt.Errorf("database checksum mismatch")
	}
	// Standalone snapshots must have no sidecars to override the verified main DB.
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := root.Lstat(DatabaseName + suffix); err == nil {
			return manifest, fmt.Errorf("unexpected SQLite sidecar")
		} else if !errors.Is(err, os.ErrNotExist) {
			return manifest, err
		}
	}
	db, err := database.OpenReadOnly(ctx, filepath.Join(directory, DatabaseName))
	if err != nil {
		return manifest, err
	}
	defer db.Close()
	if err = database.CheckCurrentSchema(ctx, db); err != nil {
		return manifest, err
	}
	if err = database.CheckIntegrity(ctx, db); err != nil {
		return manifest, err
	}
	refs, err := references(ctx, db)
	if err != nil {
		return manifest, err
	}
	expected := map[string]int64{}
	for _, ref := range refs {
		if !validReference(ref) || expected[ref.Path] != 0 {
			return manifest, fmt.Errorf("invalid database file reference")
		}
		expected[ref.Path] = ref.Size
	}
	if len(refs) != len(manifest.Assets) {
		return manifest, fmt.Errorf("asset manifest does not match database")
	}
	assetInfo, err := root.Lstat("files")
	if err != nil {
		return manifest, err
	}
	if !assetInfo.IsDir() || assetInfo.Mode()&os.ModeSymlink != 0 {
		return manifest, fmt.Errorf("invalid asset directory")
	}
	assetsRoot, err := os.OpenRoot(filepath.Join(directory, "files"))
	if err != nil {
		return manifest, err
	}
	defer assetsRoot.Close()
	for _, file := range manifest.Assets {
		if expected[file.Path] != file.Size {
			return manifest, fmt.Errorf("asset manifest does not match database")
		}
		delete(expected, file.Path)
		actual, err := digest(ctx, assetsRoot, file.Reference)
		if err != nil || actual.SHA256 != file.SHA256 {
			return manifest, fmt.Errorf("asset checksum mismatch")
		}
	}
	if len(expected) != 0 {
		return manifest, fmt.Errorf("missing assets")
	}
	return manifest, ctx.Err()
}
func Restore(ctx context.Context, source, destination string, references References) (Manifest, error) {
	manifest, err := Verify(ctx, source, references)
	if err != nil {
		return manifest, err
	}
	sourcePath, err := canonicalCandidate(source)
	if err != nil {
		return manifest, err
	}
	targetPath, err := canonicalCandidate(destination)
	if err != nil {
		return manifest, err
	}
	if within(sourcePath, targetPath) || within(targetPath, sourcePath) {
		return manifest, fmt.Errorf("restore paths must not overlap")
	}
	// Refuse replacing even an empty existing directory. The marker remains on any
	// failure and prevents a future server from opening an incomplete data pair.
	if err = reserve(destination); err != nil {
		return manifest, err
	}
	guard, err := maintenance.AcquireForRestore(filepath.Join(destination, DatabaseName), filepath.Join(destination, "files"))
	if err != nil {
		return manifest, err
	}
	defer guard.Close()
	root, err := os.OpenRoot(source)
	if err != nil {
		return manifest, err
	}
	defer root.Close()
	actual, err := copyAsset(ctx, root, manifest.Database.Reference, destination)
	if err != nil || actual.SHA256 != manifest.Database.SHA256 {
		return manifest, fmt.Errorf("database changed during restore")
	}
	assets, err := os.OpenRoot(filepath.Join(source, "files"))
	if err != nil {
		return manifest, err
	}
	defer assets.Close()
	for _, file := range manifest.Assets {
		actual, err := copyAsset(ctx, assets, file.Reference, filepath.Join(destination, "files"))
		if err != nil || actual.SHA256 != file.SHA256 {
			return manifest, fmt.Errorf("asset changed during restore")
		}
	}
	db, err := database.OpenReadOnly(ctx, filepath.Join(destination, DatabaseName))
	if err != nil {
		return manifest, err
	}
	err = database.CheckIntegrity(ctx, db)
	closeErr := db.Close()
	if err = errors.Join(err, closeErr, ctx.Err(), syncPath(filepath.Join(destination, "files"))); err != nil {
		return manifest, err
	}
	if err = finish(destination); err != nil {
		return manifest, err
	}
	return manifest, nil
}
