package app

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestAdministrativeKazakhAndLanguageAcrossMutations(t *testing.T) {
	f := newAuthFixture(t)
	writer, _ := login(t, f, "registry_staff")
	commandant, _ := login(t, f, "commandant")
	admin, _ := login(t, f, "system_admin")
	room := bedRoom(t, f, writer)
	bed := createInventoryObject(t, f, writer, room+"/beds/new", url.Values{"number": {"Свободно"}, "basis": {"Создание"}})
	for _, test := range []struct {
		path, content string
		cookie        *http.Cookie
	}{
		{"/", "Қор есебі және жатақханалар каталогы", writer},
		{"/account", "Менің кабинетім", writer},
		{"/admin/users", "Есептік жазбалар", admin},
		{"/staff/dormitories", "Жатақханалар тізілімі", writer},
		{"/staff/dormitories/dev-dorm-1", "Жатақхана Тестовое общежитие", writer},
		{room, "Санитарлық-тұрмыстық жағдайлар", writer},
		{room + "/edit", "Өзгерістерді сақтау", writer},
		{bed, "Өзгерістер тарихы", commandant},
		{bed + "/status", "Күйді өзгерту", commandant},
		{"/auth/login", "Сынақ кіруі", nil},
		{"/missing", "Бет табылмады", nil},
	} {
		w := perform(f.app.Handler(), "GET", test.path+"?lang=kk", test.cookie, nil, "")
		body := w.Body.String()
		if w.Header().Get("Content-Language") != "kk" || !strings.Contains(body, test.content) || !strings.Contains(body, `<html lang="kk">`) {
			t.Fatalf("Kazakh page %s (%d) missing %s", test.path, w.Code, test.content)
		}
		if test.cookie != nil && !strings.Contains(body, `action="/auth/logout?lang=kk"`) {
			t.Fatal("logout loses language")
		}
	}
	// Record contents that happen to match interface keys are never translated.
	w := perform(f.app.Handler(), "GET", bed+"?lang=kk", writer, nil, "")
	if !strings.Contains(w.Body.String(), "<h1>Төсек-орын Свободно</h1>") || !strings.Contains(w.Body.String(), ">Создание<") || !strings.Contains(w.Body.String(), ">Бос<") {
		t.Fatal("record contents translated or status not translated")
	}
	form := perform(f.app.Handler(), "GET", room+"/edit?lang=kk", writer, nil, "")
	fields := inventoryRoomFields()
	fields.Set("capacity", "0")
	fields.Set("csrf_token", csrf(t, form))
	fields.Set("version", version(t, form.Body.String()))
	w = perform(f.app.Handler(), "POST", room+"/edit?lang=kk", writer, fields, "")
	if w.Code != 422 || !strings.Contains(w.Body.String(), "Сыйымдылық нөлден үлкен бүтін сан болуы керек.") {
		t.Fatal("validation not localized")
	}
	fields.Set("capacity", "3")
	w = perform(f.app.Handler(), "POST", room+"/edit?lang=kk", writer, fields, "")
	target, err := url.Parse(w.Header().Get("Location"))
	if err != nil || w.Code != 303 || target.Query().Get("lang") != "kk" || target.Query().Get("saved") != "updated" {
		t.Fatal("mutation redirect lost language or state")
	}
	// Language also persists through login and its session rotation.
	loginPage := perform(f.app.Handler(), "GET", "/auth/login?lang=kk", nil, nil, "")
	var anonymous *http.Cookie
	for _, c := range loginPage.Result().Cookies() {
		if c.Name == "dorm_session" {
			anonymous = c
		}
	}
	if anonymous == nil {
		t.Fatal("no login session")
	}
	credential := f.credentials["student"]
	w = perform(f.app.Handler(), "POST", "/auth/login?lang=kk", anonymous, url.Values{"csrf_token": {csrf(t, loginPage)}, "login": {credential.Login}, "password": {credential.Password}}, "")
	if w.Code != 303 || w.Header().Get("Location") != "/account?lang=kk" {
		t.Fatal("login lost language")
	}
}
