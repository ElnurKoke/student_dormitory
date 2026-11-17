package app

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func bedRoom(t *testing.T, f *authFixture, cookie *http.Cookie) string {
	t.Helper()
	b := createInventoryObject(t, f, cookie, "/staff/dormitories/dev-dorm-1/buildings/new", url.Values{"name": {"Корпус"}})
	floor := createInventoryObject(t, f, cookie, b+"/floors/new", url.Values{"number": {"1"}})
	return createInventoryObject(t, f, cookie, floor+"/rooms/new", inventoryRoomFields())
}

func TestBedHTTPStatusHistoryAndConflicts(t *testing.T) {
	f := newAuthFixture(t)
	cookie, _ := login(t, f, "registry_staff")
	statusCookie, _ := login(t, f, "commandant")
	room := bedRoom(t, f, cookie)
	bed := createInventoryObject(t, f, cookie, room+"/beds/new", url.Values{"number": {"Место А"}, "basis": {"Акт ввода"}, "status": {"occupied"}, "user_id": {"forged"}})
	page := perform(f.app.Handler(), "GET", bed, cookie, nil, "")
	if page.Code != 200 || !strings.Contains(page.Body.String(), "История изменений") || !strings.Contains(page.Body.String(), "Акт ввода") || !strings.Contains(page.Body.String(), "Свободно") {
		t.Fatalf("bad bed page %d", page.Code)
	}
	listing := perform(f.app.Handler(), "GET", room, cookie, nil, "")
	if !strings.Contains(listing.Body.String(), bed) || !strings.Contains(listing.Body.String(), "Место А") {
		t.Fatal("room does not list beds")
	}
	form := perform(f.app.Handler(), "GET", bed+"/status", statusCookie, nil, "")
	if form.Code != 200 {
		t.Fatalf("status form %d", form.Code)
	}
	html := form.Body.String()
	for _, forbidden := range []string{`value="reserved"`, `value="occupied"`, `value="free"`} {
		if strings.Contains(html, forbidden) {
			t.Fatal("forbidden status option in form")
		}
	}
	fields := url.Values{"csrf_token": {csrf(t, form)}, "version": {version(t, html)}, "status": {"maintenance"}, "basis": {"<script>private basis</script>"}, "actor_id": {"forged"}, "occurred_at": {"forged"}}
	if w := perform(f.app.Handler(), "POST", bed+"/status", statusCookie, url.Values{"version": {"1"}, "status": {"maintenance"}, "basis": {"Акт"}}, ""); w.Code != 403 {
		t.Fatal("CSRF not enforced")
	}
	if w := perform(f.app.Handler(), "PUT", bed+"/status", statusCookie, nil, ""); w.Code != 405 {
		t.Fatal("unsupported method accepted")
	}
	invalid := url.Values{}
	for key, values := range fields {
		invalid[key] = append([]string(nil), values...)
	}
	invalid.Set("status", "occupied")
	if w := perform(f.app.Handler(), "POST", bed+"/status", statusCookie, invalid, ""); w.Code != 422 || !strings.Contains(w.Body.String(), "ручной переход") {
		t.Fatal("forged occupancy accepted")
	}
	invalid.Set("status", "maintenance")
	invalid.Set("basis", " ")
	if w := perform(f.app.Handler(), "POST", bed+"/status", statusCookie, invalid, ""); w.Code != 422 {
		t.Fatal("missing basis accepted")
	}
	if w := perform(f.app.Handler(), "POST", bed+"/status", statusCookie, fields, ""); w.Code != 303 {
		t.Fatalf("status save returned %d", w.Code)
	}
	fields.Set("status", "unavailable")
	stale := perform(f.app.Handler(), "POST", bed+"/status", statusCookie, fields, "")
	if stale.Code != 409 || !strings.Contains(stale.Body.String(), `name="version" value="1"`) || !strings.Contains(stale.Body.String(), "Обновить форму") {
		t.Fatal("stale form silently rebased or no useful conflict")
	}
	page = perform(f.app.Handler(), "GET", bed, cookie, nil, "")
	if page.Code != 200 || strings.Contains(page.Body.String(), "<script>private basis</script>") || !strings.Contains(page.Body.String(), "&lt;script&gt;private basis&lt;/script&gt;") {
		t.Fatal("basis not escaped")
	}
	var actor, at, status string
	if err := f.app.db.QueryRowContext(context.Background(), "SELECT actor_id, occurred_at, new_status FROM inventory_bed_history ORDER BY id DESC LIMIT 1").Scan(&actor, &at, &status); err != nil {
		t.Fatal(err)
	}
	if actor != f.credentials["commandant"].UserID || at == "forged" || at == "" || status != "maintenance" {
		t.Fatal("client can forge provenance")
	}
	form = perform(f.app.Handler(), "GET", bed+"/status", statusCookie, nil, "")
	html = form.Body.String()
	if !strings.Contains(html, `value="free"`) || strings.Contains(html, `value="unavailable"`) || strings.Contains(html, `value="maintenance"`) {
		t.Fatal("repair form has forbidden targets")
	}
	fields = url.Values{"csrf_token": {csrf(t, form)}, "version": {version(t, html)}, "status": {"free"}, "basis": {"Ремонт завершён"}}
	if w := perform(f.app.Handler(), "POST", bed+"/status", statusCookie, fields, ""); w.Code != 303 {
		t.Fatal("return to free failed")
	}
	// Direct designation edits cannot act as status changes.
	form = perform(f.app.Handler(), "GET", bed+"/edit", cookie, nil, "")
	fields = url.Values{"csrf_token": {csrf(t, form)}, "version": {version(t, form.Body.String())}, "number": {"Место Б"}, "status": {"reserved"}, "basis": {"Переобозначение"}, "room_id": {"forged"}}
	if w := perform(f.app.Handler(), "POST", bed+"/edit", cookie, fields, ""); w.Code != 303 {
		t.Fatal("designation save failed")
	}
	var n int
	if err := f.app.db.QueryRowContext(context.Background(), "SELECT count(*) FROM inventory_beds WHERE number = 'Место Б' AND status = 'free'").Scan(&n); err != nil || n != 1 {
		t.Fatal("designation form can change status or identity")
	}
	page = perform(f.app.Handler(), "GET", bed, cookie, nil, "")
	fields = url.Values{"csrf_token": {csrf(t, page)}, "version": {version(t, page.Body.String())}, "basis": {"Вывод из фонда"}}
	if w := perform(f.app.Handler(), "POST", bed+"/archive", cookie, fields, ""); w.Code != 303 {
		t.Fatal("bed archive failed")
	}
	page = perform(f.app.Handler(), "GET", bed, cookie, nil, "")
	if !strings.Contains(page.Body.String(), "Вывод из фонда") || strings.Contains(page.Body.String(), `href="`+bed+`/status"`) {
		t.Fatal("archived bed history missing or editable")
	}
	fields.Set("version", version(t, page.Body.String()))
	fields.Set("basis", "Возврат в фонд")
	if w := perform(f.app.Handler(), "POST", bed+"/restore", cookie, fields, ""); w.Code != 303 {
		t.Fatal("bed restore failed")
	}
}

