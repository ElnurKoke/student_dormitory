package httpserver

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"student_dormitory/web"
	"testing"
)

func TestInterfaceTranslationCoverage(t *testing.T) {
	pattern := regexp.MustCompile(`{{t ("(?:[^"\\]|\\.)*")}}`)
	err := fs.WalkDir(web.FS, "templates", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		body, err := fs.ReadFile(web.FS, path)
		if err != nil {
			return err
		}
		for _, match := range pattern.FindAllStringSubmatch(string(body), -1) {
			key, err := strconv.Unquote(match[1])
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := kazakh[key]; !ok {
				t.Errorf("missing translation %q in %s", key, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestLanguageSecurityAndRedirects(t *testing.T) {
	h := Localization(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/account?saved=updated#photos", 303)
	}), true)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "https://example.test/?lang=kk", nil))
	if got := w.Header().Get("Location"); got != "/account?lang=kk&saved=updated#photos" {
		t.Fatal("redirect", got)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("unsafe language cookie")
	}
	for _, raw := range []string{"https://external.test/path", "//external.test/path", "#section"} {
		if localURL(raw, "kk") != raw {
			t.Fatal("nonlocal link changed")
		}
	}
}
