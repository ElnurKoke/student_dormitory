package app

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"student_dormitory/internal/modules/audit"
	"student_dormitory/internal/modules/auth"
	"student_dormitory/internal/modules/inventory"
	"student_dormitory/internal/platform/config"
	"student_dormitory/internal/platform/requestid"
)

type authFixture struct {
	app         *App
	service     *auth.Service
	credentials map[string]auth.DevelopmentCredential
	logs        *bytes.Buffer
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()
	dir := t.TempDir()
	logs := new(bytes.Buffer)
	cfg := config.Config{DatabasePath: filepath.Join(dir, "auth.db"), FilesDir: filepath.Join(dir, "files"), AppEnv: "development", DevLoginEnabled: true, SessionTTL: time.Hour}
	a, err := New(context.Background(), cfg, slog.New(slog.NewJSONHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	service := auth.NewService(auth.NewRepository(a.db), audit.NewRecorder(a.db), auth.Options{DevelopmentLogin: true, SessionTTL: time.Hour})
	f := &authFixture{app: a, service: service, credentials: readDevelopmentCredentials(t, filepath.Join(dir, "dev-users.txt")), logs: logs}
	ctx := requestid.WithID(context.Background(), requestid.New())
	fund := inventory.NewService(inventory.NewRepository(a.db), service, audit.NewRecorder(a.db))
	if _, err := fund.Save(ctx, f.credentials["registry_staff"].UserID, inventory.Location{DormitoryID: "dev-dorm-1"}, inventory.Dormitory,
		inventory.Draft{Name: "Тестовое общежитие", Address: "Тестовый адрес", Operator: "Тестовый оператор", UseBasis: "Тестовое основание", Contacts: "Тестовые контакты"}, true, 0); err != nil {
		t.Fatal(err)
	}
	return f
}

func perform(handler http.Handler, method, path string, cookie *http.Cookie, form url.Values, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost:8080"+path, strings.NewReader(form.Encode()))
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

var csrfPattern = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

func csrf(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	matches := csrfPattern.FindStringSubmatch(w.Body.String())
	if len(matches) != 2 {
		t.Fatalf("no CSRF token, status %d", w.Code)
	}
	return matches[1]
}
func responseCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected one cookie, status %d", w.Code)
	}
	return cookies[0]
}
func login(t *testing.T, f *authFixture, role string) (*http.Cookie, *http.Cookie) {
	t.Helper()
	w := perform(f.app.Handler(), "GET", "/auth/login", nil, nil, "")
	anon := responseCookie(t, w)
	c := f.credentials[role]
	w = perform(f.app.Handler(), "POST", "/auth/login", anon, url.Values{"csrf_token": {csrf(t, w)}, "login": {c.Login}, "password": {c.Password}}, "")
	if w.Code != 303 {
		t.Fatalf("login %s status=%d", role, w.Code)
	}
	cookie := responseCookie(t, w)
	if cookie.Value == anon.Value {
		t.Fatal("session token not rotated")
	}
	if !cookie.HttpOnly || cookie.Secure || cookie.Path != "/" || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge <= 0 {
		t.Fatal("unsafe development cookie")
	}
	return cookie, anon
}

func TestAuthDirectURLsAndPermissionMatrix(t *testing.T) {
	f := newAuthFixture(t)
	// Assignment alone cannot grant a system administrator a business permission.
	if _, err := f.app.db.ExecContext(context.Background(), "INSERT INTO auth_user_dormitories (user_id, dormitory_id) VALUES (?, ?)", f.credentials["system_admin"].UserID, "dev-dorm-1"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/account", "/admin/users", "/staff/dormitories/dev-dorm-1"} {
		if w := perform(f.app.Handler(), "GET", path, nil, nil, ""); w.Code != 401 {
			t.Fatalf("anonymous URL %s: %d", path, w.Code)
		}
	}
	for _, role := range []string{"student", "registry_staff", "commandant", "administration", "system_admin"} {
		t.Run(role, func(t *testing.T) {
			cookie, anon := login(t, f, role)
			for _, test := range []struct {
				path string
				want int
			}{
				{"/account", 200},
				{"/admin/users", map[bool]int{true: 200, false: 403}[role == "system_admin"]},
				{"/staff/dormitories/dev-dorm-1", map[bool]int{true: 200, false: 403}[role != "student" && role != "system_admin"]},
				{"/staff/dormitories/dev-dorm-2", 403},
				{"/staff/dormitories/dev-dorm-1%27%20OR%201%3D1--", 403},
			} {
				w := perform(f.app.Handler(), "GET", test.path, cookie, nil, "")
				if w.Code != test.want {
					t.Fatalf("%s %s: %d want %d", role, test.path, w.Code, test.want)
				}
			}
			if w := perform(f.app.Handler(), "GET", "/account", anon, nil, ""); w.Code != 401 {
				t.Fatal("pre-login session still authenticated")
			}
			for _, permission := range []string{"registry.write", "places.status.write", "statistics.read", "audit.read"} {
				allowed := (role == "registry_staff" && permission == "registry.write") || (role == "commandant" && permission == "places.status.write") || (role == "administration" && (permission == "statistics.read" || permission == "audit.read"))
				err := f.service.Authorize(context.Background(), f.credentials[role].UserID, permission, "dev-dorm-1")
				if (err == nil) != allowed {
					t.Fatalf("wrong permission %s/%s", role, permission)
				}
				if err := f.service.Authorize(context.Background(), f.credentials[role].UserID, permission, "dev-dorm-2"); err == nil {
					t.Fatal("scope bypass")
				}
			}
		})
	}
	for _, credential := range f.credentials {
		if strings.Contains(f.logs.String(), credential.Password) {
			t.Fatal("password leaked into logs")
		}
	}
}

func TestCSRFRotationAndLogout(t *testing.T) {
	f := newAuthFixture(t)
	form := perform(f.app.Handler(), "GET", "/auth/login", nil, nil, "")
	anon := responseCookie(t, form)
	loginCSRF := csrf(t, form)
	c := f.credentials["student"]
	valid := url.Values{"csrf_token": {loginCSRF}, "login": {c.Login}, "password": {c.Password}}
	for _, test := range []struct {
		cookie *http.Cookie
		form   url.Values
		origin string
		want   int
	}{
		{nil, valid, "", 401}, {anon, url.Values{"login": {c.Login}, "password": {c.Password}}, "", 403},
		{anon, valid, "https://attacker.invalid", 403},
		{anon, url.Values{"csrf_token": {loginCSRF}, "login": {c.Login}, "password": {"wrong-random-password"}}, "", 401},
	} {
		if w := perform(f.app.Handler(), "POST", "/auth/login", test.cookie, test.form, test.origin); w.Code != test.want {
			t.Fatalf("login CSRF/credentials status %d want %d", w.Code, test.want)
		}
	}
	logged := perform(f.app.Handler(), "POST", "/auth/login", anon, valid, "")
	cookie := responseCookie(t, logged)
	if logged.Code != 303 {
		t.Fatalf("login status %d", logged.Code)
	}
	if w := perform(f.app.Handler(), "POST", "/auth/login", anon, valid, ""); w.Code != 401 {
		t.Fatal("anonymous session replay accepted")
	}
	account := perform(f.app.Handler(), "GET", "/account", cookie, nil, "")
	logoutCSRF := csrf(t, account)
	if logoutCSRF == loginCSRF {
		t.Fatal("CSRF not rotated")
	}
	if w := perform(f.app.Handler(), "POST", "/auth/logout", cookie, url.Values{"csrf_token": {loginCSRF}}, ""); w.Code != 403 {
		t.Fatal("stale CSRF accepted")
	}
	if w := perform(f.app.Handler(), "GET", "/auth/logout", cookie, nil, ""); w.Code != 405 {
		t.Fatal("GET logout mutates session")
	}
	other, _ := login(t, f, "commandant")
	if w := perform(f.app.Handler(), "POST", "/auth/logout", other, url.Values{"csrf_token": {logoutCSRF}}, ""); w.Code != 403 {
		t.Fatal("cross-session CSRF accepted")
	}
	out := perform(f.app.Handler(), "POST", "/auth/logout", cookie, url.Values{"csrf_token": {logoutCSRF}}, "")
	if out.Code != 303 || responseCookie(t, out).MaxAge != -1 {
		t.Fatal("logout cookie not cleared")
	}
	if w := perform(f.app.Handler(), "GET", "/account", cookie, nil, ""); w.Code != 401 {
		t.Fatal("logged-out cookie replay accepted")
	}
	if strings.Contains(f.logs.String(), cookie.Value) || strings.Contains(f.logs.String(), logoutCSRF) {
		t.Fatal("session secret leaked into logs")
	}
}

func TestExpiryRevocationAndProduction(t *testing.T) {
	f := newAuthFixture(t)
	cookie, _ := login(t, f, "registry_staff")
	ctx := context.Background()
	if _, err := f.app.db.ExecContext(ctx, "DELETE FROM auth_user_dormitories WHERE user_id = ?", f.credentials["registry_staff"].UserID); err != nil {
		t.Fatal(err)
	}
	if w := perform(f.app.Handler(), "GET", "/staff/dormitories/dev-dorm-1", cookie, nil, ""); w.Code != 403 {
		t.Fatal("scope revocation not immediate")
	}
	if _, err := f.app.db.ExecContext(ctx, "UPDATE auth_sessions SET expires_at = ?", time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	if w := perform(f.app.Handler(), "GET", "/account", cookie, nil, ""); w.Code != 401 {
		t.Fatal("expired session accepted")
	}
	cookie, _ = login(t, f, "student")
	if _, err := f.app.db.ExecContext(ctx, "UPDATE auth_users SET enabled = 0 WHERE id = ?", f.credentials["student"].UserID); err != nil {
		t.Fatal(err)
	}
	if w := perform(f.app.Handler(), "GET", "/account", cookie, nil, ""); w.Code != 401 {
		t.Fatal("disabled user accepted")
	}
	production := config.Config{AppEnv: "production", SessionTTL: time.Hour, DevLoginEnabled: true}
	if _, err := New(ctx, production, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("production accepted development login")
	}
	production.DevLoginEnabled = false
	production.FilesDir = t.TempDir()
	handler, err := newHandler(ctx, f.app.db, slog.New(slog.NewTextHandler(io.Discard, nil)), production)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "POST"} {
		if w := perform(handler, method, "/auth/login", nil, nil, ""); w.Code != 404 {
			t.Fatal("production login route exists")
		}
	}
	// Even copying a development token into the production cookie cannot authenticate.
	admin, _ := login(t, f, "system_admin")
	admin.Name = "__Host-dorm_session"
	if w := perform(handler, "GET", "/account", admin, nil, ""); w.Code != 401 {
		t.Fatal("development session accepted in production")
	}
	secureCookie := responseCookie(t, perform(handler, "GET", "/account", nil, nil, ""))
	if !secureCookie.Secure || !secureCookie.HttpOnly || secureCookie.Name != "__Host-dorm_session" || secureCookie.Path != "/" || secureCookie.Domain != "" || secureCookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("unsafe production cookie")
	}
}

