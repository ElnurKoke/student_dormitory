package app

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"image"
	"image/png"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"student_dormitory/internal/modules/inventory"
)

func TestCatalogHTTPUsesRegistryAndPublicAllowlist(t *testing.T) {
	f := newAuthFixture(t)
	cookie, _ := login(t, f, "registry_staff")
	commandant, _ := login(t, f, "commandant")
	room := bedRoom(t, f, cookie)
	bed := createInventoryObject(t, f, cookie, room+"/beds/new", url.Values{"number": {"PRIVATE-BED"}, "basis": {"PRIVATE-REASON"}})
	ctx := context.Background()
	if _, err := f.app.db.ExecContext(ctx, `UPDATE inventory_dormitories SET published=1,description='<script>Public description</script>'`); err != nil {
		t.Fatal(err)
	}
	var pixels bytes.Buffer
	if err := png.Encode(&pixels, image.NewRGBA(image.Rect(0, 0, 4, 3))); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/staff/dormitories/dev-dorm-1", room} {
		page := perform(f.app.Handler(), "GET", path, cookie, nil, "")
		if w := photoRequest(t, f, cookie, path+"/photos", csrf(t, page), pixels.Bytes(), ""); w.Code != 303 {
			t.Fatal("upload", w.Code)
		}
	}
	var storage string
	if err := f.app.db.QueryRowContext(ctx, `SELECT storage_key FROM inventory_photos LIMIT 1`).Scan(&storage); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/catalog", "/catalog/dev-dorm-1"} {
		for _, session := range []bool{false, true} {
			c := cookie
			if !session {
				c = nil
			}
			w := perform(f.app.Handler(), "GET", path, c, nil, "")
			body := w.Body.String()
			if w.Code != 200 || !strings.Contains(body, "Свободных мест") || !strings.Contains(body, "12345 KZT") || !strings.Contains(body, "/photos/") {
				t.Fatalf("catalogue failed: %d %s", w.Code, body)
			}
			if strings.Contains(body, `href="/auth/login"`) == session || strings.Contains(body, `action="/auth/logout"`) != session || strings.Contains(body, `href="/account"`) != session {
				t.Fatalf("catalogue navigation does not reflect session on %s", path)
			}
			if !session {
				for _, private := range []string{`name="csrf_token"`, `/staff/`, "registry_staff"} {
					if strings.Contains(body, private) {
						t.Fatalf("guest catalogue contains session navigation %q", private)
					}
				}
			}
			for _, secret := range []string{"PRIVATE-BED", "PRIVATE-REASON", "Тестовый оператор", "Тестовое основание", "Тестовые контакты", storage, f.credentials["registry_staff"].UserID, "<script>Public description"} {
				if strings.Contains(body, secret) {
					t.Fatalf("public HTML leaked %q", secret)
				}
			}
		}
	}
	detail := perform(f.app.Handler(), "GET", "/catalog/dev-dorm-1", nil, nil, "")
	if !strings.Contains(detail.Body.String(), "&lt;script&gt;Public description&lt;/script&gt;") {
		t.Fatal("public content not escaped")
	}
	form := perform(f.app.Handler(), "GET", bed+"/status", commandant, nil, "")
	changed := perform(f.app.Handler(), "POST", bed+"/status", commandant, url.Values{"csrf_token": {csrf(t, form)}, "version": {version(t, form.Body.String())}, "status": {"maintenance"}, "basis": {"PRIVATE-REPAIR"}}, "")
	if changed.Code != 303 {
		t.Fatal("status change failed")
	}
	service := inventory.NewPublicService(inventory.NewRepository(f.app.db))
	list, err := service.List(ctx, inventory.PublicFilter{Page: 1})
	if err != nil || list.Items[0].Free != 0 || list.Items[0].Total != 1 {
		t.Fatal("catalogue disagrees with registry", err)
	}
	empty := perform(f.app.Handler(), "GET", "/catalog?free=1", nil, nil, "")
	if empty.Code != 200 || !strings.Contains(empty.Body.String(), "Общежития не найдены") {
		t.Fatal("free filter not live")
	}
	if _, err := f.app.db.ExecContext(ctx, `UPDATE inventory_dormitories SET published=0`); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "HEAD"} {
		if w := perform(f.app.Handler(), method, "/catalog/dev-dorm-1", cookie, nil, ""); w.Code != 404 {
			t.Fatal("unpublished detail exposed")
		}
	}
	for _, path := range []string{"/catalog", "/catalog/dev-dorm-1"} {
		if w := perform(f.app.Handler(), "POST", path, cookie, nil, ""); w.Code != 405 || w.Header().Get("Allow") != "GET, HEAD" {
			t.Fatal("catalogue permits mutations")
		}
	}
}

