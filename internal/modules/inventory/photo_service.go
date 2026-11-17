package inventory

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"student_dormitory/internal/modules/audit"
)

type PhotoService struct {
	repo        *Repository
	permissions Authorizer
	audit       *audit.Recorder
	store       PhotoStore
}

func NewPhotoService(repo *Repository, permissions Authorizer, recorder *audit.Recorder, store PhotoStore) *PhotoService {
	return &PhotoService{repo: repo, permissions: permissions, audit: recorder, store: store}
}
func (s *PhotoService) authorize(ctx context.Context, tx *sql.Tx, actor string, p Location) error {
	for _, grant := range []string{"registry.read", "registry.write"} {
		if err := s.permissions.AuthorizeTx(ctx, tx, actor, grant, p.DormitoryID); err != nil {
			return err
		}
	}
	return nil
}
func photoTarget(ctx context.Context, repo *Repository, p Location) ([]Node, error) {
	if p.Kind() != Dormitory && p.Kind() != Room {
		return nil, ErrNotFound
	}
	return loadChain(ctx, repo, p)
}
func ownsPhoto(photo Photo, p Location) bool {
	return (p.Kind() == Dormitory && photo.DormitoryID == p.DormitoryID) || (p.Kind() == Room && photo.RoomID == p.RoomID)
}
func photoEvent(actor, action, scope string, before, after *Photo) (audit.Event, error) {
	// Descriptions and file contents are deliberately excluded from audit payloads.
	snap := func(p *Photo) any {
		if p == nil {
			return nil
		}
		return map[string]any{"id": p.ID, "dormitory_id": p.DormitoryID, "room_id": p.RoomID, "version": p.Version, "position": p.Position, "primary": p.Primary}
	}
	data, err := json.Marshal(map[string]any{"before": snap(before), "after": snap(after)})
	id := ""
	if after != nil {
		id = after.ID
	} else if before != nil {
		id = before.ID
	}
	return audit.Event{ActorID: actor, Action: "inventory.photo." + action, ObjectType: "inventory_photo", ObjectID: id, DormitoryID: scope, Result: "success", ChangesJSON: string(data)}, err
}

