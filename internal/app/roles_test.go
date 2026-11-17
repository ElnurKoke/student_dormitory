package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestRoleManagementHTTPAuthorizationAndCSRF(t *testing.T) {
	f := newAuthFixture(t)
	target := f.credentials["student"].UserID
	path := "/admin/users/" + target + "/role"
	for _, role := range []string{"student", "registry_staff", "commandant", "administration", "system_admin"} {
		cookie, _ := login(t, f, role)
		account := perform(f.app.Handler(), "GET", "/account", cookie, nil, "")
		token := csrf(t, account)
		if w := perform(f.app.Handler(), "GET", "/account/notifications", cookie, nil, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "Уведомлений пока нет.") {
			t.Fatal("notification inbox unavailable", role, w.Code)
		}
		if !strings.Contains(account.Body.String(), `href="/account/notifications"`) {
			t.Fatal("inbox link missing for", role)
		}
		if w := perform(f.app.Handler(), "GET", path, cookie, nil, ""); w.Code != 405 {
			t.Fatal("mutating GET", w.Code)
		}
		for _, attack := range []struct {
			form           url.Values
			origin, suffix string
		}{
			{url.Values{"role_id": {"system_admin"}, "version": {"1"}}, "", ""},
			{url.Values{"csrf_token": {"wrong"}, "role_id": {"system_admin"}, "version": {"1"}}, "", ""},
			{url.Values{"role_id": {"system_admin"}, "version": {"1"}}, "", "?csrf_token=" + token},
			{url.Values{"csrf_token": {token}, "role_id": {"system_admin"}, "version": {"1"}}, "https://attacker.invalid", ""},
		} {
			if w := perform(f.app.Handler(), "POST", path+attack.suffix, cookie, attack.form, attack.origin); w.Code != 403 {
				t.Fatal("CSRF accepted", role, w.Code)
			}
		}
		if role != "system_admin" {
			form := url.Values{"csrf_token": {token}, "role_id": {"system_admin"}, "version": {"1"}, "user_id": {f.credentials["system_admin"].UserID}, "actor_id": {f.credentials["system_admin"].UserID}}
			if w := perform(f.app.Handler(), "POST", path, cookie, form, ""); w.Code != 403 {
				t.Fatal("non-administrator changed role", role, w.Code)
			}
		}
	}
	if w := perform(f.app.Handler(), "POST", path, nil, url.Values{"role_id": {"system_admin"}, "version": {"1"}}, ""); w.Code != 401 {
		t.Fatal("anonymous change", w.Code)
	}
	admin, _ := login(t, f, "system_admin")
	page := perform(f.app.Handler(), "GET", "/admin/users?lang=kk", admin, nil, "")
	if page.Code != 200 || !strings.Contains(page.Body.String(), "Рөлді сақтау") || !strings.Contains(page.Body.String(), `name="role_id"`) || strings.Contains(page.Body.String(), "пока недоступны") {
		t.Fatal("missing role editor", page.Code)
	}
	for _, test := range []struct {
		path, role, version string
		want                int
	}{
		{path, "invalid", "1", 422}, {path, "system_admin", "bad", 409}, {path, "system_admin", "0", 409},
		{"/admin/users/missing/role", "student", "1", 404},
		{"/admin/users/" + f.credentials["system_admin"].UserID + "/role", "student", "1", 409},
	} {
		w := perform(f.app.Handler(), "POST", test.path, admin, url.Values{"csrf_token": {csrf(t, page)}, "role_id": {test.role}, "version": {test.version}}, "")
		if w.Code != test.want {
			t.Fatal("invalid role command", test, w.Code)
		}
	}
	var count int
	if err := f.app.db.QueryRowContext(context.Background(), `SELECT count(*) FROM auth_role_notifications`).Scan(&count); err != nil || count != 0 {
		t.Fatal("rejected commands changed state", count, err)
	}
}