func TestAuthChangesRollbackWhenAuditFails(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	if _, err := f.app.db.ExecContext(ctx, `CREATE TRIGGER reject_auth_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	form := perform(f.app.Handler(), "GET", "/auth/login", nil, nil, "")
	anon := responseCookie(t, form)
	c := f.credentials["student"]
	w := perform(f.app.Handler(), "POST", "/auth/login", anon, url.Values{"csrf_token": {csrf(t, form)}, "login": {c.Login}, "password": {c.Password}}, "")
	if w.Code != 500 {
		t.Fatalf("audit failure status: %d", w.Code)
	}
	var count int
	if err := f.app.db.QueryRowContext(ctx, "SELECT count(*) FROM auth_sessions WHERE user_id IS NOT NULL").Scan(&count); err != nil || count != 0 {
		t.Fatalf("unaudited session committed: %d %v", count, err)
	}
	if _, err := f.service.Session(ctx, anon.Value); err != nil {
		t.Fatal("anonymous session lost on rollback")
	}
	if _, err := f.app.db.ExecContext(ctx, "DROP TRIGGER reject_auth_audit"); err != nil {
		t.Fatal(err)
	}
	cookie, _ := login(t, f, "student")
	account := perform(f.app.Handler(), "GET", "/account", cookie, nil, "")
	if _, err := f.app.db.ExecContext(ctx, `CREATE TRIGGER reject_auth_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	w = perform(f.app.Handler(), "POST", "/auth/logout", cookie, url.Values{"csrf_token": {csrf(t, account)}}, "")
	if w.Code != 500 {
		t.Fatal("unaudited logout returned success")
	}
	if w := perform(f.app.Handler(), "GET", "/account", cookie, nil, ""); w.Code != 200 {
		t.Fatal("logout audit failure did not roll back session removal")
	}
}

type fixtureVerifier struct{ identity auth.Identity }

func (v fixtureVerifier) Verify(context.Context) (auth.Identity, error) { return v.identity, nil }

func TestIdentityMappingPreservesLocalUserID(t *testing.T) {
	f := newAuthFixture(t)
	ctx := requestid.WithID(context.Background(), requestid.New())
	userID := f.credentials["student"].UserID
	// A verified identity from another adapter links to the same local user.
	// This fixture is not an SSO protocol implementation.
	identity := auth.Identity{Provider: "fixture-provider", Subject: "external-subject-1"}
	if _, err := f.app.db.ExecContext(ctx, "INSERT INTO auth_external_identities (provider, subject, user_id) VALUES (?, ?, ?)", identity.Provider, identity.Subject, userID); err != nil {
		t.Fatal(err)
	}
	_, token, err := f.service.Anonymous(ctx)
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := f.service.Login(ctx, token, fixtureVerifier{identity})
	if err != nil || session.UserID != userID {
		t.Fatal("external identity changed local user ID")
	}
	if _, err := f.app.db.ExecContext(ctx, "INSERT INTO auth_external_identities (provider, subject, user_id) VALUES (?, ?, ?)", identity.Provider, identity.Subject, f.credentials["system_admin"].UserID); err == nil {
		t.Fatal("identity linked to two users")
	}
	_, token, err = f.service.Anonymous(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.service.Login(ctx, token, fixtureVerifier{auth.Identity{Provider: "fixture-provider", Subject: "unknown"}}); err == nil {
		t.Fatal("unknown external identity auto-created a user")
	}
	// The session lifecycle itself does not depend on development authentication.
	service := auth.NewService(auth.NewRepository(f.app.db), audit.NewRecorder(f.app.db), auth.Options{SessionTTL: time.Hour})
	_, token, err = service.Anonymous(ctx)
	if err != nil {
		t.Fatal(err)
	}
	session, _, err = service.Login(ctx, token, fixtureVerifier{identity})
	if err != nil || session.UserID != userID {
		t.Fatal("identity adapter coupled to development flag")
	}
	_, token, err = service.Anonymous(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := f.credentials["student"]
	if _, _, err := service.Login(ctx, token, auth.NewDevelopmentProvider(auth.NewRepository(f.app.db)).Attempt(c.Login, c.Password)); err != auth.ErrUnavailable {
		t.Fatal("service accepted development identity with login disabled")
	}
}
