package applications

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"student_dormitory/internal/modules/audit"
)

type DocumentService struct {
	s     *Service
	store DocumentStore
}

func NewDocumentService(s *Service, store DocumentStore) *DocumentService {
	return &DocumentService{s, store}
}
func (s *DocumentService) Reconcile(ctx context.Context) error {
	return s.s.repo.Exclusive(ctx, func(r *Repository, _ *sql.Tx) error {
		refs, err := r.DocumentKeys(ctx)
		if err != nil {
			return err
		}
		keys, err := s.store.Keys(ctx)
		if err != nil {
			return err
		}
		found := map[string]bool{}
		for _, k := range keys {
			found[k] = true
		}
		for k := range refs {
			if !found[k] {
				return fmt.Errorf("referenced application document missing")
			}
		}
		var failures error
		for _, k := range keys {
			if !refs[k] {
				failures = errors.Join(failures, s.store.Delete(ctx, k))
			}
		}
		return failures
	})
}
func (s *DocumentService) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.Reconcile(ctx)
}
func (s *DocumentService) Upload(ctx context.Context, actor, id, documentID, label, filename string, version int64, input io.Reader) (Document, error) {
	// Authorize the actual owner before parsing file contents; repeat under the lock.
	a, _, err := s.s.Detail(ctx, actor, id)
	if err != nil {
		return Document{}, err
	}
	if !a.Editable() || a.Version != version {
		return Document{}, ErrConflict
	}
	label = strings.TrimSpace(label)
	if !utf8.ValidString(label) || utf8.RuneCountInString(label) > 120 || strings.ContainsRune(label, 0) || (documentID == "" && label == "") {
		return Document{}, ErrDocument
	}
	data, media, ext, err := validateDocument(input, filename)
	if err != nil {
		return Document{}, err
	}
	vid, err := newID()
	if err != nil {
		return Document{}, err
	}
	create := documentID == ""
	if create {
		documentID, err = newID()
		if err != nil {
			return Document{}, err
		}
	}
	d := Document{ID: vid, DocumentID: documentID, ApplicationID: id, Label: label, StorageKey: vid + ext, MediaType: media, ByteSize: int64(len(data)), SHA256: documentHash(data), UploadedBy: actor, UploadedAt: timestamp(), Version: 1}
	defer s.cleanup()
	err = s.s.change(ctx, actor, func(r *Repository) (audit.Event, error) {
		a, err := r.Get(ctx, actor, id)
		if err != nil {
			return audit.Event{}, err
		}
		if !a.Editable() || a.Version != version {
			return audit.Event{}, ErrConflict
		}
		if !create {
			docs, err := r.Documents(ctx, id)
			if err != nil {
				return audit.Event{}, err
			}
			found := false
			for _, old := range docs {
				if old.DocumentID == documentID && old.Current {
					d.Version = old.Version + 1
					d.Label = old.Label
					found = true
					break
				}
			}
			if !found {
				return audit.Event{}, ErrNotFound
			}
		}
		if err := s.store.Put(ctx, d.StorageKey, data); err != nil {
			return audit.Event{}, err
		}
		if err := r.InsertDocument(ctx, d, create); err != nil {
			return audit.Event{}, err
		}
		a.Version++
		a.ContentRevision++
		if a.Status == Verified {
			a.Status = NeedsInformation
		}
		if err := r.Update(ctx, a, version); err != nil {
			return audit.Event{}, err
		}
		if err := r.AddHistory(ctx, a, "document_uploaded", d.UploadedAt); err != nil {
			return audit.Event{}, err
		}
		return event(actor, "application_document", d.ID, "document_uploaded"), nil
	})
	return d, err
}
func (s *DocumentService) Open(ctx context.Context, actor, id, versionID string, staff bool) (Document, io.ReadCloser, error) {
	var d Document
	var reader io.ReadCloser
	err := s.s.repo.Exclusive(ctx, func(r *Repository, tx *sql.Tx) error {
		permission := "applications.own"
		if staff {
			permission = "applications.review"
		}
		if err := s.s.permissions.AuthorizeTx(ctx, tx, actor, permission, ""); err != nil {
			return err
		}
		var a Application
		var err error
		if staff {
			a, err = r.Assigned(ctx, actor, id)
		} else {
			a, err = r.Get(ctx, actor, id)
		}
		if err != nil {
			return err
		}
		docs, err := r.Documents(ctx, a.ID)
		if err != nil {
			return err
		}
		found := false
		for _, doc := range docs {
			if doc.ID == versionID {
				d = doc
				found = true
				break
			}
		}
		if !found {
			return ErrNotFound
		}
		reader, err = s.store.Open(ctx, d.StorageKey)
		return err
	})
	if err != nil && reader != nil {
		reader.Close()
		reader = nil
	}
	return d, reader, err
}
