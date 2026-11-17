package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"image"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"student_dormitory/internal/platform/config"
	"student_dormitory/internal/platform/files"
	"testing"
	"time"
)

func photoRequest(t *testing.T, f *authFixture, cookie *http.Cookie, path, token string, data []byte, origin string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for name, value := range map[string]string{"csrf_token": token, "position": "3", "description": "<script>PRIVATE-CAPTION</script>", "user_id": "attacker", "role": "system_admin"} {
		if err := form.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	file, err := form.CreateFormFile("photo", "../../dangerous.php")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(data); err != nil {
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
func TestPhotosHTTPFormsPermissionsPublicationAndCSRF(t *testing.T) {
	f := newAuthFixture(t)
	cookie, _ := login(t, f, "registry_staff")
	root := "/staff/dormitories/dev-dorm-1"
	page := perform(f.app.Handler(), "GET", root, cookie, nil, "")
	token := csrf(t, page)
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 4, 3))); err != nil {
		t.Fatal(err)
	}
	data := imageBytes.Bytes()
	for _, test := range []struct {
		token, origin string
		want          int
	}{{"", "", 403}, {token, "https://attacker.invalid", 403}, {token, "", 303}} {
		w := photoRequest(t, f, cookie, root+"/photos", test.token, data, test.origin)
		if w.Code != test.want {
			t.Fatalf("upload status %d want %d: %s", w.Code, test.want, w.Body.String())
		}
	}
	var id, key, actor string
	if err := f.app.db.QueryRowContext(context.Background(), "SELECT id,storage_key FROM inventory_photos").Scan(&id, &key); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(key, "dangerous") || strings.Contains(key, "/") || !strings.HasSuffix(key, ".png") {
		t.Fatal("original name trusted")
	}
	if err := f.app.db.QueryRowContext(context.Background(), "SELECT actor_id FROM audit_events WHERE object_id=?", id).Scan(&actor); err != nil || actor != f.credentials["registry_staff"].UserID {
		t.Fatal("untrusted author")
	}
	page = perform(f.app.Handler(), "GET", root, cookie, nil, "")
	if page.Code != 200 || strings.Contains(page.Body.String(), "<script>PRIVATE-CAPTION") || !strings.Contains(page.Body.String(), "&lt;script&gt;PRIVATE-CAPTION") || !strings.Contains(page.Body.String(), "/staff/photos/"+id) {
		t.Fatal("unsafe or absent gallery")
	}
	for _, method := range []string{"GET", "HEAD"} {
		if w := perform(f.app.Handler(), method, "/photos/"+id, nil, nil, ""); w.Code != 404 {
			t.Fatal("unpublished photo public")
		}
	}
	if _, err := f.app.db.ExecContext(context.Background(), "UPDATE inventory_dormitories SET published=1"); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "HEAD"} {
		w := perform(f.app.Handler(), method, "/photos/"+id, nil, nil, "")
		if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
			t.Fatalf("unsafe public image response: %d %v", w.Code, w.Header())
		}
		if method == "HEAD" && w.Body.Len() != 0 {
			t.Fatal("HEAD has body")
		}
		if method == "GET" {
			if _, err := png.Decode(bytes.NewReader(w.Body.Bytes())); err != nil {
				t.Fatal("image response corrupted")
			}
		}
	}
	for _, path := range []string{"/static/" + key, "/files/photos/" + key} {
		if w := perform(f.app.Handler(), "GET", path, nil, nil, ""); w.Code != 404 {
			t.Fatal("private directory mounted")
		}
	}
	if w := photoRequest(t, f, cookie, root+"/photos", token, []byte("<svg onload='alert(1)'></svg>"), ""); w.Code != 422 {
		t.Fatal("fake image accepted")
	}
	if w := photoRequest(t, f, cookie, root+"/photos", token, bytes.Repeat([]byte{1}, (5<<20)+1), ""); w.Code != 422 {
		t.Fatalf("large file status %d", w.Code)
	}
	if w := photoRequest(t, f, cookie, root+"/photos", token, bytes.Repeat([]byte{1}, 6<<20), ""); w.Code != 413 {
		t.Fatalf("large request status %d", w.Code)
	}
	if w := photoRequest(t, f, cookie, root+"/edit", token, data, ""); w.Code != 403 {
		t.Fatal("multipart enabled outside upload")
	}
	for _, role := range []string{"student", "commandant", "administration", "system_admin"} {
		roleCookie, _ := login(t, f, role)
		account := perform(f.app.Handler(), "GET", "/account", roleCookie, nil, "")
		roleToken := csrf(t, account)
		if w := photoRequest(t, f, roleCookie, root+"/photos", roleToken, data, ""); w.Code != 403 {
			t.Fatalf("role %s uploaded: %d", role, w.Code)
		}
		fields := url.Values{"csrf_token": {roleToken}, "version": {"1"}, "position": {"0"}}
		for _, action := range []string{"edit", "delete"} {
			if w := perform(f.app.Handler(), "POST", root+"/photos/"+id+"/"+action, roleCookie, fields, ""); w.Code != 403 {
				t.Fatalf("role %s %s status %d", role, action, w.Code)
			}
		}
		readWant := 200
		if role == "student" || role == "system_admin" {
			readWant = 403
		}
		if w := perform(f.app.Handler(), "GET", "/staff/photos/"+id, roleCookie, nil, ""); w.Code != readWant {
			t.Fatalf("private image %s %d", role, w.Code)
		}
	}
	if w := perform(f.app.Handler(), "GET", root+"/photos/"+id+"/delete", cookie, nil, ""); w.Code != 405 {
		t.Fatal("GET deletes photo")
	}
	if w := perform(f.app.Handler(), "POST", root+"/photos/"+id+"/delete", cookie, url.Values{"version": {"1"}}, ""); w.Code != 403 {
		t.Fatal("delete lacks csrf")
	}
	fields := url.Values{"csrf_token": {token}, "version": {"1"}, "position": {"0"}, "description": {"Updated"}, "primary": {"on"}}
	if w := perform(f.app.Handler(), "POST", root+"/photos/"+id+"/edit", cookie, fields, ""); w.Code != 303 {
		t.Fatalf("update %d", w.Code)
	}
	if w := perform(f.app.Handler(), "POST", root+"/photos/"+id+"/edit", cookie, fields, ""); w.Code != 409 {
		t.Fatal("stale edit accepted")
	}
	// Assignment is checked afresh on private bytes and all mutations.
	if _, err := f.app.db.ExecContext(context.Background(), "DELETE FROM auth_user_dormitories WHERE user_id=?", f.credentials["registry_staff"].UserID); err != nil {
		t.Fatal(err)
	}
	if w := perform(f.app.Handler(), "GET", "/staff/photos/"+id, cookie, nil, ""); w.Code != 403 {
		t.Fatal("revoked scope still reads")
	}
	if w := photoRequest(t, f, cookie, root+"/photos", token, data, ""); w.Code != 403 {
		t.Fatal("revoked scope still uploads")
	}
	if strings.Contains(f.logs.String(), "PRIVATE-CAPTION") || strings.Contains(f.logs.String(), "dangerous.php") {
		t.Fatal("user content in logs")
	}
}

