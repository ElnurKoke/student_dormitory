package app

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func createInventoryObject(t *testing.T, f *authFixture, cookie *http.Cookie, path string, fields url.Values) string {
	t.Helper()
	page := perform(f.app.Handler(), "GET", path, cookie, nil, "")
	if page.Code != 200 {
		t.Fatalf("new form %s status %d", path, page.Code)
	}
	fields.Set("csrf_token", csrf(t, page))
	result := perform(f.app.Handler(), "POST", path, cookie, fields, "")
	if result.Code != 303 {
		t.Fatalf("create via %s status %d", path, result.Code)
	}
	target, err := url.Parse(result.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return target.Path
}
func inventoryRoomFields() url.Values {
	return url.Values{"number": {"101"}, "capacity": {"2"}, "area": {"20.25"}, "equipment": {"Стол и шкаф"}, "sanitary": {"Санузел на этаже"}, "accessible": {"on"}, "accessibility_notes": {"Доступный вход"}, "price_minor": {"12345"}, "currency": {"KZT"}, "price_period": {"месяц"}}
}

var versionPattern = regexp.MustCompile(`name="version" value="([0-9]+)"`)

func version(t *testing.T, w string) string {
	t.Helper()
	m := versionPattern.FindStringSubmatch(w)
	if len(m) != 2 {
		t.Fatal("missing record version")
	}
	return m[1]
}

func TestInventoryAdministrativeHTTP(t *testing.T) {
	f := newAuthFixture(t)
	cookie, _ := login(t, f, "registry_staff")
	root := "/staff/dormitories/dev-dorm-1"
	if _, err := f.app.db.ExecContext(context.Background(), "INSERT INTO auth_user_dormitories (user_id,dormitory_id) VALUES (?, ?)", f.credentials["registry_staff"].UserID, "dev-dorm-2"); err != nil {
		t.Fatal(err)
	}
	dorm := createInventoryObject(t, f, cookie, "/staff/dormitories/new", url.Values{"dormitory_id": {"dev-dorm-2"}, "name": {"Второе общежитие"}, "address": {"Адрес"}, "operator": {"Оператор"}, "use_basis": {"Договор"}, "contacts": {"Контакт"}, "published": {"on"}})
	if dorm != "/staff/dormitories/dev-dorm-2" {
		t.Fatal("assigned permanent ID changed")
	}
	building := createInventoryObject(t, f, cookie, root+"/buildings/new", url.Values{"name": {"Корпус А"}})
	floor := createInventoryObject(t, f, cookie, building+"/floors/new", url.Values{"number": {"1"}})
	room := createInventoryObject(t, f, cookie, floor+"/rooms/new", inventoryRoomFields())
	page := perform(f.app.Handler(), "GET", room, cookie, nil, "")
	if page.Code != 200 || !strings.Contains(page.Body.String(), "12345 KZT") || !strings.Contains(page.Body.String(), "20.25 м²") {
		t.Fatal("room details incorrect")
	}
	// Invalid input is retained alongside field-level errors, not replaced by zeroes.
	form := perform(f.app.Handler(), "GET", room+"/edit", cookie, nil, "")
	fields := inventoryRoomFields()
	fields.Set("capacity", "0")
	fields.Set("csrf_token", csrf(t, form))
	fields.Set("version", version(t, form.Body.String()))
	invalid := perform(f.app.Handler(), "POST", room+"/edit", cookie, fields, "")
	if invalid.Code != 422 || !strings.Contains(invalid.Body.String(), `id="capacity-error"`) || !strings.Contains(invalid.Body.String(), `value="12345"`) {
		t.Fatal("validation is not usable")
	}
	fields.Set("capacity", "3")
	fields.Set("number", "102")
	saved := perform(f.app.Handler(), "POST", room+"/edit", cookie, fields, "")
	if saved.Code != 303 {
		t.Fatalf("edit returned %d", saved.Code)
	}
	stale := perform(f.app.Handler(), "POST", room+"/edit", cookie, fields, "")
	if stale.Code != 409 {
		t.Fatal("stale form overwrote the record")
	}
	if w := perform(f.app.Handler(), "POST", room+"/archive", cookie, url.Values{"version": {"2"}}, ""); w.Code != 403 {
		t.Fatal("archive accepts missing CSRF")
	}
	if w := perform(f.app.Handler(), "GET", room+"/archive", cookie, nil, ""); w.Code != 405 {
		t.Fatal("GET mutates archive state")
	}
	if w := perform(f.app.Handler(), "DELETE", floor, cookie, nil, ""); w.Code != 405 {
		t.Fatal("deletion exposed")
	}
	// Parent archiving fails while the room is active, with a useful explanation.
	page = perform(f.app.Handler(), "GET", floor, cookie, nil, "")
	blocked := perform(f.app.Handler(), "POST", floor+"/archive", cookie, url.Values{"csrf_token": {csrf(t, page)}, "version": {version(t, page.Body.String())}}, "")
	if blocked.Code != 409 || !strings.Contains(blocked.Body.String(), "Сначала архивируйте") {
		t.Fatal("unsafe parent archiving or unhelpful error")
	}
	page = perform(f.app.Handler(), "GET", room, cookie, nil, "")
	archived := perform(f.app.Handler(), "POST", room+"/archive", cookie, url.Values{"csrf_token": {csrf(t, page)}, "version": {version(t, page.Body.String())}}, "")
	if archived.Code != 303 {
		t.Fatal("archive failed")
	}
	page = perform(f.app.Handler(), "GET", room, cookie, nil, "")
	if page.Code != 200 || !strings.Contains(page.Body.String(), "Восстановить объект") {
		t.Fatal("archive lost object or restore action")
	}
	restored := perform(f.app.Handler(), "POST", room+"/restore", cookie, url.Values{"csrf_token": {csrf(t, page)}, "version": {version(t, page.Body.String())}}, "")
	if restored.Code != 303 {
		t.Fatal("restore failed")
	}
	// Posting a field which looks like a parent ID cannot move the object.
	form = perform(f.app.Handler(), "GET", room+"/edit", cookie, nil, "")
	fields = inventoryRoomFields()
	fields.Set("csrf_token", csrf(t, form))
	fields.Set("version", version(t, form.Body.String()))
	fields.Set("floor_id", "some-other-floor")
	if w := perform(f.app.Handler(), "POST", room+"/edit", cookie, fields, ""); w.Code != 303 {
		t.Fatal("edit failed")
	}
	if w := perform(f.app.Handler(), "GET", room, cookie, nil, ""); w.Code != 200 {
		t.Fatal("object moved by forged parent field")
	}
}

func TestInventoryHTTPAccessBoundaries(t *testing.T) {
	f := newAuthFixture(t)
	writer, _ := login(t, f, "registry_staff")
	root := "/staff/dormitories/dev-dorm-1"
	building := createInventoryObject(t, f, writer, root+"/buildings/new", url.Values{"name": {"Корпус"}})
	floor := createInventoryObject(t, f, writer, building+"/floors/new", url.Values{"number": {"1"}})
	room := createInventoryObject(t, f, writer, floor+"/rooms/new", inventoryRoomFields())
	for _, role := range []string{"student", "system_admin", "commandant", "administration"} {
		cookie, _ := login(t, f, role)
		for _, path := range []string{root + "/edit", root + "/buildings/new", building + "/edit", floor + "/rooms/new", room + "/edit"} {
			if w := perform(f.app.Handler(), "GET", path, cookie, nil, ""); w.Code != 403 {
				t.Fatalf("%s can open write URL: %s (%d)", role, path, w.Code)
			}
			account := perform(f.app.Handler(), "GET", "/account", cookie, nil, "")
			if w := perform(f.app.Handler(), "POST", path, cookie, url.Values{"csrf_token": {csrf(t, account)}, "version": {"1"}}, ""); w.Code != 403 {
				t.Fatalf("%s can POST write URL: %d", role, w.Code)
			}
		}
		want := 200
		if role == "student" || role == "system_admin" {
			want = 403
		}
		if w := perform(f.app.Handler(), "GET", room, cookie, nil, ""); w.Code != want {
			t.Fatal("incorrect inventory read grant")
		}
	}
	if w := perform(f.app.Handler(), "GET", strings.Replace(room, "dev-dorm-1", "unassigned", 1), writer, nil, ""); w.Code != 403 {
		t.Fatal("unassigned root accepted")
	}
	// Even a valid session loses write access immediately when its assignment is revoked.
	page := perform(f.app.Handler(), "GET", room+"/edit", writer, nil, "")
	if _, err := f.app.db.ExecContext(context.Background(), "DELETE FROM auth_user_dormitories WHERE user_id = ?", f.credentials["registry_staff"].UserID); err != nil {
		t.Fatal(err)
	}
	fields := inventoryRoomFields()
	fields.Set("csrf_token", csrf(t, page))
	fields.Set("version", version(t, page.Body.String()))
	if w := perform(f.app.Handler(), "POST", room+"/edit", writer, fields, ""); w.Code != 403 {
		t.Fatal("revoked writer can still edit")
	}
}
