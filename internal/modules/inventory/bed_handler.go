package inventory

import (
	"errors"
	"net/http"
	"strconv"

	"student_dormitory/internal/modules/auth"
	"student_dormitory/internal/platform/httpserver"
)

func (h *Handler) BedStatus(w http.ResponseWriter, r *http.Request) {
	actor, err := h.actor.ActorID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	p := location(r)
	v, err := h.detailView(r, actor, p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !v.CanWriteStatus {
		h.fail(w, r, auth.ErrForbidden)
		return
	}
	if !v.CanChangeStatus {
		h.fail(w, r, ErrArchived)
		return
	}
	v.ActionURL = v.BaseURL + "/status"
	v.CancelURL = v.BaseURL
	v.Errors = ValidationError{}
	status := 200
	if r.Method == http.MethodPost {
		v.Version, err = strconv.ParseInt(r.PostForm.Get("version"), 10, 64)
		if err != nil || v.Version < 1 {
			h.renderer.Error(w, r, 400)
			return
		}
		v.Basis = r.PostForm.Get("basis")
		v.NextStatus = BedStatus(r.PostForm.Get("status"))
		_, err = h.service.ChangeBedStatus(r.Context(), actor, p, v.Version, v.NextStatus, v.Basis)
		if err == nil {
			http.Redirect(w, r, v.BaseURL+"?saved=status", http.StatusSeeOther)
			return
		}
		var fields ValidationError
		switch {
		case errors.As(err, &fields):
			v.Errors = fields
			status = 422
		case errors.Is(err, ErrConflict):
			v.Errors["_form"] = errorMessage(err)
			status = 409
		default:
			h.fail(w, r, err)
			return
		}
	}
	token, err := h.actor.CSRFToken(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.renderer.Render(w, r, status, "inventory_status", httpserver.Page{Title: "Изменение статуса койко-места", CSRFToken: token, Data: v})
}
