package httpserver

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

type languageKey struct{}

func Language(r *http.Request) string {
	if lang, _ := r.Context().Value(languageKey{}).(string); lang == "kk" {
		return "kk"
	}
	return "ru"
}
func Localization(next http.Handler, secure bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lang := "ru"
		if cookie, err := r.Cookie("dorm_language"); err == nil && (cookie.Value == "ru" || cookie.Value == "kk") {
			lang = cookie.Value
		}
		if selected := r.URL.Query().Get("lang"); selected == "ru" || selected == "kk" {
			lang = selected
			http.SetCookie(w, &http.Cookie{Name: "dorm_language", Value: lang, Path: "/", MaxAge: 31536000, Secure: secure, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		}
		r = r.WithContext(context.WithValue(r.Context(), languageKey{}, lang))
		next.ServeHTTP(&languageWriter{ResponseWriter: w, lang: lang}, r)
	})
}

type languageWriter struct {
	http.ResponseWriter
	lang string
}

func (w *languageWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *languageWriter) WriteHeader(status int) {
	if location := w.Header().Get("Location"); location != "" {
		w.Header().Set("Location", localURL(location, w.lang))
	}
	w.ResponseWriter.WriteHeader(status)
}
func localURL(raw, lang string) string {
	if lang != "kk" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Set("lang", lang)
	u.RawQuery = q.Encode()
	return u.String()
}
func languageURL(r *http.Request, lang string) string {
	u := *r.URL
	q := u.Query()
	q.Set("lang", lang)
	u.RawQuery = q.Encode()
	return u.RequestURI()
}

// Text translates only developer-owned interface strings; public and user-entered
// content is never passed to it. Missing keys retain the Russian source.
func Text(r *http.Request, source string) string { return translate(Language(r), source) }
func translate(lang, source string) string {
	if lang == "kk" {
		if text, ok := kazakh[source]; ok {
			return text
		}
	}
	return source
}
