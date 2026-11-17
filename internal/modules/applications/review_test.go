package applications

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"student_dormitory/internal/modules/auth"
	"student_dormitory/internal/platform/files"
)

type reviewFixture struct {
	s      *Service
	review *ReviewService
	docs   *DocumentService
	store  *files.Local
	db     *sql.DB
	ctx    context.Context
}

func workflow(t *testing.T) *reviewFixture {
	t.Helper()
	s, db, ctx := fixture(t)
	for id, role := range map[string]string{"reviewer": "registry_staff", "reviewer2": "registry_staff", "coordinator": "administration"} {
		if _, err := db.ExecContext(ctx, "INSERT INTO auth_users(id,display_name) VALUES (?,?)", id, id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO auth_user_roles VALUES (?,?)", id, role); err != nil {
			t.Fatal(err)
		}
	}
	store, err := files.NewDocumentLocal(filepath.Join(t.TempDir(), "documents"))
	if err != nil {
		t.Fatal(err)
	}
	directory := auth.NewService(auth.NewRepository(db), s.audit, auth.Options{})
	return &reviewFixture{s, NewReviewService(s, directory), NewDocumentService(s, store), store, db, ctx}
}
func testPDF() []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] >>"}
	offsets := []int{0}
	for i, obj := range objects {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 4\n0000000000 65535 f \n")
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size 4 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref)
	return b.Bytes()
}
func (w *reviewFixture) current(t *testing.T, id string) Application {
	t.Helper()
	a, _, err := w.s.Detail(w.ctx, "one", id)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func (w *reviewFixture) upload(t *testing.T, a Application, document string) Document {
	t.Helper()
	d, err := w.docs.Upload(w.ctx, "one", a.ID, document, "Справка", "evidence.pdf", a.Version, bytes.NewReader(testPDF()))
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func (w *reviewFixture) submitted(t *testing.T, a Application) Application {
	t.Helper()
	a = w.current(t, a.ID)
	out, err := w.s.Submit(w.ctx, "one", a.ID, a.SubmitKey, ConsentVersion, true, a.Version)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func (w *reviewFixture) assigned(t *testing.T, a Application) Application {
	t.Helper()
	a = w.submitted(t, a)
	if err := w.review.Assign(w.ctx, "coordinator", a.ID, "reviewer", a.Version); err != nil {
		t.Fatal(err)
	}
	return w.current(t, a.ID)
}
func TestManualReviewCorrectionsVersionsAndNotifications(t *testing.T) {
	w := workflow(t)
	a := ready(t, w.s, w.ctx)
	d1 := w.upload(t, a, "")
	a = w.assigned(t, a)
	registration, date := a.Number(), a.SubmittedAt
	if err := w.review.Review(w.ctx, "reviewer", a.ID, "needs_information", "Дополните справку <script>private-review</script>", false, a.Version); err != nil {
		t.Fatal(err)
	}
	if err := w.review.Review(w.ctx, "reviewer", a.ID, "needs_information", "repeat", false, a.Version); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate review", err)
	}
	a = w.current(t, a.ID)
	changed := valid()
	changed.Group = "Исправленная группа"
	if err := w.s.Edit(w.ctx, "one", a.ID, changed, a.Version); err != nil {
		t.Fatal(err)
	}
	a = w.current(t, a.ID)
	d2 := w.upload(t, a, d1.DocumentID)
	if d2.Version != 2 {
		t.Fatal("new version not created")
	}
	a = w.current(t, a.ID)
	inputVersion := a.Version
	a = w.submitted(t, a)
	if a.Status != UnderReview || a.Number() != registration || a.SubmittedAt != date {
		t.Fatal("resubmission changed registration", a)
	}
	if _, err := w.s.Submit(w.ctx, "one", a.ID, a.SubmitKey, ConsentVersion, true, inputVersion); err != nil {
		t.Fatal("repeat correction", err)
	}
	if err := w.review.Review(w.ctx, "reviewer", a.ID, "completed", "Проверено вручную", false, a.Version); !errors.Is(err, ErrValidation) {
		t.Fatal("unconfirmed review", err)
	}
	if err := w.review.Review(w.ctx, "reviewer", a.ID, "completed", "Проверено вручную", true, a.Version); err != nil {
		t.Fatal(err)
	}
	data, err := w.s.Workspace(w.ctx, "one", a.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if data.Application.Status != Verified || !data.Checks.Name || !data.Checks.Email || !data.Checks.Group || !data.Checks.Request || len(data.Reviews) != 2 {
		t.Fatal("not checked", data.Checks)
	}
	checked := map[string]bool{}
	for _, d := range data.Documents {
		checked[d.ID] = d.Checked
	}
	if !checked[d2.ID] || checked[d1.ID] {
		t.Fatal("wrong document checked")
	}
	for _, d := range []Document{d1, d2} {
		_, r, err := w.docs.Open(w.ctx, "one", a.ID, d.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(r)
		r.Close()
		if err != nil || !bytes.Equal(b, testPDF()) {
			t.Fatal("document bytes changed")
		}
	}
	a = data.Application
	changed.Email = "corrected@example.test"
	if err := w.s.Edit(w.ctx, "one", a.ID, changed, a.Version); err != nil {
		t.Fatal(err)
	}
	data, err = w.s.Workspace(w.ctx, "one", a.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if data.Application.Status != NeedsInformation || data.Checks.Email || !data.Checks.Group || !data.Checks.Name || !data.Checks.Request {
		t.Fatal("changed evidence still checked", data.Checks)
	}
	if err := w.review.Review(w.ctx, "reviewer", a.ID, "completed", "without submission", true, data.Application.Version); !errors.Is(err, ErrConflict) {
		t.Fatal("review without resubmission", err)
	}
	oldSnapshot := false
	for _, snap := range data.Snapshots {
		if snap.Fields.Email.Value == valid().Email && snap.Fields.Group.Value == changed.Group {
			oldSnapshot = true
		}
	}
	if !oldSnapshot {
		t.Fatal("old fields lost")
	}
	a = w.submitted(t, data.Application)
	if err := w.review.Review(w.ctx, "reviewer", a.ID, "completed", "Повторно проверено", true, a.Version); err != nil {
		t.Fatal(err)
	}
	a = w.current(t, a.ID)
	d3 := w.upload(t, a, d1.DocumentID)
	if d3.Version != 3 {
		t.Fatal("replacement version")
	}
	data, _ = w.s.Workspace(w.ctx, "one", a.ID, false)
	if data.Application.Status != NeedsInformation || !data.Checks.Email {
		t.Fatal("file replacement changed unrelated fields")
	}
	for _, d := range data.Documents {
		if d.Current && d.Checked {
			t.Fatal("new file inherited review")
		}
	}
	notifications, err := w.s.Notifications(w.ctx, "one")
	if err != nil || len(notifications) != 4 {
		t.Fatal("notification duplication", len(notifications), err)
	}
	if err := w.s.ReadNotification(w.ctx, "two", notifications[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign notification read", err)
	}
	for i := 0; i < 2; i++ {
		if err := w.s.ReadNotification(w.ctx, "one", notifications[0].ID); err != nil {
			t.Fatal(err)
		}
	}
	notifications, _ = w.s.Notifications(w.ctx, "one")
	if notifications[0].ReadAt == "" {
		t.Fatal("read marker missing")
	}
	if count(t, w.db, "SELECT count(*) FROM audit_events WHERE action='applications.notification_read'") != 1 {
		t.Fatal("duplicate read audit")
	}
	if count(t, w.db, "SELECT count(*) FROM application_registrations") != 1 || count(t, w.db, "SELECT count(*) FROM application_submissions") != 3 {
		t.Fatal("duplicate registration/submission")
	}
	for _, table := range []string{"application_reviews", "application_document_versions", "application_history_details"} {
		if _, err := w.db.ExecContext(w.ctx, "DELETE FROM "+table); err == nil {
			t.Fatal("mutable evidence", table)
		}
	}
}
func TestReviewAssignmentAndFileIsolation(t *testing.T) {
	w := workflow(t)
	a := ready(t, w.s, w.ctx)
	d := w.upload(t, a, "")
	if _, r, err := w.docs.Open(w.ctx, "reviewer", a.ID, d.ID, true); !errors.Is(err, ErrNotFound) || r != nil {
		t.Fatal("unassigned read", err)
	}
	a = w.assigned(t, a)
	for _, actor := range []string{"reviewer2", "one", "two", "admin", "coordinator"} {
		if _, err := w.s.Workspace(w.ctx, actor, a.ID, true); err == nil {
			t.Fatal("foreign workspace", actor)
		}
		if _, r, err := w.docs.Open(w.ctx, actor, a.ID, d.ID, true); err == nil || r != nil {
			t.Fatal("foreign staff file", actor)
		}
	}
	for _, actor := range []string{"two", "reviewer", "admin", "coordinator"} {
		if _, r, err := w.docs.Open(w.ctx, actor, a.ID, d.ID, false); err == nil || r != nil {
			t.Fatal("foreign owner file", actor)
		}
	}
	b, err := w.s.Create(w.ctx, "two")
	if err != nil {
		t.Fatal(err)
	}
	if _, r, err := w.docs.Open(w.ctx, "two", b.ID, d.ID, false); !errors.Is(err, ErrNotFound) || r != nil {
		t.Fatal("mixed file target", err)
	}
	if _, err := w.docs.Upload(w.ctx, "two", b.ID, d.DocumentID, "Справка", "doc.pdf", b.Version, bytes.NewReader(testPDF())); !errors.Is(err, ErrNotFound) {
		t.Fatal("mixed document parent", err)
	}
	for _, actor := range []string{"one", "reviewer", "admin"} {
		if err := w.review.Assign(w.ctx, actor, a.ID, "reviewer2", a.Version); !errors.Is(err, auth.ErrForbidden) {
			t.Fatal("assignment bypass", actor, err)
		}
	}
	if err := w.review.Assign(w.ctx, "coordinator", a.ID, "two", a.Version); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("ineligible reviewer", err)
	}
	if err := w.review.Assign(w.ctx, "coordinator", a.ID, "reviewer2", a.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Workspace(w.ctx, "reviewer", a.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatal("old assignment still works", err)
	}
	if _, r, err := w.docs.Open(w.ctx, "reviewer", a.ID, d.ID, true); !errors.Is(err, ErrNotFound) || r != nil {
		t.Fatal("old reviewer file", err)
	}
	_, r, err := w.docs.Open(w.ctx, "reviewer2", a.ID, d.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	assigned, candidates, err := w.review.Assignments(w.ctx, "coordinator")
	if err != nil || len(assigned) != 1 || len(candidates) != 2 || assigned[0].Fields.Name.Value != "" {
		t.Fatal("assignment list leaks fields", err)
	}
	list, _ := w.review.List(w.ctx, "reviewer")
	if len(list) != 0 {
		t.Fatal("foreign assigned list")
	}
	list, _ = w.review.List(w.ctx, "reviewer2")
	if len(list) != 1 {
		t.Fatal("missing assigned list")
	}
	if _, err := w.db.ExecContext(w.ctx, "DELETE FROM auth_user_roles WHERE user_id='reviewer2'"); err != nil {
		t.Fatal(err)
	}
	if _, r, err := w.docs.Open(w.ctx, "reviewer2", a.ID, d.ID, true); !errors.Is(err, auth.ErrForbidden) || r != nil {
		t.Fatal("revoked reviewer", err)
	}
}
func TestDocumentFormatAndSizeValidation(t *testing.T) {
	var pngData, jpegData bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	png.Encode(&pngData, img)
	jpeg.Encode(&jpegData, img, nil)
	for _, tt := range []struct {
		name  string
		data  []byte
		valid bool
	}{{"a.pdf", testPDF(), true}, {"a.PNG", pngData.Bytes(), true}, {"a.jpeg", jpegData.Bytes(), true}, {"a.pdf", pngData.Bytes(), false}, {"a.png", jpegData.Bytes(), false}, {"a.svg", []byte("<svg/>"), false}, {"a.jpg", []byte("broken"), false}, {"a.pdf", []byte("%PDF-1.4\nnot-a-pdf\n%%EOF"), false}, {"a.pdf", testPDF()[:len(testPDF())-10], false}, {"a.png", nil, false}, {"a.png", bytes.Repeat([]byte{1}, int(MaxDocumentBytes)+1), false}} {
		_, _, _, err := validateDocument(bytes.NewReader(tt.data), tt.name)
		if (err == nil) != tt.valid {
			t.Fatal(tt.name, tt.valid, err)
		}
	}
	var huge bytes.Buffer
	png.Encode(&huge, image.NewRGBA(image.Rect(0, 0, 4097, 1)))
	if _, _, _, err := validateDocument(&huge, "huge.png"); !errors.Is(err, ErrDocument) {
		t.Fatal("image bound", err)
	}
}
func TestReviewNotificationAndUploadRollback(t *testing.T) {
	w := workflow(t)
	a := ready(t, w.s, w.ctx)
	if _, err := w.db.ExecContext(w.ctx, "CREATE TRIGGER reject_notify BEFORE INSERT ON application_notifications BEGIN SELECT RAISE(ABORT,'failure'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Submit(w.ctx, "one", a.ID, a.SubmitKey, ConsentVersion, true, a.Version); err == nil {
		t.Fatal("submission without notification")
	}
	if w.current(t, a.ID).Status != Draft || count(t, w.db, "SELECT count(*) FROM application_registrations") != 0 {
		t.Fatal("partial submission")
	}
	w.db.ExecContext(w.ctx, "DROP TRIGGER reject_notify")
	for _, table := range []string{"application_document_versions", "application_history_details", "audit_events"} {
		if _, err := w.db.ExecContext(w.ctx, "CREATE TRIGGER reject_upload BEFORE INSERT ON "+table+" BEGIN SELECT RAISE(ABORT,'failure'); END"); err != nil {
			t.Fatal(err)
		}
		if _, err := w.docs.Upload(w.ctx, "one", a.ID, "", "Справка", "doc.pdf", a.Version, bytes.NewReader(testPDF())); err == nil {
			t.Fatal("upload without mandatory record", table)
		}
		keys, err := w.store.Keys(w.ctx)
		if err != nil || len(keys) != 0 {
			t.Fatal("orphan not swept", keys, err)
		}
		if w.current(t, a.ID).Version != a.Version || count(t, w.db, "SELECT count(*) FROM application_documents") != 0 {
			t.Fatal("partial upload")
		}
		w.db.ExecContext(w.ctx, "DROP TRIGGER reject_upload")
	}
	a = w.assigned(t, a)
	for _, table := range []string{"application_reviews", "application_history_details", "application_notifications", "audit_events"} {
		if _, err := w.db.ExecContext(w.ctx, "CREATE TRIGGER reject_review BEFORE INSERT ON "+table+" BEGIN SELECT RAISE(ABORT,'failure'); END"); err != nil {
			t.Fatal(err)
		}
		if err := w.review.Review(w.ctx, "reviewer", a.ID, "completed", "private comment", true, a.Version); err == nil {
			t.Fatal("review without mandatory record", table)
		}
		if current := w.current(t, a.ID); current.Status != UnderReview || current.Version != a.Version {
			t.Fatal("partial review")
		}
		if count(t, w.db, "SELECT count(*) FROM application_reviews") != 0 || count(t, w.db, "SELECT count(*) FROM application_notifications") != 1 {
			t.Fatal("partial notification")
		}
		w.db.ExecContext(w.ctx, "DROP TRIGGER reject_review")
	}
	var auditPayload string
	if err := w.db.QueryRowContext(w.ctx, "SELECT group_concat(changes_json) FROM audit_events").Scan(&auditPayload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(auditPayload, "private comment") || strings.Contains(auditPayload, valid().Email) {
		t.Fatal("private audit payload")
	}
	d := w.uploadDraftForCleanup(t)
	if err := w.store.Delete(w.ctx, d.StorageKey); err != nil {
		t.Fatal(err)
	}
	if err := w.docs.Reconcile(w.ctx); err == nil {
		t.Fatal("missing referenced file not detected")
	}
}
func (w *reviewFixture) uploadDraftForCleanup(t *testing.T) Document {
	a := ready(t, w.s, w.ctx)
	return w.upload(t, a, "")
}