func TestCatalogFilterErrorsAndLanguage(t *testing.T) {
	f := newAuthFixture(t)
	for _, query := range []string{"page=0", "page=-1", "page=1000001", "page=abc", "min=-1", "min=1.5", "min=9223372036854775808", "min=1", "min=2&max=1&currency=KZT&period=month", "free=yes", "currency=BADCODE", "accessible=on"} {
		w := perform(f.app.Handler(), "GET", "/catalog?"+query+"&lang=kk", nil, nil, "")
		if w.Code != 400 || !strings.Contains(w.Body.String(), "Сүзгілерді тексеріңіз") {
			t.Fatalf("invalid filter %s: %d", query, w.Code)
		}
	}
	w := perform(f.app.Handler(), "GET", "/catalog?name=Алматы&address=Көше&lang=kk", nil, nil, "")
	if w.Code != 200 || w.Header().Get("Content-Language") != "kk" || !strings.Contains(w.Body.String(), `<html lang="kk">`) || !strings.Contains(w.Body.String(), "Жатақханалар каталогы") || !strings.Contains(w.Body.String(), `value="Алматы"`) || !strings.Contains(w.Body.String(), `href="/catalog?lang=kk"`) {
		t.Fatal("Kazakh catalogue or links missing")
	}
	language := responseCookie(t, w)
	if language.Name != "dorm_language" || language.Value != "kk" || !language.HttpOnly {
		t.Fatal("language not persisted")
	}
	w = perform(f.app.Handler(), "GET", "/catalog", language, nil, "")
	if w.Header().Get("Content-Language") != "kk" {
		t.Fatal("lost language cookie")
	}
	w = perform(f.app.Handler(), "GET", "/catalog?lang=ru", language, nil, "")
	if !strings.Contains(w.Body.String(), "Каталог общежитий") || w.Header().Get("Content-Language") != "ru" {
		t.Fatal("cannot switch back to Russian")
	}
}

func TestCatalogKeepsFiltersInPaginationAndCardLinks(t *testing.T) {
	f := newAuthFixture(t)
	for i := 0; i < 13; i++ {
		_, err := f.app.db.ExecContext(context.Background(), `INSERT INTO inventory_dormitories(id,name,address,operator,use_basis,contacts,published) VALUES(?,'Пагинация','Алматы','private','private','private',1)`, fmt.Sprintf("pagination-%02d", i))
		if err != nil {
			t.Fatal(err)
		}
	}
	q := url.Values{"name": {"Пагинация"}, "address": {"Алматы"}, "lang": {"kk"}}
	page := perform(f.app.Handler(), "GET", "/catalog?"+q.Encode(), nil, nil, "")
	next := regexp.MustCompile(`rel="next" href="([^"]+)"`).FindStringSubmatch(page.Body.String())
	if len(next) != 2 {
		t.Fatal("no next page")
	}
	u, err := url.Parse(html.UnescapeString(next[1]))
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("page") != "2" || u.Query().Get("name") != "Пагинация" || u.Query().Get("address") != "Алматы" || u.Query().Get("lang") != "kk" {
		t.Fatal("filters lost during pagination")
	}
	page = perform(f.app.Handler(), "GET", u.RequestURI(), nil, nil, "")
	links := regexp.MustCompile(`href="(/catalog/pagination-[^"]+)"`).FindStringSubmatch(page.Body.String())
	if page.Code != 200 || len(links) != 2 {
		t.Fatal("no card on second page")
	}
	u, err = url.Parse(html.UnescapeString(links[1]))
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("page") != "2" || u.Query().Get("name") != "Пагинация" {
		t.Fatal("card loses source filters")
	}
	page = perform(f.app.Handler(), "GET", u.RequestURI(), nil, nil, "")
	back := regexp.MustCompile(`href="(/catalog\?[^"]+)"[^>]*>Каталогқа`).FindStringSubmatch(page.Body.String())
	if page.Code != 200 || len(back) != 2 {
		t.Fatal("no return to filtered catalogue")
	}
	u, err = url.Parse(html.UnescapeString(back[1]))
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("page") != "2" || u.Query().Get("address") != "Алматы" || u.Query().Get("name") != "Пагинация" || u.Query().Get("lang") != "kk" {
		t.Fatal("return loses filters")
	}
	if err := f.app.Close(); err != nil {
		t.Fatal(err)
	}
	page = perform(f.app.Handler(), "GET", "/catalog?lang=kk", nil, nil, "")
	if page.Code != 500 || !strings.Contains(page.Body.String(), "Сервердің ішкі қатесі") || strings.Contains(page.Body.String(), "database") {
		t.Fatal("database failure not handled safely")
	}
}
