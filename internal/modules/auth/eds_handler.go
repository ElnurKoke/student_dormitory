package auth

import (
	"encoding/json"
	"errors"
	"net/http"

	"student_dormitory/internal/platform/httpserver"
	"student_dormitory/internal/platform/nca"
)

type loginView struct{ EDSEnabled, DevelopmentEnabled bool }

type accountView struct {
	User
	Certificate *CertificateProfile
}

func (h *Handler) loginPage(w http.ResponseWriter, r *http.Request, status int, token, login, message string) {
	title := "Вход"
	if h.eds == nil && h.development != nil {
		title = "Тестовый вход"
	}
	h.renderer.Render(w, r, status, "login", httpserver.Page{Title: title, CSRFToken: token, LoginValue: login, Message: message,
		Data: loginView{EDSEnabled: h.eds != nil, DevelopmentEnabled: h.development != nil}})
}

func edsJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func (h *Handler) edsFailure(w http.ResponseWriter, r *http.Request, err error) {
	status, message := 500, "Не удалось выполнить вход. Повторите попытку."
	switch {
	case errors.Is(err, nca.ErrUnavailable):
		status, message = 503, "Сервис проверки ЭЦП временно недоступен. Повторите попытку позже."
	case errors.Is(err, nca.ErrInvalidSignature), errors.Is(err, ErrCredentials), errors.Is(err, ErrUnauthenticated):
		status, message = 401, "Подпись не принята. Проверьте сертификат и повторите вход с новым запросом."
	case errors.Is(err, ErrForbidden):
		status, message = 403, "Обновите страницу входа и повторите попытку."
	}
	edsJSON(w, status, map[string]string{"message": httpserver.Text(r, message)})
}

func (h *Handler) EDSChallenge(w http.ResponseWriter, r *http.Request) {
	if h.eds == nil {
		h.fail(w, r, ErrUnavailable)
		return
	}
	challenge, err := h.eds.Challenge(r.Context(), h.token(r))
	if err != nil {
		h.edsFailure(w, r, err)
		return
	}
	edsJSON(w, 200, challenge)
}

func (h *Handler) EDSLogin(w http.ResponseWriter, r *http.Request) {
	if h.eds == nil {
		h.fail(w, r, ErrUnavailable)
		return
	}
	session, token, err := h.eds.Login(r.Context(), h.token(r), r.PostForm.Get("cms"))
	if err != nil {
		h.edsFailure(w, r, err)
		return
	}
	h.setCookie(w, session, token)
	edsJSON(w, 200, map[string]string{"redirect": "/account"})
}
