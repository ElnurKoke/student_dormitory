package files

import (
	"context"
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateLocalStorage(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "photos")
	store, err := NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	key := rand.Text() + rand.Text() + ".png"
	if err := store.Put(ctx, key, []byte("pixels")); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, key, []byte("replacement")); err == nil {
		t.Fatal("overwrote existing key")
	}
	info, err := os.Stat(filepath.Join(dir, key))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("unsafe file mode")
	}
	info, err = os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("unsafe directory mode")
	}
	reader, err := store.Open(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := store.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil || string(data) != "pixels" {
		t.Fatal("open reader lost content after deletion")
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatal("delete not idempotent")
	}
	for _, key := range []string{"../escape.png", "/tmp/image.png", "evil.html", "a/b.png"} {
		if err := store.Put(ctx, key, []byte("payload")); err == nil {
			t.Fatal("unsafe name accepted")
		}
		if _, err := store.Open(ctx, key); err == nil {
			t.Fatal("unsafe name opened")
		}
		if err := store.Delete(ctx, key); err == nil {
			t.Fatal("unsafe name removed")
		}
	}
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, key)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(ctx, key); err == nil {
		t.Fatal("symlink served")
	}
	if _, err := store.Keys(ctx); err == nil {
		t.Fatal("symlink can pass startup reconciliation")
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "private" {
		t.Fatal("outside file changed")
	}
}
