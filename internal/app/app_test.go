package app

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"student_dormitory/internal/platform/config"
)

func TestRoutesAndDatabaseFailure(t *testing.T) {
	dir := t.TempDir()
	a, err := New(context.Background(), config.Config{
		DatabasePath: filepath.Join(dir, "database.db"), FilesDir: filepath.Join(dir, "files"), AppEnv: "development", SessionTTL: 8 * time.Hour,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	var users int
	if err := a.db.QueryRowContext(context.Background(), "SELECT count(*) FROM auth_users").Scan(&users); err != nil || users != 0 {
		t.Fatal("server implicitly created test users")
	}
	for _, test := range []struct {
		method, path          string
		status                int
		contentType, contains string
	}{
		{"GET", "/", 200, "text/html", "Учёт фонда и каталог общежитий"},
		{"GET", "/healthz", 200, "application/json", `{"status":"ok"}`},
		{"HEAD", "/", 200, "text/html", ""},
		{"GET", "/static/app.css", 200, "text/css", ":root"},
		{"GET", "/missing", 404, "text/html", "Страница не найдена"},
		{"GET", "/static/", 404, "text/html", "Страница не найдена"},
		{"POST", "/", 405, "text/html", "Метод не поддерживается"},
	} {
		t.Run(test.method+test.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			a.Handler().ServeHTTP(w, httptest.NewRequest(test.method, test.path, nil))
			if w.Code != test.status || !strings.HasPrefix(w.Header().Get("Content-Type"), test.contentType) || !strings.Contains(w.Body.String(), test.contains) {
				t.Fatalf("unexpected response: %d %v %s", w.Code, w.Header(), w.Body.String())
			}
			if test.status == 405 && w.Header().Get("Allow") != "GET, HEAD" {
				t.Fatal("missing Allow header")
			}
		})
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 503 || strings.Contains(w.Body.String(), "database") {
		t.Fatalf("unexpected health failure: %d %s", w.Code, w.Body.String())
	}
}
