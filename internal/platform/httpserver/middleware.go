package httpserver

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"student_dormitory/internal/platform/requestid"
)

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (w *responseWriter) WriteHeader(code int) {
	if w.status != 0 {
		return
	}
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

func Middleware(next http.Handler, log *slog.Logger, renderer *Renderer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		// Never trust or log an incoming request ID; it could contain credentials.
		id := requestid.New()
		r = r.WithContext(requestid.WithID(r.Context(), id))
		requestLog := log.With("request_id", id)
		recorder := &responseWriter{ResponseWriter: w}
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		policy := "default-src 'self'; img-src 'self'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'; connect-src 'self'"
		if r.URL.Path == "/auth/login" {
			policy += " wss://127.0.0.1:13579"
		}
		w.Header().Set("Content-Security-Policy", policy)
		defer func() {
			if recovered := recover(); recovered != nil {
				// Let net/http terminate a partially written response on panic.
				if recovered == http.ErrAbortHandler || recorder.status != 0 {
					requestLog.Error("request aborted", "stack", string(debug.Stack()))
					panic(http.ErrAbortHandler)
				}
				requestLog.Error("request panic", "stack", string(debug.Stack()))
				renderer.Error(recorder, r, http.StatusInternalServerError)
			}
			status := recorder.status
			if status == 0 {
				status = http.StatusOK
			}
			route := r.Pattern
			if route == "" {
				route = "unmatched"
			}
			requestLog.Info("http request", "method", logMethod(r.Method), "route", route, "status", status, "duration", time.Since(start))
		}()
		next.ServeHTTP(recorder, r)
	})
}

func logMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodConnect, http.MethodTrace:
		return method
	default:
		return "OTHER"
	}
}
