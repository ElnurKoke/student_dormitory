package inventory

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"student_dormitory/internal/modules/audit"
	"student_dormitory/internal/modules/auth"
	"student_dormitory/internal/platform/files"
)

func photoPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	img := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	img.Set(1, 1, color.NRGBA{R: 255, A: 128})
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func photoFixture(t *testing.T) (*fixture, *PhotoService, Location, *files.Local) {
	t.Helper()
	f := setup(t)
	target, _ := hierarchy(t, f)
	store, err := files.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return f, NewPhotoService(NewRepository(f.db), f.s.permissions, audit.NewRecorder(f.db), store), target, store
}
func uploadPhoto(t *testing.T, f *fixture, s *PhotoService, p Location, d PhotoDraft) Photo {
	t.Helper()
	photo, err := s.Upload(f.ctx, "writer-a", p, bytes.NewReader(photoPNG(t)), d)
	if err != nil {
		t.Fatal(err)
	}
	return photo
}
func TestPhotosMetadataOrderPrimaryVersionsAndPublication(t *testing.T) {
	f, s, target, store := photoFixture(t)
	first := uploadPhoto(t, f, s, target, PhotoDraft{Description: "<script>caption</script>", Position: 20})
	second := uploadPhoto(t, f, s, target, PhotoDraft{Position: 10, Primary: true})
	album, err := s.repo.Photos(f.ctx, target)
	if err != nil || len(album) != 2 || album[0].ID != second.ID || !album[0].Primary || album[1].Primary || album[1].Version != 2 {
		t.Fatalf("wrong album: %+v %v", album, err)
	}
	if err := s.Change(f.ctx, "writer-a", target, first.ID, 1, PhotoDraft{}, false); !errors.Is(err, ErrConflict) {
		t.Fatal("accepted stale demoted photo")
	}
	if err := s.Change(f.ctx, "writer-a", target, first.ID, 2, PhotoDraft{Description: "New", Position: 0, Primary: true}, false); err != nil {
		t.Fatal(err)
	}
	for _, p := range []Photo{first, second} {
		meta, reader, err := s.Open(f.ctx, "", p.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(reader)
		reader.Close()
		if int64(len(data)) != meta.ByteSize || meta.Width != 3 || meta.Height != 2 {
			t.Fatal("wrong stored metadata")
		}
	}
	// Publication is inherited from the real dormitory, independent of IDs in URLs.
	if _, err := f.db.ExecContext(f.ctx, "UPDATE inventory_dormitories SET published=0 WHERE id=?", target.DormitoryID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Open(f.ctx, "", first.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatal("unpublished photo is public")
	}
	_, reader, err := s.Open(f.ctx, "writer-a", first.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	reader.Close()
	if _, err := f.db.ExecContext(f.ctx, "UPDATE inventory_dormitories SET published=1 WHERE id=?; UPDATE inventory_buildings SET archived=1 WHERE id=?", target.DormitoryID, target.BuildingID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Open(f.ctx, "", first.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatal("archived ancestor bypass")
	}
	if err := s.Change(f.ctx, "writer-a", target, first.ID, 3, PhotoDraft{}, true); !errors.Is(err, ErrArchived) {
		t.Fatal("modified photo below archive")
	}
	if _, err := f.db.ExecContext(f.ctx, "UPDATE inventory_buildings SET archived=0 WHERE id=?", target.BuildingID); err != nil {
		t.Fatal(err)
	}
	if err := s.Change(f.ctx, "writer-a", target, first.ID, 3, PhotoDraft{}, true); err != nil {
		t.Fatal(err)
	}
	album, err = s.repo.Photos(f.ctx, target)
	if err != nil || len(album) != 1 || !album[0].Primary {
		t.Fatal("deletion failed to pick next primary")
	}
	if _, err := store.Open(f.ctx, first.StorageKey); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("deleted file remains")
	}
	dormPhoto := uploadPhoto(t, f, s, Location{DormitoryID: target.DormitoryID}, PhotoDraft{})
	if dormPhoto.DormitoryID != target.DormitoryID || dormPhoto.RoomID != "" {
		t.Fatal("dorm owner not stored")
	}
	var count int
	if err := f.db.QueryRowContext(f.ctx, "SELECT count(*) FROM audit_events WHERE object_type='inventory_photo'").Scan(&count); err != nil || count != 5 {
		t.Fatalf("audit count %d %v", count, err)
	}
}
func pngChunk(kind string, body []byte) []byte {
	out := make([]byte, len(body)+12)
	binary.BigEndian.PutUint32(out, uint32(len(body)))
	copy(out[4:8], kind)
	copy(out[8:], body)
	binary.BigEndian.PutUint32(out[len(out)-4:], crc32.ChecksumIEEE(out[4:len(out)-4]))
	return out
}
func TestPhotoFormatsLimitsAndMetadataRemoval(t *testing.T) {
	raw := photoPNG(t)
	// Ancillary metadata and trailing active content must never survive.
	withMetadata := append(append(append([]byte{}, raw[:33]...), pngChunk("tEXt", []byte("Comment\x00PRIVATE-METADATA"))...), raw[33:]...)
	withMetadata = append(withMetadata, []byte("<script>PAYLOAD</script>")...)
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil); err != nil {
		t.Fatal(err)
	}
	app1 := append([]byte{0xff, 0xe1, 0, 20}, []byte("Exif\x00\x00PRIVATE-DATA")...)
	// APP1 length includes the two length bytes.
	binary.BigEndian.PutUint16(app1[2:4], uint16(len(app1)-2))
	jpegMetadata := append(append(append([]byte{}, jpg.Bytes()[:2]...), app1...), jpg.Bytes()[2:]...)
	// Tiny lossless WebP, generated independently of the decoder under test.
	webp, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4TBUAAAAvAAAAEAcQERGIiP4HAA==")
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"png": withMetadata, "jpeg": jpegMetadata, "webp": webp} {
		t.Run(name, func(t *testing.T) {
			clean, meta, err := normalizePhoto(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(clean, []byte("PRIVATE")) || bytes.Contains(clean, []byte("PAYLOAD")) || bytes.Contains(clean, []byte("Exif")) {
				t.Fatal("metadata or active content survived")
			}
			if name == "webp" && meta.MediaType != "image/png" {
				t.Fatal("WebP was not normalized")
			}
			if _, _, err := image.Decode(bytes.NewReader(clean)); err != nil {
				t.Fatal("stored image invalid")
			}
		})
	}
	huge := append([]byte{}, raw...)
	binary.BigEndian.PutUint32(huge[16:20], MaxPhotoSide+1)
	binary.BigEndian.PutUint32(huge[29:33], crc32.ChecksumIEEE(huge[12:29]))
	tooManyPixels := append([]byte{}, raw...)
	binary.BigEndian.PutUint32(tooManyPixels[16:20], 4000)
	binary.BigEndian.PutUint32(tooManyPixels[20:24], 4000)
	binary.BigEndian.PutUint32(tooManyPixels[29:33], crc32.ChecksumIEEE(tooManyPixels[12:29]))
	for _, data := range [][]byte{nil, []byte("<svg onload='evil()'></svg>"), []byte("GIF89a"), []byte("RIFFbadWEBP"), raw[:40], huge, tooManyPixels, bytes.Repeat([]byte{1}, int(MaxPhotoBytes+1))} {
		if _, _, err := normalizePhoto(bytes.NewReader(data)); !errors.Is(err, ErrPhoto) {
			t.Fatalf("invalid file accepted: %v", err)
		}
	}
	if _, err := validatePhotoDraft(PhotoDraft{Description: strings.Repeat("я", 1001)}); err == nil {
		t.Fatal("long description accepted")
	}
}

type photoFaultStore struct {
	PhotoStore
	failPut, failDelete bool
	afterPut            func()
}

func (s *photoFaultStore) Put(ctx context.Context, key string, b []byte) error {
	if err := s.PhotoStore.Put(ctx, key, b); err != nil {
		return err
	}
	if s.afterPut != nil {
		s.afterPut()
	}
	if s.failPut {
		return errors.New("simulated put failure after file creation")
	}
	return nil
}
func (s *photoFaultStore) Delete(ctx context.Context, key string) error {
	if s.failDelete {
		return errors.New("simulated delete failure")
	}
	return s.PhotoStore.Delete(ctx, key)
}
func TestPhotoFailureCleanupAndRecovery(t *testing.T) {
	for _, failure := range []string{"audit", "put", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			f, s, p, store := photoFixture(t)
			kept := uploadPhoto(t, f, s, p, PhotoDraft{})
			fault := &photoFaultStore{PhotoStore: store}
			s.store = fault
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			switch failure {
			case "audit":
				if _, err := f.db.ExecContext(f.ctx, `CREATE TRIGGER reject_photo_audit BEFORE INSERT ON audit_events WHEN NEW.object_type='inventory_photo' BEGIN SELECT RAISE(ABORT,'failure'); END`); err != nil {
					t.Fatal(err)
				}
			case "put":
				fault.failPut = true
			case "cancel":
				fault.afterPut = cancel
			}
			if _, err := s.Upload(ctx, "writer-a", p, bytes.NewReader(photoPNG(t)), PhotoDraft{Primary: true}); err == nil {
				t.Fatal("failure accepted")
			}
			keys, err := store.Keys(f.ctx)
			if err != nil || len(keys) != 1 || keys[0] != kept.StorageKey {
				t.Fatalf("failed save leaked or removed file: %v %v", keys, err)
			}
			photos, err := s.repo.Photos(f.ctx, p)
			if err != nil || len(photos) != 1 || !photos[0].Primary || photos[0].Version != 1 {
				t.Fatal("rollback changed existing primary")
			}
		})
	}
	f, s, p, store := photoFixture(t)
	photo := uploadPhoto(t, f, s, p, PhotoDraft{})
	fault := &photoFaultStore{PhotoStore: store, failDelete: true}
	s.store = fault
	if err := s.Change(f.ctx, "writer-a", p, photo.ID, 1, PhotoDraft{}, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Open(f.ctx, "", photo.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted photo exposed during store outage")
	}
	if err := s.Reconcile(f.ctx); err == nil {
		t.Fatal("cleanup failure hidden")
	}
	// Same sweep on a fresh service models process restart after delete failure.
	restarted := NewPhotoService(NewRepository(f.db), f.s.permissions, audit.NewRecorder(f.db), store)
	if err := restarted.Reconcile(f.ctx); err != nil {
		t.Fatal(err)
	}
	keys, _ := store.Keys(f.ctx)
	if len(keys) != 0 {
		t.Fatal("recovery leaked orphan")
	}
	// Crash between durable Put and metadata commit leaves a discoverable orphan.
	if err := store.Put(f.ctx, rand.Text()+rand.Text()+".png", photoPNG(t)); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Reconcile(f.ctx); err != nil {
		t.Fatal(err)
	}
	keys, _ = store.Keys(f.ctx)
	if len(keys) != 0 {
		t.Fatal("crashed upload not collected")
	}
}
func TestPhotoPermissionsChainAndTransactionRecheck(t *testing.T) {
	f, s, p, _ := photoFixture(t)
	photo := uploadPhoto(t, f, s, p, PhotoDraft{})
	for _, actor := range []string{"writer-b", "sys"} {
		if _, err := s.Upload(f.ctx, actor, p, bytes.NewReader(photoPNG(t)), PhotoDraft{}); !errors.Is(err, auth.ErrForbidden) {
			t.Fatal("write permission bypass")
		}
		if err := s.Change(f.ctx, actor, p, photo.ID, 1, PhotoDraft{}, true); !errors.Is(err, auth.ErrForbidden) {
			t.Fatal("delete permission bypass")
		}
		if _, _, err := s.Open(f.ctx, actor, photo.ID, false); !errors.Is(err, auth.ErrForbidden) {
			t.Fatal("private read bypass")
		}
	}
	forged := p
	forged.BuildingID = "other"
	if _, err := s.Upload(f.ctx, "writer-a", forged, bytes.NewReader(photoPNG(t)), PhotoDraft{}); !errors.Is(err, ErrNotFound) {
		t.Fatal("forged ancestor accepted")
	}
	if err := s.Change(f.ctx, "writer-a", Location{DormitoryID: p.DormitoryID}, photo.ID, 1, PhotoDraft{}, true); !errors.Is(err, ErrNotFound) {
		t.Fatal("photo moved between owners")
	}
	s.permissions = photoTxDenied{Authorizer: f.s.permissions}
	if _, err := s.Upload(f.ctx, "writer-a", p, bytes.NewReader(photoPNG(t)), PhotoDraft{}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("write skipped transactional permission check")
	}
}

type photoTxDenied struct{ Authorizer }

func (photoTxDenied) AuthorizeTx(context.Context, *sql.Tx, string, string, string) error {
	return auth.ErrForbidden
}

func TestPhotoConcurrentChangesAndSweep(t *testing.T) {
	f, s, p, store := photoFixture(t)
	photo := uploadPhoto(t, f, s, p, PhotoDraft{})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Go(func() { results <- s.Change(f.ctx, "writer-a", p, photo.ID, 1, PhotoDraft{Position: 5}, false) })
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("lost update")
	}
	started, release := make(chan struct{}), make(chan struct{})
	fault := &photoFaultStore{PhotoStore: store, afterPut: func() { close(started); <-release }}
	s.store = fault
	uploaded := make(chan error, 1)
	data := photoPNG(t)
	go func() { _, err := s.Upload(f.ctx, "writer-a", p, bytes.NewReader(data), PhotoDraft{}); uploaded <- err }()
	<-started
	sweeping := make(chan error, 1)
	go func() { sweeping <- s.Reconcile(f.ctx) }()
	close(release)
	if err := <-uploaded; err != nil {
		t.Fatal(err)
	}
	if err := <-sweeping; err != nil {
		t.Fatal(err)
	}
	keys, err := store.Keys(f.ctx)
	if err != nil || len(keys) != 2 {
		t.Fatal("sweep raced with upload")
	}
}

