package applications

import (
	"errors"
	"net/http"
	"strconv"
	"student_dormitory/internal/modules/auth"
	"student_dormitory/internal/platform/httpserver"
)

type ActorSource interface {
	ActorID(*http.Request) (string, error)
	CSRFToken(*http.Request) (string, error)
}
type Handler struct {
	s        *Service
	actor    ActorSource
	renderer *httpserver.Renderer
}

func NewHandler(s *Service, a ActorSource, r *httpserver.Renderer) *Handler { return &Handler{s, a, r} }

type view struct {
	Documents             []Document
	Reviews               []Review
	Snapshots             []Snapshot
	Checks                Checks
	Staff, AssignMode     bool
	Reviewers             []Reviewer
	Notifications         []Notification
	RoleNotifications     []auth.RoleNotification
	Profile               Profile
	Application           Application
	Applications          []Application
	History               []Change
	Consent               Consent
	Input                 Input
	ProfileMode, EditMode bool
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	status := 500
	switch {
	case errors.Is(err, auth.ErrUnauthenticated):
		status = 401
	case errors.Is(err, auth.ErrForbidden):
		status = 403
	case errors.Is(err, ErrNotFound):
		status = 404
	case errors.Is(err, ErrConflict):
		h.renderer.Render(w, r, 409, "error", httpserver.Page{Title: "Конфликт изменений", Status: 409, Message: "Заявление уже изменено или действие недоступно в текущем статусе. Обновите карточку."})
		return
	case errors.Is(err, ErrValidation):
		h.renderer.Render(w, r, 422, "error", httpserver.Page{Title: "Проверьте данные", Status: 422, Message: "Для отправки заполните ФИО, email, учебную группу и текст заявления; примите текущую версию согласия. Максимум: ФИО 200, email 254, группа 100, текст 2000 символов."})
		return
	}
	h.renderer.Error(w, r, status)
}
func (h *Handler) render(w http.ResponseWriter, r *http.Request, title, template string, v view, message string, status int) {
	token, err := h.actor.CSRFToken(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.renderer.Render(w, r, status, template, httpserver.Page{Title: title, CSRFToken: token, Data: v, Message: message})
}
func parseInput(r *http.Request) Input {
	return Input{r.PostForm.Get("name"), r.PostForm.Get("email"), r.PostForm.Get("group"), r.PostForm.Get("request")}
}
func version(r *http.Request) (int64, error) {
	v, err := strconv.ParseInt(r.PostForm.Get("version"), 10, 64)
	if err != nil || v < 0 {
		return 0, ErrConflict
	}
	return v, nil
}
func (h *Handler) Profile(w http.ResponseWriter, r *http.Request) {
	actor, err := h.actor.ActorID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	p, err := h.s.Profile(r.Context(), actor)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	v := view{Profile: p, ProfileMode: true, Input: input(p.Fields)}
	status := 200
	message := ""
	if r.Method == "POST" {
		n, err := version(r)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		v.Input = parseInput(r)
		v.Profile.Version = n
		err = h.s.SaveProfile(r.Context(), actor, v.Input, n)
		if err == nil {
			http.Redirect(w, r, "/student/profile?saved=1", 303)
			return
		}
		if !errors.Is(err, ErrValidation) {
			h.fail(w, r, err)
			return
		}
		status = 422
		message = "Проверьте email и длину полей: ФИО до 200, email до 254, группа до 100 символов."
	}
	if r.URL.Query().Get("saved") == "1" {
		message = "Профиль сохранён. Источники полей указаны ниже."
	}
	h.render(w, r, "Профиль студента", "application_form", v, message, status)
}
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	actor, err := h.actor.ActorID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items, err := h.s.List(r.Context(), actor)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, "Мои заявления", "applications", view{Applications: items}, "", 200)
}
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	actor, err := h.actor.ActorID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	a, err := h.s.Create(r.Context(), actor)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, a.URL()+"/edit", 303)
}
func (h *Handler) Detail(w http.ResponseWriter, r *http.Request) {
	actor, err := h.actor.ActorID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	a, history, err := h.s.Detail(r.Context(), actor, r.PathValue("applicationID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	c, err := h.s.Consent(r.Context(), actor, a.ConsentVersion)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, "Заявление", "application_detail", view{Application: a, History: history, Consent: c}, "", 200)
}
func (h *Handler) Edit(w http.ResponseWriter, r *http.Request) {
	actor, err := h.actor.ActorID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	a, _, err := h.s.Detail(r.Context(), actor, r.PathValue("applicationID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !a.Editable() {
		h.fail(w, r, ErrConflict)
		return
	}
	v := view{Application: a, EditMode: true, Input: input(a.Fields)}
	status := 200
	message := ""
	if r.Method == "POST" {
		n, err := version(r)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		v.Input = parseInput(r)
		v.Application.Version = n
		err = h.s.Edit(r.Context(), actor, a.ID, v.Input, n)
		if err == nil {
			http.Redirect(w, r, a.URL(), 303)
			return
		}
		if !errors.Is(err, ErrValidation) {
			h.fail(w, r, err)
			return
		}
		status = 422
		message = "Проверьте email и длину полей: ФИО до 200, email до 254, группа до 100, текст до 2000 символов."
	}
	h.render(w, r, "Редактирование заявления", "application_form", v, message, status)
}
func (h *Handler) Submit(w http.ResponseWriter, r *http.Request) {
	actor, err := h.actor.ActorID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	n, err := version(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	a, err := h.s.Submit(r.Context(), actor, r.PathValue("applicationID"), r.PostForm.Get("submit_key"), r.PostForm.Get("consent_version"), r.PostForm.Get("accept_consent") == "on", n)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, a.URL(), 303)
}
func (h *Handler) Withdraw(w http.ResponseWriter, r *http.Request) {
	actor, err := h.actor.ActorID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	n, err := version(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	id := r.PathValue("applicationID")
	if err := h.s.Withdraw(r.Context(), actor, id, n); err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/applications/"+id, 303)
}
