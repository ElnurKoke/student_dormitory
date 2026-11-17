package httpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"student_dormitory/internal/platform/requestid"
	"student_dormitory/web"
)

func TestPanicResponse(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	renderer, err := NewRenderer(web.FS, log)
	if err != nil {
		t.Fatal(err)
	}
	handler := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("private diagnostic")
	}), log, renderer)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 500 || !strings.Contains(w.Body.String(), "Ошибка 500") || strings.Contains(w.Body.String(), "private diagnostic") {
		t.Fatalf("unexpected panic response: %d %s", w.Code, w.Body.String())
	}
}

func TestRequestIDAndPrivateDataNotLogged(t *testing.T) {
	for _, panics := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "panic"}[panics], func(t *testing.T) {
			var output bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&output, nil))
			renderer, err := NewRenderer(web.FS, log)
			if err != nil {
				t.Fatal(err)
			}
			const secret = "DO-NOT-LOG-credential-profile-document"
			var ids []string
			handler := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				id := requestid.FromContext(r.Context())
				ids = append(ids, id)
				if panics {
					panic(secret)
				}
				w.WriteHeader(http.StatusNoContent)
			}), log, renderer)
			for i := 0; i < 2; i++ {
				r := httptest.NewRequest("POST", "/"+secret+"?token="+secret, strings.NewReader(secret))
				r.Header.Set("Authorization", "Bearer "+secret)
				r.Header.Set("Cookie", "session="+secret)
				r.Header.Set("X-Request-ID", secret)
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if ids[i] == "" || w.Header().Get("X-Request-ID") != ids[i] {
					t.Fatal("request ID did not reach context and response")
				}
			}
			if ids[0] == ids[1] {
				t.Fatal("request ID reused")
			}
			if strings.Contains(output.String(), secret) {
				t.Fatalf("sensitive request/panic data leaked: %s", output.String())
			}
			decoder := json.NewDecoder(&output)
			for decoder.More() {
				var entry map[string]any
				if err := decoder.Decode(&entry); err != nil {
					t.Fatal(err)
				}
				if entry["request_id"] != ids[0] && entry["request_id"] != ids[1] {
					t.Fatal("missing log correlation")
				}
				if entry["msg"] == "http request" && entry["route"] != "unmatched" {
					t.Fatal("raw URL logged")
				}
			}
		})
	}
}