func TestPhotoDeleteAndEditRollbackWithAudit(t *testing.T) {
	f, s, p, store := photoFixture(t)
	photo := uploadPhoto(t, f, s, p, PhotoDraft{})
	next := uploadPhoto(t, f, s, p, PhotoDraft{})
	if _, err := f.db.ExecContext(f.ctx, `CREATE TRIGGER reject_photo_audit BEFORE INSERT ON audit_events WHEN NEW.object_type='inventory_photo' BEGIN SELECT RAISE(ABORT,'failure'); END`); err != nil {
		t.Fatal(err)
	}
	for _, remove := range []bool{false, true} {
		if err := s.Change(f.ctx, "writer-a", p, photo.ID, 1, PhotoDraft{Description: "Should roll back", Position: 10}, remove); err == nil {
			t.Fatal("audit failure ignored")
		}
		album, err := s.repo.Photos(f.ctx, p)
		if err != nil || len(album) != 2 {
			t.Fatal("rollback lost metadata")
		}
		persisted, err := s.repo.Photo(f.ctx, photo.ID)
		if err != nil || persisted.Version != 1 || !persisted.Primary || persisted.Description != "" {
			t.Fatal("rollback lost original state")
		}
		reader, err := store.Open(f.ctx, photo.StorageKey)
		if err != nil {
			t.Fatal("file removed before commit")
		}
		reader.Close()
		persisted, err = s.repo.Photo(f.ctx, next.ID)
		if err != nil || persisted.Version != 1 || persisted.Primary {
			t.Fatal("replacement primary not rolled back")
		}
	}
	if _, err := f.db.ExecContext(f.ctx, "DROP TRIGGER reject_photo_audit"); err != nil {
		t.Fatal(err)
	}
	s.store = &photoFaultStore{PhotoStore: store, afterPut: func() { panic("sensitive panic value") }}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()
		_, _ = s.Upload(f.ctx, "writer-a", p, bytes.NewReader(photoPNG(t)), PhotoDraft{Primary: true})
	}()
	keys, err := store.Keys(f.ctx)
	if err != nil || len(keys) != 2 {
		t.Fatal("panic leaked file")
	}
	persisted, err := s.repo.Photo(f.ctx, photo.ID)
	if err != nil || persisted.Version != 1 || !persisted.Primary {
		t.Fatal("panic changed original")
	}
}
func TestPhotoReconcileDoesNotDeleteOnDBFailureOrMissingReference(t *testing.T) {
	f, s, p, store := photoFixture(t)
	photo := uploadPhoto(t, f, s, p, PhotoDraft{})
	if err := store.Delete(f.ctx, photo.StorageKey); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(f.ctx); err == nil {
		t.Fatal("missing referenced file not detected")
	}
	key := rand.Text() + rand.Text() + ".png"
	if err := store.Put(f.ctx, key, photoPNG(t)); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(f.ctx); err == nil {
		t.Fatal("DB error ignored")
	}
	reader, err := store.Open(context.Background(), key)
	if err != nil {
		t.Fatal("deleted files without known reference set")
	}
	reader.Close()
}