func TestRoomPhotoHTTPUsesActualOwner(t *testing.T) {
	f := newAuthFixture(t)
	cookie, _ := login(t, f, "registry_staff")
	root := "/staff/dormitories/dev-dorm-1"
	building := createInventoryObject(t, f, cookie, root+"/buildings/new", url.Values{"name": {"A"}})
	floor := createInventoryObject(t, f, cookie, building+"/floors/new", url.Values{"number": {"1"}})
	room := createInventoryObject(t, f, cookie, floor+"/rooms/new", inventoryRoomFields())
	page := perform(f.app.Handler(), "GET", room, cookie, nil, "")
	token := csrf(t, page)
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	if w := photoRequest(t, f, cookie, room+"/photos", token, data.Bytes(), ""); w.Code != 303 {
		t.Fatal("room upload failed")
	}
	var id string
	if err := f.app.db.QueryRowContext(context.Background(), "SELECT id FROM inventory_photos").Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.db.ExecContext(context.Background(), "UPDATE inventory_dormitories SET published=1"); err != nil {
		t.Fatal(err)
	}
	if w := perform(f.app.Handler(), "GET", "/photos/"+id, nil, nil, ""); w.Code != 200 {
		t.Fatal("published room photo unavailable")
	}
	fields := url.Values{"csrf_token": {token}, "version": {"1"}}
	if w := perform(f.app.Handler(), "POST", root+"/photos/"+id+"/delete", cookie, fields, ""); w.Code != 404 {
		t.Fatal("foreign album accepted photo")
	}
	forged := strings.Replace(room, "/buildings/", "/buildings/wrong/floors/wrong/rooms/", 1)
	if w := photoRequest(t, f, cookie, forged+"/photos", token, data.Bytes(), ""); w.Code == 303 {
		t.Fatal("forged chain accepted")
	}
	if _, err := f.app.db.ExecContext(context.Background(), "UPDATE inventory_floors SET archived=1"); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "HEAD"} {
		if w := perform(f.app.Handler(), method, "/photos/"+id, nil, nil, ""); w.Code != 404 {
			t.Fatal("archived room ancestor still public")
		}
	}
	if _, err := f.app.db.ExecContext(context.Background(), "UPDATE inventory_floors SET archived=0"); err != nil {
		t.Fatal(err)
	}
	if w := perform(f.app.Handler(), "POST", room+"/photos/"+id+"/delete", cookie, fields, ""); w.Code != 303 {
		t.Fatal("room delete failed")
	}
	if w := perform(f.app.Handler(), "GET", "/photos/"+id, nil, nil, ""); w.Code != 404 {
		t.Fatal("deleted room photo exposed")
	}
}

func TestPhotoStartupSweepsInterruptedUpload(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{DatabasePath: filepath.Join(dir, "db.sqlite"), FilesDir: filepath.Join(dir, "files"), AppEnv: "development", SessionTTL: time.Hour}
	store, err := files.NewLocal(filepath.Join(cfg.FilesDir, "photos"))
	if err != nil {
		t.Fatal(err)
	}
	key := rand.Text() + rand.Text() + ".png"
	ctx := context.Background()
	if err := store.Put(ctx, key, []byte("interrupted upload")); err != nil {
		t.Fatal(err)
	}
	a, err := New(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := store.Open(ctx, key); !os.IsNotExist(err) {
		t.Fatal("startup did not remove unreferenced file")
	}
}