// Reconcile removes only unreferenced keys, with the same DB write lock used by
// uploads. Failed deletions remain discoverable on the next run. Never sweep
// when the reference query fails. Run at startup and after upload/deletion.
func (s *PhotoService) Reconcile(ctx context.Context) error {
	return s.repo.Exclusive(ctx, func(repo *Repository, _ *sql.Tx) error {
		referenced, err := repo.PhotoKeys(ctx)
		if err != nil {
			return err
		}
		keys, err := s.store.Keys(ctx)
		if err != nil {
			return err
		}
		found := map[string]bool{}
		for _, key := range keys {
			found[key] = true
		}
		for key := range referenced {
			if !found[key] {
				return fmt.Errorf("referenced photo file is missing")
			}
		}
		var failures error
		for _, key := range keys {
			if !referenced[key] {
				failures = errors.Join(failures, s.store.Delete(ctx, key))
			}
		}
		return failures
	})
}
func (s *PhotoService) cleanup() {
	// A canceled HTTP request must not cancel rollback cleanup. If the store is
	// unavailable, the startup sweep retries; metadata never exposes orphan keys.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.Reconcile(ctx)
}
func (s *PhotoService) Upload(ctx context.Context, actor string, target Location, input io.Reader, draft PhotoDraft) (Photo, error) {
	// Fail forbidden requests before decoding untrusted pixels.
	for _, grant := range []string{"registry.read", "registry.write"} {
		if err := s.permissions.Authorize(ctx, actor, grant, target.DormitoryID); err != nil {
			return Photo{}, err
		}
	}
	draft, err := validatePhotoDraft(draft)
	if err != nil {
		return Photo{}, err
	}
	data, photo, err := normalizePhoto(input)
	if err != nil {
		return Photo{}, err
	}
	photo.ID = rand.Text()
	extension := ".png"
	if photo.MediaType == "image/jpeg" {
		extension = ".jpg"
	}
	photo.StorageKey = rand.Text() + rand.Text() + extension
	photo.Description, photo.Position = draft.Description, draft.Position
	if target.Kind() == Dormitory {
		photo.DormitoryID = target.DormitoryID
	} else {
		photo.RoomID = target.RoomID
	}
	// Defer also runs on panic, after RecordChange's rollback. Checking references
	// rather than blindly removing the key handles an ambiguous commit safely.
	defer s.cleanup()
	err = s.audit.RecordChange(ctx, func(ctx context.Context, tx *sql.Tx) (audit.Event, error) {
		if err := s.authorize(ctx, tx, actor, target); err != nil {
			return audit.Event{}, err
		}
		repo := s.repo.WithTx(tx)
		chain, err := photoTarget(ctx, repo, target)
		if err != nil {
			return audit.Event{}, err
		}
		if err := active(chain); err != nil {
			return audit.Event{}, err
		}
		existing, err := repo.Photos(ctx, target)
		if err != nil {
			return audit.Event{}, err
		}
		photo.Primary = draft.Primary || len(existing) == 0
		if photo.Primary {
			if err := repo.DemotePhotos(ctx, target); err != nil {
				return audit.Event{}, err
			}
		}
		// File must be durable BEFORE its metadata can commit. No raw upload is stored.
		if err := s.store.Put(ctx, photo.StorageKey, data); err != nil {
			return audit.Event{}, err
		}
		if err := repo.InsertPhoto(ctx, photo); err != nil {
			return audit.Event{}, err
		}
		photo, err = repo.Photo(ctx, photo.ID)
		if err != nil {
			return audit.Event{}, err
		}
		return photoEvent(actor, "created", target.DormitoryID, nil, &photo)
	})
	return photo, err
}
func (s *PhotoService) Change(ctx context.Context, actor string, target Location, id string, version int64, draft PhotoDraft, remove bool) error {
	draft, err := validatePhotoDraft(draft)
	if err != nil {
		return err
	}
	if remove {
		defer s.cleanup()
	}
	return s.audit.RecordChange(ctx, func(ctx context.Context, tx *sql.Tx) (audit.Event, error) {
		if err := s.authorize(ctx, tx, actor, target); err != nil {
			return audit.Event{}, err
		}
		repo := s.repo.WithTx(tx)
		chain, err := photoTarget(ctx, repo, target)
		if err != nil {
			return audit.Event{}, err
		}
		if err := active(chain); err != nil {
			return audit.Event{}, err
		}
		old, err := repo.Photo(ctx, id)
		if err != nil {
			return audit.Event{}, err
		}
		if !ownsPhoto(old, target) {
			return audit.Event{}, ErrNotFound
		}
		if old.Version != version {
			return audit.Event{}, ErrConflict
		}
		if remove {
			if err := repo.RemovePhoto(ctx, id, version); err != nil {
				return audit.Event{}, err
			}
			if old.Primary {
				remaining, err := repo.Photos(ctx, target)
				if err != nil {
					return audit.Event{}, err
				}
				if len(remaining) > 0 {
					next := remaining[0]
					next.Primary = true
					if err := repo.UpdatePhoto(ctx, next, next.Version); err != nil {
						return audit.Event{}, err
					}
				}
			}
			return photoEvent(actor, "deleted", target.DormitoryID, &old, nil)
		}
		updated := old
		updated.Description, updated.Position = draft.Description, draft.Position
		if draft.Primary && !old.Primary {
			if err := repo.DemotePhotos(ctx, target); err != nil {
				return audit.Event{}, err
			}
			updated.Primary = true
		}
		if err := repo.UpdatePhoto(ctx, updated, version); err != nil {
			return audit.Event{}, err
		}
		updated.Version++
		return photoEvent(actor, "updated", target.DormitoryID, &old, &updated)
	})
}

// Open checks the real owner and publication on every request, including HEAD.
// Opening under the lock prevents reconciliation unlinking between lookup/open.
func (s *PhotoService) Open(ctx context.Context, actor, id string, public bool) (Photo, io.ReadCloser, error) {
	var photo Photo
	var reader io.ReadCloser
	err := s.repo.Exclusive(ctx, func(repo *Repository, tx *sql.Tx) error {
		var err error
		photo, err = repo.Photo(ctx, id)
		if err != nil {
			return err
		}
		target, err := repo.PhotoLocation(ctx, photo)
		if err != nil {
			return err
		}
		chain, err := photoTarget(ctx, repo, target)
		if err != nil {
			return err
		}
		if public {
			if !chain[0].Published || active(chain) != nil {
				return ErrNotFound
			}
		} else {
			// Exclusive supplies a repository bound to the same transaction.
			if err := s.permissions.AuthorizeTx(ctx, tx, actor, "registry.read", target.DormitoryID); err != nil {
				return err
			}
		}
		reader, err = s.store.Open(ctx, photo.StorageKey)
		return err
	})
	if err != nil && reader != nil {
		reader.Close()
		reader = nil
	}
	return photo, reader, err
}
