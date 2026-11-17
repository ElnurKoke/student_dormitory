package app

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestNavigationReflectsAccessOnEveryPage(t *testing.T) {
	f := newAuthFixture(t)
	for _, role := range []string{"student", "registry_staff", "commandant", "administration", "system_admin"} {
		cookie, _ := login(t, f, role)
		for _, path := range []string{"/", "/account", "/missing", "/catalog", "/catalog/missing", "/catalog?page=abc"} {
			w := perform(f.app.Handler(), "GET", path, cookie, nil, "")
			body := w.Body.String()
			if !strings.Contains(body, `action="/auth/logout"`) || !strings.Contains(body, `href="/account"`) {
				t.Fatalf("missing session navigation on %s for %s", path, role)
			}
			if strings.Contains(body, `href="/auth/login"`) {
				t.Fatalf("signed-in user sees login on %s for %s", path, role)
			}
			if has := strings.Contains(body, `href="/admin/users"`); has != (role == "system_admin") {
				t.Fatalf("wrong account-management navigation for %s", role)
			}
			if has := strings.Contains(body, `href="/staff/dormitories/dev-dorm-1"`); has != (role != "student" && role != "system_admin") {
				t.Fatalf("wrong dormitory navigation for %s", role)
			}
			if strings.Contains(body, `href="/staff/dormitories/dev-dorm-2"`) {
				t.Fatal("unassigned dormitory in navigation")
			}
		}
	}
	guest := perform(f.app.Handler(), "GET", "/account", nil, nil, "")
	if guest.Code != http.StatusUnauthorized || !strings.Contains(guest.Body.String(), `href="/auth/login"`) {
		t.Fatal("authentication error offers no way to sign in")
	}
}

func TestUsersSearchAndLoginRecovery(t *testing.T) {
	f := newAuthFixture(t)
	admin, _ := login(t, f, "system_admin")
	w := perform(f.app.Handler(), "GET", "/admin/users?q=registry_staff", admin, nil, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "registry_staff") || strings.Contains(w.Body.String(), f.credentials["student"].UserID) {
		t.Fatal("user search does not filter results")
	}
	w = perform(f.app.Handler(), "GET", "/admin/users?q=no-such-user", admin, nil, "")
	if !strings.Contains(w.Body.String(), "Пользователи не найдены") || !strings.Contains(w.Body.String(), "Сбросить поиск") {
		t.Fatal("empty search has no recovery action")
	}
	student, _ := login(t, f, "student")
	if w := perform(f.app.Handler(), "GET", "/admin/users?q=student", student, nil, ""); w.Code != http.StatusForbidden {
		t.Fatal("search bypasses authorization")
	}
	form := perform(f.app.Handler(), "GET", "/auth/login", nil, nil, "")
	w = perform(f.app.Handler(), "POST", "/auth/login", responseCookie(t, form), url.Values{
		"csrf_token": {csrf(t, form)}, "login": {"dev_student"}, "password": {"wrong-secret-never-return"},
	}, "")
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `value="dev_student"`) || !strings.Contains(w.Body.String(), `id="login-error"`) || strings.Contains(w.Body.String(), "wrong-secret-never-return") {
		t.Fatal("login recovery loses login, exposes password or lacks an error message")
	}
	if csrf(t, w) == "" {
		t.Fatal("retry has no CSRF token")
	}
}

func TestLogoutFromCatalog(t *testing.T) {
	f := newAuthFixture(t)
	cookie, _ := login(t, f, "student")
	page := perform(f.app.Handler(), "GET", "/catalog", cookie, nil, "")
	w := perform(f.app.Handler(), "POST", "/auth/logout", cookie, url.Values{"csrf_token": {csrf(t, page)}}, "")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("logout from catalogue failed: %d", w.Code)
	}
	w = perform(f.app.Handler(), "GET", "/catalog", cookie, nil, "")
	if !strings.Contains(w.Body.String(), `href="/auth/login"`) || strings.Contains(w.Body.String(), `action="/auth/logout"`) {
		t.Fatal("catalogue still shows signed-in navigation after logout")
	}
}
