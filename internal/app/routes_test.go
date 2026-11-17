package app

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Exercise each route registered by newHandler, including every inventory level.
// Successful state changes are covered by the inventory, bed and photo tests.
func TestAllRegisteredRoutes(t *testing.T) {
	f := newAuthFixture(t)
	writer, _ := login(t, f, "registry_staff")
	commandant, _ := login(t, f, "commandant")
	admin, _ := login(t, f, "system_admin")
	if _, err := f.app.db.ExecContext(context.Background(), "INSERT INTO auth_user_dormitories (user_id, dormitory_id) VALUES (?, ?)", f.credentials["registry_staff"].UserID, "dev-dorm-2"); err != nil {
		t.Fatal(err)
	}
	root := "/staff/dormitories/dev-dorm-1"
	building := createInventoryObject(t, f, writer, root+"/buildings/new", url.Values{"name": {"Корпус"}})
	floor := createInventoryObject(t, f, writer, building+"/floors/new", url.Values{"number": {"1"}})
	room := createInventoryObject(t, f, writer, floor+"/rooms/new", inventoryRoomFields())
	bed := createInventoryObject(t, f, writer, room+"/beds/new", url.Values{"number": {"1"}, "basis": {"Ввод в эксплуатацию"}})
	type route struct {
		path, methods string
		cookie        *http.Cookie
		getStatus     int
	}
	read, form := "GET, HEAD", "GET, HEAD, POST"
	routes := []route{
		{"/", read, nil, 200},
		{"/healthz", read, nil, 200},
		{"/static/app.css", read, nil, 200},
		{"/auth/login", form, nil, 200},
		{"/auth/logout", "POST", writer, 405},
		{"/account", read, writer, 200},
		{"/admin/users", read, admin, 200},
		{"/photos/missing", read, nil, 404},
		{"/staff/photos/missing", read, writer, 404},
		{"/staff/dormitories", read, writer, 200},
		{bed + "/status", form, commandant, 200},
	}
	for _, level := range []struct{ collection, detail string }{
		{"/staff/dormitories", root},
		{root + "/buildings", building},
		{building + "/floors", floor},
		{floor + "/rooms", room},
		{room + "/beds", bed},
	} {
		routes = append(routes,
			route{level.collection + "/new", form, writer, 200},
			route{level.detail, read, writer, 200},
			route{level.detail + "/edit", form, writer, 200},
			route{level.detail + "/archive", "POST", writer, 405},
			route{level.detail + "/restore", "POST", writer, 405},
		)
	}
	for _, owner := range []string{root, room} {
		for _, suffix := range []string{"/photos", "/photos/missing/edit", "/photos/missing/delete"} {
			routes = append(routes, route{owner + suffix, "POST", writer, 405})
		}
	}
	for _, rt := range routes {
		t.Run(rt.path, func(t *testing.T) {
			for _, method := range []string{"GET", "HEAD"} {
				w := perform(f.app.Handler(), method, rt.path, rt.cookie, nil, "")
				if w.Code != rt.getStatus {
					t.Fatalf("%s: status %d, want %d", method, w.Code, rt.getStatus)
				}
				if rt.getStatus == 200 && strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") && !strings.Contains(w.Body.String(), `aria-label="Основная навигация"`) {
					t.Fatal("page is missing shared navigation")
				}
			}
			w := perform(f.app.Handler(), "DELETE", rt.path, rt.cookie, nil, "")
			if w.Code != 405 || w.Header().Get("Allow") != rt.methods {
				t.Fatalf("unsupported method: status %d, Allow %q", w.Code, w.Header().Get("Allow"))
			}
			if strings.Contains(rt.methods, "POST") {
				if w := perform(f.app.Handler(), "POST", rt.path, nil, url.Values{}, ""); w.Code != 401 {
					t.Fatalf("anonymous POST: status %d", w.Code)
				}
				cookie := rt.cookie
				if cookie == nil {
					cookie = writer
				}
				if w := perform(f.app.Handler(), "POST", rt.path, cookie, url.Values{}, ""); w.Code != 403 {
					t.Fatalf("POST without CSRF: status %d", w.Code)
				}
			}
		})
	}
	t.Logf("checked %d registered routes", len(routes))
}

func TestNavigationToDormitoryNotYetCreated(t *testing.T) {
	f := newAuthFixture(t)
	path := "/staff/dormitories/dev-dorm-2"
	for _, role := range []string{"registry_staff", "commandant", "administration"} {
		if _, err := f.app.db.ExecContext(context.Background(), "INSERT INTO auth_user_dormitories (user_id, dormitory_id) VALUES (?, ?)", f.credentials[role].UserID, "dev-dorm-2"); err != nil {
			t.Fatal(err)
		}
		cookie, _ := login(t, f, role)
		home := perform(f.app.Handler(), "GET", "/", cookie, nil, "")
		if !strings.Contains(home.Body.String(), `href="`+path+`"`) {
			t.Fatal("assigned dormitory is missing from navigation")
		}
		for _, method := range []string{"GET", "HEAD"} {
			w := perform(f.app.Handler(), method, path, cookie, nil, "")
			target := "/staff/dormitories?pending=1"
			message := "Назначенное общежитие ещё не внесено в реестр"
			if role == "registry_staff" {
				target = "/staff/dormitories/new?scope=dev-dorm-2"
				message = `value="dev-dorm-2"`
			}
			if w.Code != 303 || w.Header().Get("Location") != target {
				t.Fatalf("%s %s: status %d, Location %q", role, method, w.Code, w.Header().Get("Location"))
			}
			page := perform(f.app.Handler(), method, target, cookie, nil, "")
			if page.Code != 200 || !strings.Contains(page.Body.String(), message) {
				t.Fatalf("%s: navigation did not reach a useful page", role)
			}
		}
	}
	for _, role := range []string{"student", "system_admin"} {
		cookie, _ := login(t, f, role)
		if w := perform(f.app.Handler(), "GET", path, cookie, nil, ""); w.Code != 403 {
			t.Fatalf("%s bypassed permission check: %d", role, w.Code)
		}
	}
	reader, _ := login(t, f, "commandant")
	if w := perform(f.app.Handler(), "GET", "/staff/dormitories/unassigned", reader, nil, ""); w.Code != 403 {
		t.Fatal("unassigned dormitory bypassed permission check")
	}
}