func TestBedHTTPPermissionsAndCapacity(t *testing.T) {
	f := newAuthFixture(t)
	writer, _ := login(t, f, "registry_staff")
	room := bedRoom(t, f, writer)
	bed := createInventoryObject(t, f, writer, room+"/beds/new", url.Values{"number": {"1"}, "basis": {"Ввод"}})
	createInventoryObject(t, f, writer, room+"/beds/new", url.Values{"number": {"2"}, "basis": {"Ввод"}})
	form := perform(f.app.Handler(), "GET", room+"/beds/new", writer, nil, "")
	fields := url.Values{"csrf_token": {csrf(t, form)}, "number": {"3"}, "basis": {"Ввод"}}
	if w := perform(f.app.Handler(), "POST", room+"/beds/new", writer, fields, ""); w.Code != 409 || !strings.Contains(w.Body.String(), "Вместимость комнаты исчерпана") {
		t.Fatal("capacity overflow through HTTP")
	}
	form = perform(f.app.Handler(), "GET", room+"/edit", writer, nil, "")
	fields = inventoryRoomFields()
	fields.Set("capacity", "1")
	fields.Set("version", version(t, form.Body.String()))
	fields.Set("csrf_token", csrf(t, form))
	if w := perform(f.app.Handler(), "POST", room+"/edit", writer, fields, ""); w.Code != 422 || !strings.Contains(w.Body.String(), "действующих койко-мест") {
		t.Fatal("room can shrink below beds through HTTP")
	}
	for _, role := range []string{"student", "system_admin", "commandant", "administration"} {
		cookie, _ := login(t, f, role)
		account := perform(f.app.Handler(), "GET", "/account", cookie, nil, "")
		for _, path := range []string{room + "/beds/new", bed + "/edit"} {
			if w := perform(f.app.Handler(), "GET", path, cookie, nil, ""); w.Code != 403 {
				t.Fatalf("%s can access %s: %d", role, path, w.Code)
			}
			if w := perform(f.app.Handler(), "POST", path, cookie, url.Values{"csrf_token": {csrf(t, account)}, "version": {"1"}, "status": {"maintenance"}, "basis": {"Акт"}}, ""); w.Code != 403 {
				t.Fatalf("%s can mutate %s: %d", role, path, w.Code)
			}
		}
		want := 200
		if role == "student" || role == "system_admin" {
			want = 403
		}
		page := perform(f.app.Handler(), "GET", bed, cookie, nil, "")
		if page.Code != want || (want == 200 && role != "commandant" && strings.Contains(page.Body.String(), `href="`+bed+`/status"`)) {
			t.Fatal("incorrect bed read permissions")
		}
	}
	for _, role := range []string{"registry_staff", "student", "system_admin", "commandant", "administration"} {
		cookie, _ := login(t, f, role)
		account := perform(f.app.Handler(), "GET", "/account", cookie, nil, "")
		wantGet, wantPost := 403, 403
		if role == "commandant" {
			wantGet, wantPost = 200, 422
		}
		if w := perform(f.app.Handler(), "GET", bed+"/status", cookie, nil, ""); w.Code != wantGet {
			t.Fatalf("%s status form: %d", role, w.Code)
		}
		if w := perform(f.app.Handler(), "POST", bed+"/status", cookie, url.Values{"csrf_token": {csrf(t, account)}, "version": {"1"}, "status": {"reserved"}, "basis": {"Акт"}}, ""); w.Code != wantPost {
			t.Fatalf("%s status POST: %d", role, w.Code)
		}
	}
	if w := perform(f.app.Handler(), "GET", strings.Replace(bed, "dev-dorm-1", "unassigned", 1), writer, nil, ""); w.Code != 403 {
		t.Fatal("cross-scope bed read")
	}
	statusWriter, _ := login(t, f, "commandant")
	form = perform(f.app.Handler(), "GET", bed+"/status", statusWriter, nil, "")
	if _, err := f.app.db.ExecContext(context.Background(), "DELETE FROM auth_user_dormitories WHERE user_id = ?", f.credentials["commandant"].UserID); err != nil {
		t.Fatal(err)
	}
	fields = url.Values{"csrf_token": {csrf(t, form)}, "version": {"1"}, "status": {"maintenance"}, "basis": {"Акт"}}
	if w := perform(f.app.Handler(), "POST", bed+"/status", statusWriter, fields, ""); w.Code != 403 {
		t.Fatal("revoked assignment can change bed status")
	}
}
