package applications

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"student_dormitory/internal/modules/auth"
	"student_dormitory/internal/platform/httpserver"
)

type WorkflowHandler struct {
	*Handler
	review            *ReviewService
	documents         *DocumentService
	roleNotifications RoleNotificationSource
}

// RoleNotificationSource supplies account notifications without reading auth tables.
type RoleNotificationSource interface {
	RoleNotifications(context.Context, string) ([]auth.RoleNotification, error)
}

func NewWorkflowHandler(s *Service, review *ReviewService, docs *DocumentService, a ActorSource, r *httpserver.Renderer, roleNotifications RoleNotificationSource) *WorkflowHandler {
	return &WorkflowHandler{NewHandler(s, a, r), review, docs, roleNotifications}
}
func (h *WorkflowHandler) Detail(staff bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, err := h.actor.ActorID(r)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		data, err := h.s.Workspace(r.Context(), actor, r.PathValue("applicationID"), staff)
		if err != nil {
			h.fail(w, r, err)
			return
		}

		v := view{Application: data.Application, History: data.History, Documents: data.Documents, Reviews: data.Reviews, Snapshots: data.Snapshots, Checks: data.Checks, Staff: staff, Consent: data.Consent}
		h.render(w, r, "Заявление", "application_detail", v, "", 200)
	})
}
func (h *WorkflowHandler) StaffList(assign bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, err := h.actor.ActorID(r)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		v := view{AssignMode: assign}
		if assign {
			v.Applications, v.Reviewers, err = h.review.Assignments(r.Context(), actor)
		} else {
			v.Applications, err = h.review.List(r.Context(), actor)
		}
		if err != nil {
			h.fail(w, r, err)
			return
		}
		h.render(w, r, "Проверка заявлений", "application_staff", v, "", 200)
	})
}
func (h *WorkflowHandler) Assign(w http.ResponseWriter, r *http.Request) {
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
	if err := h.review.Assign(r.Context(), actor, r.PathValue("applicationID"), r.PostForm.Get("reviewer_id"), n); err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/staff/application-assignments", 303)
}
func (h *WorkflowHandler) Review(w http.ResponseWriter, r *http.Request) {
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
	if err := h.review.Review(r.Context(), actor, r.PathValue("applicationID"), r.PostForm.Get("result"), r.PostForm.Get("comment"), r.PostForm.Get("confirm_review") == "on", n); err != nil {
		h.reviewFail(w, r, err)
		return
	}
	http.Redirect(w, r, "/staff/applications/"+r.PathValue("applicationID"), 303)
}
func (h *WorkflowHandler) reviewFail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrValidation) {
		h.renderer.Render(w, r, 422, "error", httpserver.Page{Title: "Проверьте результат проверки", Status: 422, Message: "Выберите результат, укажите комментарий до 2000 символов и подтвердите завершение ручной проверки для результата «проверено»."})
		return
	}
	h.fail(w, r, err)
}
func (h *WorkflowHandler) Upload(w http.ResponseWriter, r *http.Request) {
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
	if r.MultipartForm == nil || len(r.MultipartForm.File) != 1 || len(r.MultipartForm.File["document"]) != 1 {
		h.documentFail(w, r, ErrDocument)
		return
	}
	fileHeader := r.MultipartForm.File["document"][0]
	file, err := fileHeader.Open()
	if err != nil {
		h.documentFail(w, r, ErrDocument)
		return
	}
	defer file.Close()
	_, err = h.documents.Upload(r.Context(), actor, r.PathValue("applicationID"), r.PostForm.Get("document_id"), r.PostForm.Get("label"), fileHeader.Filename, n, file)
	if err != nil {
		h.documentFail(w, r, err)
		return
	}
	http.Redirect(w, r, "/applications/"+r.PathValue("applicationID")+"#documents", 303)
}
func (h *WorkflowHandler) documentFail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrDocument) {
		h.renderer.Render(w, r, 422, "error", httpserver.Page{Title: "Проверьте документ", Status: 422, Message: "Допустимы PDF, JPEG и PNG до 10 МиБ. Изображения: до 4096 пикселей по стороне и 12 миллионов пикселей. Название документа — до 120 символов."})
		return
	}
	h.fail(w, r, err)
}
func (h *WorkflowHandler) Download(staff bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, err := h.actor.ActorID(r)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		d, reader, err := h.documents.Open(r.Context(), actor, r.PathValue("applicationID"), r.PathValue("versionID"), staff)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		defer reader.Close()
		ext := map[string]string{"application/pdf": ".pdf", "image/jpeg": ".jpg", "image/png": ".png"}[d.MediaType]
		w.Header().Set("Content-Type", d.MediaType)
		w.Header().Set("Content-Disposition", `attachment; filename="document`+ext+`"`)
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Length", strconv.FormatInt(d.ByteSize, 10))
		if r.Method != http.MethodHead {
			if _, err := io.CopyN(w, reader, d.ByteSize); err != nil {
				panic(http.ErrAbortHandler)
			}
		}
	})
}
func (h *WorkflowHandler) Notifications(w http.ResponseWriter, r *http.Request) {
	actor, err := h.actor.ActorID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items, err := h.s.Notifications(r.Context(), actor)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	roles, err := h.roleNotifications.RoleNotifications(r.Context(), actor)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, "Уведомления", "application_notifications", view{Notifications: items, RoleNotifications: roles}, "", 200)
}
func (h *WorkflowHandler) ReadNotification(w http.ResponseWriter, r *http.Request) {
	actor, err := h.actor.ActorID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("notificationID")), 10, 64)
	if err != nil || id < 1 {
		h.fail(w, r, ErrNotFound)
		return
	}
	if err := h.s.ReadNotification(r.Context(), actor, id); err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/account/notifications", 303)
}
