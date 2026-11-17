package app

import (
	"context"
	"encoding/csv"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func reportData(t *testing.T, f *authFixture) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO inventory_dormitories(id,name,address,operator,use_basis,contacts) VALUES('foreign-dorm','FOREIGN-DORM-SECRET','FOREIGN-ADDRESS','FOREIGN-OPERATOR','FOREIGN-BASIS','FOREIGN-CONTACT')`,
		`UPDATE inventory_dormitories SET name='Алатау, Қазақша',description='=HYPERLINK("example")' WHERE id='dev-dorm-1'`,
		`INSERT INTO inventory_buildings(id,dormitory_id,name) VALUES('report-b','dev-dorm-1','Корпус')`,
		`INSERT INTO inventory_floors(id,building_id,number) VALUES('report-f','report-b','1')`,
		`INSERT INTO inventory_rooms(id,floor_id,number,capacity,area_hundredths,equipment,sanitary,accessible,price_minor,currency,price_period) VALUES('report-r','report-f','101',5,2025,'Стол','Душ',1,9007199254740993,'KZT','месяц')`,
		`INSERT INTO inventory_beds(id,room_id,number,status) VALUES('report-p','report-r','1','free')`,
		`INSERT INTO audit_events(actor_id,action,object_type,object_id,result,request_id,dormitory_id,changes_json) VALUES('FOREIGN-ACTOR','inventory.room.updated','inventory_room','FOREIGN-OBJECT','success','FOREIGN-REQUEST','foreign-dorm','{"after":{"description":"FOREIGN-DIFF"}}')`,
		`INSERT INTO audit_events(actor_id,action,object_type,object_id,result,request_id,dormitory_id,changes_json) VALUES('LOCAL-ACTOR','inventory.room.updated','inventory_room','report-r','success','LOCAL-REQUEST','dev-dorm-1','{"before":{"equipment":"Стол"},"after":{"equipment":"<script>өзгеріс</script>","price_minor":9007199254740993,"password":"PAYLOAD-SECRET","storage_key":"STORAGE-SECRET"}}')`,
	} {
		if _, err := f.app.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
}
func TestReportRoutesRespectEveryRoleAndScope(t *testing.T) {
	f := newAuthFixture(t)
	reportData(t, f)
	for _, role := range []string{"student", "registry_staff", "commandant", "administration", "system_admin"} {
		cookie, _ := login(t, f, role)
		for _, path := range []string{"/staff/statistics", "/staff/registry-export", "/staff/registry.csv", "/staff/history"} {
			want := 403
			if role == "administration" || role == "registry_staff" && (path == "/staff/registry-export" || path == "/staff/registry.csv") {
				want = 200
			}
			for _, method := range []string{"GET", "HEAD"} {
				w := perform(f.app.Handler(), method, path, cookie, nil, "")
				if w.Code != want {
					t.Fatalf("%s %s as %s: %d want %d; %s", method, path, role, w.Code, want, w.Body.String())
				}
				body := w.Body.String()
				for _, secret := range []string{"FOREIGN-", "PAYLOAD-SECRET", "STORAGE-SECRET"} {
					if strings.Contains(body, secret) {
						t.Fatalf("report leaked %s as %s", secret, role)
					}
				}
				if w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("closed data can be cached")
				}
			}
			w := perform(f.app.Handler(), "GET", path+"?dormitory=foreign-dorm", cookie, nil, "")
			if w.Code != 403 {
				t.Fatalf("foreign scope allowed %s %s: %d", role, path, w.Code)
			}
			for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
				if w := perform(f.app.Handler(), method, path, cookie, nil, ""); w.Code != 405 || w.Header().Get("Allow") != "GET, HEAD" {
					t.Fatalf("report permits mutation %s %s", method, path)
				}
			}
		}
		home := perform(f.app.Handler(), "GET", "/account", cookie, nil, "").Body.String()
		if strings.Contains(home, `href="/staff/statistics"`) != (role == "administration") || strings.Contains(home, `href="/staff/history"`) != (role == "administration") || strings.Contains(home, `href="/staff/registry-export"`) != (role == "administration" || role == "registry_staff") {
			t.Fatal("navigation does not reflect report grants", role)
		}
	}
	for _, path := range []string{"/staff/statistics", "/staff/registry-export", "/staff/registry.csv", "/staff/history"} {
		if w := perform(f.app.Handler(), "GET", path, nil, nil, ""); w.Code != 401 {
			t.Fatal("anonymous report access", path, w.Code)
		}
	}
}
func TestCSVFiltersEncodingPaginationAndRevocation(t *testing.T) {
	f := newAuthFixture(t)
	reportData(t, f)
	cookie, _ := login(t, f, "registry_staff")
	// More than one preview page; CSV must include the complete filtered set.
	for i := 0; i < 55; i++ {
		if _, err := f.app.db.ExecContext(context.Background(), `INSERT INTO inventory_rooms(id,floor_id,number,capacity,area_hundredths,equipment,sanitary,accessible,price_minor,currency,price_period) VALUES(?,? ,?,2,1000,'Жабдық','Душ',0,100,'KZT','ай')`, fmt.Sprintf("page-r-%02d", i), "report-f", fmt.Sprint(200+i)); err != nil {
			t.Fatal(err)
		}
	}
	preview := perform(f.app.Handler(), "GET", "/staff/registry-export?kind=room&q=Алатау&lang=kk", cookie, nil, "")
	body := preview.Body.String()
	if preview.Code != 200 || !strings.Contains(body, "Тізілімді экспорттау") || !strings.Contains(body, `name="lang" value="kk"`) {
		t.Fatalf("export preview failed %d %s", preview.Code, body)
	}
	match := regexp.MustCompile(`href="(/staff/registry.csv[^"]*)"`).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatal("no filtered download link")
	}
	download := html.UnescapeString(match[1])
	parsed, err := url.Parse(download)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Query().Get("kind") != "room" || parsed.Query().Get("q") != "Алатау" || parsed.Query().Get("lang") != "kk" || parsed.Query().Get("page") != "" {
		t.Fatal("download loses filter/language")
	}
	w := perform(f.app.Handler(), "GET", download, cookie, nil, "")
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/csv; charset=utf-8" || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment;") || !strings.HasPrefix(w.Body.String(), "\ufeff") {
		t.Fatal("wrong CSV headers or encoding")
	}
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(w.Body.String(), "\ufeff"))).ReadAll()
	if err != nil || len(rows) != 57 {
		t.Fatalf("CSV page truncated: %d %v", len(rows), err)
	}
	fields := map[string]int{}
	for i, key := range rows[0] {
		fields[key] = i
	}
	found := false
	for _, row := range rows[1:] {
		if row[fields["dormitory_name"]] != "Алатау, Қазақша" || row[fields["kind"]] != "room" {
			t.Fatal("CSV filter/text mismatch")
		}
		if row[fields["id"]] == "report-r" {
			found = true
			if row[fields["price_minor"]] != "9007199254740993" {
				t.Fatal("CSV money lost precision")
			}
		}
	}
	if !found {
		t.Fatal("CSV lost base room")
	}
	w = perform(f.app.Handler(), "GET", "/staff/registry.csv?kind=dormitory", cookie, nil, "")
	rows, err = csv.NewReader(strings.NewReader(strings.TrimPrefix(w.Body.String(), "\ufeff"))).ReadAll()
	if err != nil || len(rows) != 2 || rows[1][fields["description"]] != "'=HYPERLINK(\"example\")" {
		t.Fatal("CSV formula not neutralized", err)
	}
	if _, err := f.app.db.ExecContext(context.Background(), `DELETE FROM auth_user_dormitories WHERE user_id=?`, f.credentials["registry_staff"].UserID); err != nil {
		t.Fatal(err)
	}
	w = perform(f.app.Handler(), "GET", "/staff/registry.csv", cookie, nil, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "Алатау") || strings.Contains(w.Body.String(), "Жабдық") {
		t.Fatal("revoked scope still exported")
	}
	w = perform(f.app.Handler(), "GET", "/staff/registry.csv?dormitory=dev-dorm-1", cookie, nil, "")
	if w.Code != 403 {
		t.Fatal("selected revoked scope exported")
	}
}
func TestHistoryFiltersEscapingAndReadOnly(t *testing.T) {
	f := newAuthFixture(t)
	reportData(t, f)
	cookie, _ := login(t, f, "administration")
	q := url.Values{"actor": {"LOCAL-ACTOR"}, "action": {"inventory.room.updated"}, "type": {"inventory_room"}, "object": {"report-r"}, "lang": {"kk"}}
	w := perform(f.app.Handler(), "GET", "/staff/history?"+q.Encode(), cookie, nil, "")
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, "Тізілім өзгерістерінің тарихы") || !strings.Contains(body, "&lt;script&gt;өзгеріс&lt;/script&gt;") || strings.Contains(body, "<script>өзгеріс") || !strings.Contains(body, "9007199254740993") {
		t.Fatal("history display or escaping failed", w.Code)
	}
	for _, secret := range []string{"FOREIGN-", "PAYLOAD-SECRET", "STORAGE-SECRET", `auth.login`, `auth_session`} {
		if strings.Contains(body, secret) {
			t.Fatal("history exposes out-of-scope or technical payload", secret)
		}
	}
	empty := perform(f.app.Handler(), "GET", "/staff/history?actor=not-present", cookie, nil, "")
	if empty.Code != 200 || !strings.Contains(empty.Body.String(), "События не найдены") {
		t.Fatal("history filter ignored")
	}
	for _, path := range []string{"/staff/history?page=0", "/staff/history?from=2026-02-30", "/staff/history?from=2026-11-20&to=2026-11-01", "/staff/registry.csv?status=INVALID", "/staff/registry-export?state=INVALID", "/staff/statistics?state=all"} {
		if w := perform(f.app.Handler(), "GET", path, cookie, nil, ""); w.Code != 400 {
			t.Fatal("invalid filter accepted", path, w.Code)
		}
	}
	if _, err := f.app.db.ExecContext(context.Background(), `DELETE FROM auth_user_dormitories WHERE user_id=?`, f.credentials["administration"].UserID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/staff/history", "/staff/statistics"} {
		w = perform(f.app.Handler(), "GET", path, cookie, nil, "")
		if w.Code != 200 || strings.Contains(w.Body.String(), "LOCAL-ACTOR") || strings.Contains(w.Body.String(), "Алатау") {
			t.Fatal("revoked scope still present")
		}
	}
}
