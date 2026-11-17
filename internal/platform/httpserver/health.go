package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"student_dormitory/internal/platform/requestid"
)

// Pinger allows a real database to be replaced by a controlled probe in tests.
type Pinger interface {
	PingContext(context.Context) error
}

func Health(db Pinger, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := db.PingContext(ctx); err != nil {
			log.Error("health probe failed", "request_id", requestid.FromContext(r.Context()))
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("{\"status\":\"unavailable\"}\n"))
			return
		}
		w.Write([]byte("{\"status\":\"ok\"}\n"))
	})
}
