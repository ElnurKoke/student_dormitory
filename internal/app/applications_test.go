package app

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"student_dormitory/internal/modules/auth"
	"student_dormitory/internal/platform/requestid"
)

func applicationHidden(t *testing.T, body, name string) string {
	t.Helper()
	m := regexp.MustCompile(`name="` + name + `" value="([^"]*)"`).FindStringSubmatch(body)
	if len(m) != 2 {
		t.Fatalf("missing field %s", name)
	}
	return m[1]
}
func secondStudent(t *testing.T, f *authFixture, student *http.Cookie) *http.Cookie {
	t.Helper()
	ctx := requestid.WithID(context.Background(), "second-student-fixture")
	for _, q := range []string{`INSERT INTO auth_users(id,display_name) VALUES ('student-two','Другой студент')`, `INSERT INTO auth_user_roles VALUES ('student-two','student')`, `INSERT INTO auth_external_identities VALUES ('fixture','student-two','student-two')`} {
		if _, err := f.app.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	_, anon, err := f.service.Anonymous(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := f.service.Login(ctx, anon, fixtureVerifier{auth.Identity{Provider: "fixture", Subject: "student-two"}})
	if err != nil {
		t.Fatal(err)
	}
	c := *student
	c.Value = token
	return &c
}
func TestApplicationsStudentHTTPWorkflowAndIsolation(t *testing.T) {
	f := newAuthFixture(t)
	student, _ := login(t, f, "student")
	other := secondStudent(t, f, student)
	page := perform(f.app.Handler(), "GET", "/student/profile", student, nil, "")
	if page.Code != 200 {
		t.Fatal(page.Code)
	}
	profile := url.Values{"csrf_token": {csrf(t, page)}, "version": {"0"}, "name": {"Студент <script>alert('x')</script>"}, "email": {"private@example.test"}, "group": {"Группа 123"}, "user_id": {"student-two"}, "role": {"system_admin"}}
	if w := perform(f.app.Handler(), "POST", "/student/profile", student, profile, ""); w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	listing := perform(f.app.Handler(), "GET", "/applications", student, nil, "")
	create := url.Values{"csrf_token": {csrf(t, listing)}}
	if w := perform(f.app.Handler(), "POST", "/applications/new", student, nil, ""); w.Code != 403 {
		t.Fatal("no csrf", w.Code)
	}
	w := perform(f.app.Handler(), "POST", "/applications/new", student, create, "")
	if w.Code != 303 {
		t.Fatal(w.Code)
	}
	editPath := w.Header().Get("Location")
	path := strings.TrimSuffix(editPath, "/edit")
	edit := perform(f.app.Handler(), "GET", editPath, student, nil, "")
	if strings.Contains(edit.Body.String(), "<script>") || !strings.Contains(edit.Body.String(), "&lt;script&gt;") {
		t.Fatal("unescaped student name")
	}
	draft := url.Values{"csrf_token": {csrf(t, edit)}, "version": {applicationHidden(t, edit.Body.String(), "version")}, "name": {profile.Get("name")}, "email": {profile.Get("email")}, "group": {profile.Get("group")}, "request": {"Хочу общежитие <img src=x onerror=alert(1)>"}}
	if w := perform(f.app.Handler(), "POST", editPath, student, draft, ""); w.Code != 303 {
		t.Fatal(w.Code)
	}
	if w := perform(f.app.Handler(), "POST", editPath, student, draft, ""); w.Code != 409 {
		t.Fatal("stale draft", w.Code)
	}
	detail := perform(f.app.Handler(), "GET", path, student, nil, "")
	if detail.Code != 200 || strings.Contains(detail.Body.String(), "<img src=x") {
		t.Fatal("detail", detail.Code)
	}
	submission := url.Values{"csrf_token": {csrf(t, detail)}, "version": {applicationHidden(t, detail.Body.String(), "version")}, "submit_key": {applicationHidden(t, detail.Body.String(), "submit_key")}, "consent_version": {applicationHidden(t, detail.Body.String(), "consent_version")}, "accept_consent": {"on"}}
	for _, methodPath := range []struct{ method, path string }{{"GET", path}, {"GET", editPath}, {"POST", editPath}, {"POST", path + "/submit"}, {"POST", path + "/withdraw"}} {
		otherPage := perform(f.app.Handler(), "GET", "/applications", other, nil, "")
		form := url.Values{}
		for k, v := range submission {
			form[k] = v
		}
		form.Set("csrf_token", csrf(t, otherPage))
		w := perform(f.app.Handler(), methodPath.method, methodPath.path, other, form, "")
		if w.Code != 404 {
			t.Fatal("foreign access", methodPath, w.Code)
		}
	}
	otherList := perform(f.app.Handler(), "GET", "/applications", other, nil, "")
	if strings.Contains(otherList.Body.String(), path) {
		t.Fatal("foreign listing")
	}
	otherProfile := perform(f.app.Handler(), "GET", "/student/profile", other, nil, "")
	if strings.Contains(otherProfile.Body.String(), profile.Get("email")) {
		t.Fatal("profile user_id trusted")
	}
	for _, action := range []string{"submit", "withdraw"} {
		if w := perform(f.app.Handler(), "GET", path+"/"+action, student, nil, ""); w.Code != 405 {
			t.Fatal("get mutation", w.Code)
		}
		if w := perform(f.app.Handler(), "POST", path+"/"+action, student, nil, ""); w.Code != 403 {
			t.Fatal("csrf bypass", w.Code)
		}
	}
	for i := 0; i < 2; i++ {
		if w := perform(f.app.Handler(), "POST", path+"/submit", student, submission, ""); w.Code != 303 || w.Header().Get("Location") != path {
			t.Fatal("submit/replay", w.Code)
		}
	}
	registered := perform(f.app.Handler(), "GET", path, student, nil, "")
	if !strings.Contains(registered.Body.String(), "APP-00000001") || !strings.Contains(registered.Body.String(), "test-v1") {
		t.Fatal("registration confirmation missing")
	}
	withdraw := url.Values{"csrf_token": {csrf(t, registered)}, "version": {applicationHidden(t, registered.Body.String(), "version")}}
	if w := perform(f.app.Handler(), "POST", path+"/withdraw", student, withdraw, ""); w.Code != 303 {
		t.Fatal(w.Code)
	}
	after := perform(f.app.Handler(), "GET", path, student, nil, "")
	if !strings.Contains(after.Body.String(), "отозвано") || !strings.Contains(after.Body.String(), "APP-00000001") {
		t.Fatal("withdrawn record lost")
	}
	kk := perform(f.app.Handler(), "GET", "/applications?lang=kk", student, nil, "")
	if kk.Code != 200 || !strings.Contains(kk.Body.String(), "Менің өтініштерім") {
		t.Fatal("Kazakh navigation")
	}
	for _, secret := range []string{profile.Get("name"), profile.Get("email"), draft.Get("request"), submission.Get("submit_key")} {
		if strings.Contains(f.logs.String(), secret) {
			t.Fatal("sensitive data logged")
		}
	}
}
func TestApplicationsHTTPPermissionMatrix(t *testing.T) {
	f := newAuthFixture(t)
	for _, role := range []string{"student", "registry_staff", "commandant", "administration", "system_admin"} {
		cookie, _ := login(t, f, role)
		account := perform(f.app.Handler(), "GET", "/account", cookie, nil, "")
		if strings.Contains(account.Body.String(), `href="/applications"`) != (role == "student") {
			t.Fatal("navigation permission", role)
		}
		for _, path := range []string{"/applications", "/student/profile", "/applications/unknown", "/applications/unknown/edit"} {
			w := perform(f.app.Handler(), "GET", path, cookie, nil, "")
			want := 403
			if role == "student" {
				want = 200
				if strings.Contains(path, "unknown") {
					want = 404
				}
			}
			if w.Code != want {
				t.Fatal(role, path, w.Code, want)
			}
		}
		for _, path := range []string{"/applications/new", "/student/profile", "/applications/unknown/edit", "/applications/unknown/submit", "/applications/unknown/withdraw"} {
			form := url.Values{"csrf_token": {csrf(t, account)}, "version": {"1"}}
			w := perform(f.app.Handler(), "POST", path, cookie, form, "")
			if role != "student" && w.Code != 403 {
				t.Fatal("permission bypass", role, path, w.Code)
			}
		}
	}
	for _, path := range []string{"/student/profile", "/applications", "/applications/unknown", "/applications/unknown/edit"} {
		if w := perform(f.app.Handler(), "GET", path, nil, nil, ""); w.Code != 401 {
			t.Fatal("guest access", path, w.Code)
		}
	}
}
