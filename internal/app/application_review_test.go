package app

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"student_dormitory/internal/modules/applications"
	"student_dormitory/internal/modules/audit"
	"student_dormitory/internal/modules/auth"
	"student_dormitory/internal/platform/backup"
	"student_dormitory/internal/platform/requestid"
)

func documentPDF() []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := []int{}
	for i, obj := range []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] >>"} {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := b.Len()
	b.WriteString("xref\n0 4\n0000000000 65535 f \n")
	for _, v := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", v)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size 4 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref)
	return b.Bytes()
}
func documentRequest(t *testing.T, f *authFixture, cookie *http.Cookie, path string, values url.Values, filename string, data []byte, origin string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for key, items := range values {
		for _, v := range items {
			if err := form.WriteField(key, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	part, err := form.CreateFormFile("document", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "http://localhost:8080"+path, &body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(w, r)
	return w
}
func newApplicationHTTP(t *testing.T, f *authFixture, cookie *http.Cookie) string {
	t.Helper()
	page := perform(f.app.Handler(), "GET", "/applications", cookie, nil, "")
	created := perform(f.app.Handler(), "POST", "/applications/new", cookie, url.Values{"csrf_token": {csrf(t, page)}}, "")
	if created.Code != 303 {
		t.Fatal(created.Code)
	}
	path := strings.TrimSuffix(created.Header().Get("Location"), "/edit")
	page = perform(f.app.Handler(), "GET", path+"/edit", cookie, nil, "")
	form := url.Values{"csrf_token": {csrf(t, page)}, "version": {applicationHidden(t, page.Body.String(), "version")}, "name": {"Документы Студента"}, "email": {"private-docs@example.test"}, "group": {"Группа 2026"}, "request": {"Прошу общежитие"}}
	if w := perform(f.app.Handler(), "POST", path+"/edit", cookie, form, ""); w.Code != 303 {
		t.Fatal(w.Code)
	}
	return path
}
func appSubmission(t *testing.T, page *httptest.ResponseRecorder) url.Values {
	t.Helper()
	return url.Values{"csrf_token": {csrf(t, page)}, "version": {applicationHidden(t, page.Body.String(), "version")}, "submit_key": {applicationHidden(t, page.Body.String(), "submit_key")}, "consent_version": {applicationHidden(t, page.Body.String(), "consent_version")}, "accept_consent": {"on"}}
}
func workflowWorkspace(t *testing.T, f *authFixture, cookie *http.Cookie, path string) applications.Workspace {
	t.Helper()
	session, err := f.service.Session(context.Background(), cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	s := applications.NewService(applications.NewRepository(f.app.db), f.service, audit.NewRecorder(f.app.db), f.service)
	data, err := s.Workspace(context.Background(), session.UserID, strings.TrimPrefix(path, "/applications/"), false)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func fixtureReviewer(t *testing.T, f *authFixture, model *http.Cookie) *http.Cookie {
	t.Helper()
	ctx := requestid.WithID(context.Background(), "reviewer-fixture")
	for _, q := range []string{`INSERT INTO auth_users(id,display_name) VALUES ('other-reviewer','Посторонний проверяющий')`, `INSERT INTO auth_user_roles VALUES ('other-reviewer','registry_staff')`, `INSERT INTO auth_external_identities VALUES ('fixture','other-reviewer','other-reviewer')`} {
		if _, err := f.app.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	_, anon, err := f.service.Anonymous(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := f.service.Login(ctx, anon, fixtureVerifier{auth.Identity{Provider: "fixture", Subject: "other-reviewer"}})
	if err != nil {
		t.Fatal(err)
	}
	c := *model
	c.Value = token
	return &c
}
func TestManualApplicationReviewHTTPFullCycleAndFiles(t *testing.T) {
	f := newAuthFixture(t)
	student, _ := login(t, f, "student")
	other := secondStudent(t, f, student)
	reviewer, _ := login(t, f, "registry_staff")
	outsider := fixtureReviewer(t, f, reviewer)
	coordinator, _ := login(t, f, "administration")
	admin, _ := login(t, f, "system_admin")
	path := newApplicationHTTP(t, f, student)
	id := strings.TrimPrefix(path, "/applications/")
	page := perform(f.app.Handler(), "GET", path, student, nil, "")
	staleSubmit := appSubmission(t, page)
	upload := url.Values{"csrf_token": {csrf(t, page)}, "version": {applicationHidden(t, page.Body.String(), "version")}, "label": {"Справка <script>PRIVATE-DOC-LABEL</script>"}}
	for _, tt := range []struct {
		token, origin, filename string
		data                    []byte
		want                    int
	}{{"", "", "a.pdf", documentPDF(), 403}, {csrf(t, page), "https://attacker.invalid", "a.pdf", documentPDF(), 403}, {csrf(t, page), "", "a.png", documentPDF(), 422}, {csrf(t, page), "", "a.pdf", bytes.Repeat([]byte{1}, 11<<20), 413}} {
		form := url.Values{}
		for k, v := range upload {
			form[k] = append([]string(nil), v...)
		}
		form.Set("csrf_token", tt.token)
		w := documentRequest(t, f, student, path+"/documents", form, tt.filename, tt.data, tt.origin)
		if w.Code != tt.want {
			t.Fatal("upload validation", w.Code, tt.want)
		}
	}
	if w := documentRequest(t, f, student, path+"/documents", upload, "../../private.pdf", documentPDF(), ""); w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	data := workflowWorkspace(t, f, student, path)
	doc1 := data.Documents[0]
	var p, j bytes.Buffer
	png.Encode(&p, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	jpeg.Encode(&j, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil)
	for _, tt := range []struct {
		name string
		data []byte
	}{{"photo.png", p.Bytes()}, {"photo.jpg", j.Bytes()}} {
		page = perform(f.app.Handler(), "GET", path, student, nil, "")
		upload.Set("version", applicationHidden(t, page.Body.String(), "version"))
		upload.Set("label", tt.name)
		if w := documentRequest(t, f, student, path+"/documents", upload, tt.name, tt.data, ""); w.Code != 303 {
			t.Fatal(w.Code)
		}
	}
	if w := perform(f.app.Handler(), "POST", path+"/submit", student, staleSubmit, ""); w.Code != 409 {
		t.Fatal("stale send", w.Code)
	}
	for _, method := range []string{"GET", "HEAD"} {
		for _, actor := range []*http.Cookie{nil, other, reviewer, outsider, coordinator, admin} {
			w := perform(f.app.Handler(), method, doc1.URL(), actor, nil, "")
			want := 403
			if actor == nil {
				want = 401
			}
			if actor == other {
				want = 404
			}
			if w.Code != want {
				t.Fatal("owner file isolation", method, w.Code, want)
			}
		}
		w := perform(f.app.Handler(), method, doc1.URL(), student, nil, "")
		if w.Code != 200 || w.Header().Get("Content-Disposition") != `attachment; filename="document.pdf"` || w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("unsafe delivery", w.Code)
		}
		if method == "GET" && !bytes.Equal(w.Body.Bytes(), documentPDF()) {
			t.Fatal("changed file")
		}
		if method == "HEAD" && w.Body.Len() != 0 {
			t.Fatal("head body")
		}
	}
	for _, public := range []string{"/documents/" + doc1.StorageKey, "/static/documents/" + doc1.StorageKey, "/photos/" + doc1.ID} {
		if w := perform(f.app.Handler(), "GET", public, nil, nil, ""); w.Code != 404 {
			t.Fatal("public document", w.Code)
		}
	}
	ownOther := newApplicationHTTP(t, f, other)
	if w := perform(f.app.Handler(), "GET", ownOther+"/documents/"+doc1.ID, other, nil, ""); w.Code != 404 {
		t.Fatal("mixed app file", w.Code)
	}
	page = perform(f.app.Handler(), "GET", path, student, nil, "")
	submission := appSubmission(t, page)
	for i := 0; i < 2; i++ {
		if w := perform(f.app.Handler(), "POST", path+"/submit", student, submission, ""); w.Code != 303 {
			t.Fatal(w.Code)
		}
	}
	if w := perform(f.app.Handler(), "GET", "/staff"+path, reviewer, nil, ""); w.Code != 404 {
		t.Fatal("unassigned detail", w.Code)
	}
	assignPage := perform(f.app.Handler(), "GET", "/staff/application-assignments", coordinator, nil, "")
	if assignPage.Code != 200 || strings.Contains(assignPage.Body.String(), "private-docs@example.test") || strings.Contains(assignPage.Body.String(), doc1.Label) {
		t.Fatal("routing list privacy")
	}
	assign := url.Values{"csrf_token": {csrf(t, assignPage)}, "version": {applicationHidden(t, assignPage.Body.String(), "version")}, "reviewer_id": {f.credentials["registry_staff"].UserID}}
	if w := perform(f.app.Handler(), "POST", "/staff/application-assignments/"+id, coordinator, assign, ""); w.Code != 303 {
		t.Fatal(w.Code)
	}
	for _, actor := range []*http.Cookie{outsider, coordinator, admin, student, other} {
		for _, suffix := range []string{"", "/documents/" + doc1.ID} {
			w := perform(f.app.Handler(), "GET", "/staff"+path+suffix, actor, nil, "")
			want := 403
			if actor == outsider {
				want = 404
			}
			if w.Code != want {
				t.Fatal("staff isolation", w.Code, want)
			}
		}
	}
	staffPage := perform(f.app.Handler(), "GET", "/staff"+path, reviewer, nil, "")
	if staffPage.Code != 200 || strings.Contains(staffPage.Body.String(), "<script>PRIVATE-DOC-LABEL</script>") {
		t.Fatal("reviewer detail/XSS")
	}
	request := url.Values{"csrf_token": {csrf(t, staffPage)}, "version": {applicationHidden(t, staffPage.Body.String(), "version")}, "result": {"needs_information"}, "comment": {"PRIVATE-REVIEW-COMMENT <script>исправить справку</script>"}}
	if w := perform(f.app.Handler(), "POST", "/staff"+path+"/review", reviewer, request, ""); w.Code != 303 {
		t.Fatal(w.Code)
	}
	if w := perform(f.app.Handler(), "POST", "/staff"+path+"/review", reviewer, request, ""); w.Code != 409 {
		t.Fatal("duplicate review", w.Code)
	}
	edit := perform(f.app.Handler(), "GET", path+"/edit", student, nil, "")
	correction := url.Values{"csrf_token": {csrf(t, edit)}, "version": {applicationHidden(t, edit.Body.String(), "version")}, "name": {"Документы Студента"}, "email": {"private-docs@example.test"}, "group": {"Группа исправлена"}, "request": {"Прошу общежитие"}}
	if w := perform(f.app.Handler(), "POST", path+"/edit", student, correction, ""); w.Code != 303 {
		t.Fatal(w.Code)
	}
	page = perform(f.app.Handler(), "GET", path, student, nil, "")
	upload.Set("version", applicationHidden(t, page.Body.String(), "version"))
	upload.Set("document_id", doc1.DocumentID)
	if w := documentRequest(t, f, student, path+"/documents", upload, "corrected.pdf", documentPDF(), ""); w.Code != 303 {
		t.Fatal(w.Code)
	}
	page = perform(f.app.Handler(), "GET", path, student, nil, "")
	submission = appSubmission(t, page)
	for i := 0; i < 2; i++ {
		if w := perform(f.app.Handler(), "POST", path+"/submit", student, submission, ""); w.Code != 303 {
			t.Fatal("resubmit", w.Code)
		}
	}
	staffPage = perform(f.app.Handler(), "GET", "/staff"+path, reviewer, nil, "")
	result := url.Values{"csrf_token": {csrf(t, staffPage)}, "version": {applicationHidden(t, staffPage.Body.String(), "version")}, "result": {"completed"}, "comment": {"Завершена ручная проверка"}, "confirm_review": {"on"}}
	if w := perform(f.app.Handler(), "POST", "/staff"+path+"/review", reviewer, result, ""); w.Code != 303 {
		t.Fatal(w.Code)
	}
	data = workflowWorkspace(t, f, student, path)
	if data.Application.Status != applications.Verified || len(data.Documents) != 4 || len(data.Reviews) != 2 || !data.Checks.Group {
		t.Fatal("incomplete workflow")
	}
	if w := perform(f.app.Handler(), "GET", "/staff"+doc1.URL(), reviewer, nil, ""); w.Code != 200 {
		t.Fatal("old version lost", w.Code)
	}
	notices := perform(f.app.Handler(), "GET", "/account/notifications", student, nil, "")
	if notices.Code != 200 || !strings.Contains(notices.Body.String(), "Заявление зарегистрировано") || !strings.Contains(notices.Body.String(), "Запрошено дополнение") || !strings.Contains(notices.Body.String(), "Ручная проверка завершена") {
		t.Fatal("missing notifications")
	}
	var noticeID int64
	var noticeCount int
	if err := f.app.db.QueryRowContext(context.Background(), "SELECT min(id),count(*) FROM application_notifications WHERE application_id=?", id).Scan(&noticeID, &noticeCount); err != nil || noticeCount != 3 {
		t.Fatal("notification duplication", err, noticeCount)
	}
	readPath := "/account/notifications/" + strconv.FormatInt(noticeID, 10) + "/read"
	otherPage := perform(f.app.Handler(), "GET", "/applications", other, nil, "")
	if w := perform(f.app.Handler(), "POST", readPath, other, url.Values{"csrf_token": {csrf(t, otherPage)}}, ""); w.Code != 404 {
		t.Fatal("foreign read marker", w.Code)
	}
	for i := 0; i < 2; i++ {
		if w := perform(f.app.Handler(), "POST", readPath, student, url.Values{"csrf_token": {csrf(t, notices)}}, ""); w.Code != 303 {
			t.Fatal("read marker", w.Code)
		}
	}
	after := perform(f.app.Handler(), "GET", "/account/notifications", student, nil, "")
	if !strings.Contains(after.Body.String(), "Прочитано") {
		t.Fatal("read state lost")
	}
	for _, secret := range []string{"private-docs@example.test", "PRIVATE-DOC-LABEL", "PRIVATE-REVIEW-COMMENT", string(documentPDF())} {
		if strings.Contains(f.logs.String(), secret) {
			t.Fatal("private logs")
		}
	}
}
func TestApplicationDocumentsBackupRestore(t *testing.T) {
	f := newAuthFixture(t)
	student, _ := login(t, f, "student")
	path := newApplicationHTTP(t, f, student)
	page := perform(f.app.Handler(), "GET", path, student, nil, "")
	form := url.Values{"csrf_token": {csrf(t, page)}, "version": {applicationHidden(t, page.Body.String(), "version")}, "label": {"Резервируемый документ"}}
	if w := documentRequest(t, f, student, path+"/documents", form, "doc.pdf", documentPDF(), ""); w.Code != 303 {
		t.Fatal(w.Code)
	}
	data := workflowWorkspace(t, f, student, path)
	d1 := data.Documents[0]
	form.Set("version", strconv.FormatInt(data.Application.Version, 10))
	form.Set("document_id", d1.DocumentID)
	if w := documentRequest(t, f, student, path+"/documents", form, "doc.pdf", documentPDF(), ""); w.Code != 303 {
		t.Fatal(w.Code)
	}
	cfg := f.app.config
	if err := f.app.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	bundle := filepath.Join(t.TempDir(), "bundle")
	manifest, err := backup.Create(ctx, cfg.DatabasePath, cfg.FilesDir, bundle, BackupReferences)
	if err != nil || len(manifest.Assets) != 2 {
		t.Fatal("missing document backup", err, len(manifest.Assets))
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if _, err := backup.Restore(ctx, bundle, restored, BackupReferences); err != nil {
		t.Fatal(err)
	}
	cfg.DatabasePath = filepath.Join(restored, "dormitory.db")
	cfg.FilesDir = filepath.Join(restored, "files")
	cfg.DevUsersFile = filepath.Join(restored, "dev-users.txt")
	a, err := New(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if w := perform(a.Handler(), "GET", d1.URL(), student, nil, ""); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), documentPDF()) {
		t.Fatal("restored document unavailable", w.Code)
	}
	if _, err := os.Stat(filepath.Join(cfg.FilesDir, "documents", d1.StorageKey)); err != nil {
		t.Fatal(err)
	}
}

func TestApplicationWorkflowRouteSecurityAndKazakh(t *testing.T) {
	f := newAuthFixture(t)
	for _, role := range []string{"student", "registry_staff", "commandant", "administration", "system_admin"} {
		cookie, _ := login(t, f, role)
		account := perform(f.app.Handler(), "GET", "/account", cookie, nil, "")
		token := csrf(t, account)
		for path, allowed := range map[string]bool{"/staff/applications": role == "registry_staff", "/staff/application-assignments": role == "administration", "/account/notifications": true} {
			w := perform(f.app.Handler(), "GET", path, cookie, nil, "")
			want := 403
			if allowed {
				want = 200
			}
			if w.Code != want {
				t.Fatal(role, path, w.Code, want)
			}
		}
		for _, path := range []string{"/staff/application-assignments/unknown", "/staff/applications/unknown/review", "/account/notifications/1/read", "/applications/unknown/documents"} {
			if w := perform(f.app.Handler(), "GET", path, cookie, nil, ""); w.Code != 405 {
				t.Fatal("mutating GET", path, w.Code)
			}
			if w := perform(f.app.Handler(), "POST", path, cookie, nil, ""); w.Code != 403 {
				t.Fatal("missing CSRF", path, w.Code)
			}
			if w := perform(f.app.Handler(), "POST", path, cookie, url.Values{"csrf_token": {token}}, "https://attacker.invalid"); w.Code != 403 {
				t.Fatal("cross-origin", path, w.Code)
			}
		}
		kkPath := "/account/notifications?lang=kk"
		expected := "Хабарламалар"
		if role == "registry_staff" {
			kkPath = "/staff/applications?lang=kk"
			expected = "Тағайындалған өтініштер"
		}
		if role == "administration" {
			kkPath = "/staff/application-assignments?lang=kk"
			expected = "Тексерушіні тағайындау"
		}
		if role == "student" || role == "registry_staff" || role == "administration" {
			w := perform(f.app.Handler(), "GET", kkPath, cookie, nil, "")
			if w.Code != 200 || !strings.Contains(w.Body.String(), expected) {
				t.Fatal("missing Kazakh workflow page", role, w.Code)
			}
		}
	}
	for _, path := range []string{"/staff/applications", "/staff/application-assignments", "/account/notifications", "/applications/unknown/documents/unknown", "/staff/applications/unknown/documents/unknown"} {
		if w := perform(f.app.Handler(), "GET", path, nil, nil, ""); w.Code != 401 {
			t.Fatal("anonymous workflow", path, w.Code)
		}
	}
}