func TestRoleChangeNotifiesExistingSessionAndSurvivesRestart(t *testing.T) {
	f := newAuthFixture(t)
	admin, _ := login(t, f, "system_admin")
	user, _ := login(t, f, "registry_staff")
	target := f.credentials["registry_staff"].UserID
	path := "/admin/users/" + target + "/role"
	page := perform(f.app.Handler(), "GET", "/admin/users?q=registry_staff&lang=kk", admin, nil, "")
	form := url.Values{"csrf_token": {csrf(t, page)}, "role_id": {"commandant"}, "version": {"1"}, "q": {"registry_staff"}, "user_id": {f.credentials["student"].UserID}, "actor_id": {f.credentials["student"].UserID}}
	w := perform(f.app.Handler(), "POST", path+"?lang=kk", admin, form, "")
	if w.Code != 303 || !strings.Contains(w.Header().Get("Location"), "q=registry_staff") || !strings.Contains(w.Header().Get("Location"), "lang=kk") {
		t.Fatal("role save or redirect", w.Code, w.Header().Get("Location"))
	}
	account := perform(f.app.Handler(), "GET", "/account", user, nil, "")
	if account.Code != 200 || !strings.Contains(account.Body.String(), "Комендант") || !strings.Contains(account.Body.String(), `class="badge">1</span>`) {
		t.Fatal("existing session did not receive role/notification", account.Code)
	}
	ctx := context.Background()
	if err := f.service.Authorize(ctx, target, "registry.write", "dev-dorm-1"); err == nil {
		t.Fatal("old permission retained")
	}
	if err := f.service.Authorize(ctx, target, "places.status.write", "dev-dorm-1"); err != nil {
		t.Fatal("new permission unavailable", err)
	}
	notes, err := f.service.RoleNotifications(ctx, target)
	if err != nil || len(notes) != 1 {
		t.Fatal("notification missing", notes, err)
	}
	inbox := perform(f.app.Handler(), "GET", "/account/notifications?lang=kk", user, nil, "")
	if inbox.Code != 200 || !strings.Contains(inbox.Body.String(), "Сіздің рөліңіз өзгертілді") || !strings.Contains(inbox.Body.String(), "Комендант") || strings.Contains(inbox.Body.String(), "Хабарламалар әзірше жоқ") {
		t.Fatal("notification not displayed", inbox.Code)
	}
	if w := perform(f.app.Handler(), "POST", path, admin, form, ""); w.Code != 409 {
		t.Fatal("stale role form accepted", w.Code)
	}
	readPath := "/account/role-notifications/" + strconv.FormatInt(notes[0].ID, 10) + "/read"
	if w := perform(f.app.Handler(), "GET", readPath, user, nil, ""); w.Code != 405 {
		t.Fatal("mutating read GET", w.Code)
	}
	if w := perform(f.app.Handler(), "POST", readPath, user, nil, ""); w.Code != 403 {
		t.Fatal("read without CSRF", w.Code)
	}
	if w := perform(f.app.Handler(), "POST", readPath, user, url.Values{"csrf_token": {csrf(t, inbox)}}, "https://attacker.invalid"); w.Code != 403 {
		t.Fatal("read cross-origin", w.Code)
	}
	adminPage := perform(f.app.Handler(), "GET", "/account", admin, nil, "")
	if w := perform(f.app.Handler(), "POST", readPath, admin, url.Values{"csrf_token": {csrf(t, adminPage)}}, ""); w.Code != 404 {
		t.Fatal("read someone else's notification", w.Code)
	}
	for range 2 {
		if w := perform(f.app.Handler(), "POST", readPath, user, url.Values{"csrf_token": {csrf(t, inbox)}}, ""); w.Code != 303 {
			t.Fatal("read notification", w.Code)
		}
	}
	account = perform(f.app.Handler(), "GET", "/account", user, nil, "")
	if strings.Contains(account.Body.String(), `class="badge">1</span>`) {
		t.Fatal("unread badge not cleared")
	}
	cfg := f.app.config
	if err := f.app.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	inbox = perform(restarted.Handler(), "GET", "/account/notifications", user, nil, "")
	if inbox.Code != http.StatusOK || !strings.Contains(inbox.Body.String(), "Ваша роль изменена") || !strings.Contains(inbox.Body.String(), "Прочитано") {
		t.Fatal("notification lost after restart", inbox.Code)
	}
	account = perform(restarted.Handler(), "GET", "/account", user, nil, "")
	if account.Code != 200 || !strings.Contains(account.Body.String(), "Комендант") {
		t.Fatal("development bootstrap overwrote changed role", account.Code)
	}
}

func TestApplicationNotificationsRemainPrivateAfterRoleChange(t *testing.T) {
	f := newAuthFixture(t)
	student, _ := login(t, f, "student")
	admin, _ := login(t, f, "system_admin")
	path := newApplicationHTTP(t, f, student)
	page := perform(f.app.Handler(), "GET", path, student, nil, "")
	if w := perform(f.app.Handler(), "POST", path+"/submit", student, appSubmission(t, page), ""); w.Code != 303 {
		t.Fatal("submit", w.Code)
	}
	var notificationID int64
	if err := f.app.db.QueryRowContext(context.Background(), `SELECT id FROM application_notifications WHERE user_id=?`, f.credentials["student"].UserID).Scan(&notificationID); err != nil {
		t.Fatal(err)
	}
	page = perform(f.app.Handler(), "GET", "/admin/users", admin, nil, "")
	form := url.Values{"csrf_token": {csrf(t, page)}, "version": {"1"}, "role_id": {"commandant"}}
	if w := perform(f.app.Handler(), "POST", "/admin/users/"+f.credentials["student"].UserID+"/role", admin, form, ""); w.Code != 303 {
		t.Fatal("change student role", w.Code)
	}
	inbox := perform(f.app.Handler(), "GET", "/account/notifications", student, nil, "")
	if inbox.Code != 200 || !strings.Contains(inbox.Body.String(), "Заявление зарегистрировано") || !strings.Contains(inbox.Body.String(), "Ваша роль изменена") {
		t.Fatal("old notification lost after role change", inbox.Code)
	}
	if w := perform(f.app.Handler(), "GET", path, student, nil, ""); w.Code != 403 {
		t.Fatal("inbox bypassed application rights", w.Code)
	}
	readPath := "/account/notifications/" + strconv.FormatInt(notificationID, 10) + "/read"
	for range 2 {
		if w := perform(f.app.Handler(), "POST", readPath, student, url.Values{"csrf_token": {csrf(t, inbox)}}, ""); w.Code != 303 {
			t.Fatal("former student cannot read own notification", w.Code)
		}
	}
	other, _ := login(t, f, "commandant")
	otherPage := perform(f.app.Handler(), "GET", "/account/notifications", other, nil, "")
	if otherPage.Code != 200 || strings.Contains(otherPage.Body.String(), "Заявление зарегистрировано") || strings.Contains(otherPage.Body.String(), "Ваша роль изменена") {
		t.Fatal("notification leaked to other account", otherPage.Code)
	}
	if w := perform(f.app.Handler(), "POST", readPath, other, url.Values{"csrf_token": {csrf(t, otherPage)}}, ""); w.Code != 404 {
		t.Fatal("another account read former student's notification", w.Code)
	}
}
