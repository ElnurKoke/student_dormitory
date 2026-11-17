package inventory

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"student_dormitory/internal/platform/httpserver"
)

type PhotoHandler struct {
	*Handler
	photos *PhotoService
}

func NewPhotoHandler(photos *PhotoService, handler *Handler) *PhotoHandler {
	return &PhotoHandler{Handler: handler, photos: photos}
}
func (h *PhotoHandler) photoFail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrPhoto) {
		h.renderer.Render(w, r, 422, "error", httpserver.Page{Title: "Проверьте фотографию", Status: 422, Message: "Выберите JPEG, PNG или WebP до 5 МиБ, не более 4096 пикселей по стороне и 12 миллионов пикселей. Описание — до 1000 символов, порядок — от 0 до 1000000. Повторите загрузку со страницы объекта."})
		return
	}
	h.fail(w, r, err)
}
func photoDraft(r *http.Request) (PhotoDraft, error) {
	position, err := strconv.ParseInt(r.PostForm.Get("position"), 10, 64)
	if err != nil {
		return PhotoDraft{}, ErrPhoto
	}
	return PhotoDraft{Description: r.PostForm.Get("description"), Position: position, Primary: r.PostForm.Get("primary") == "on"}, nil
}
func (h *PhotoHandler) Upload(w http.ResponseWriter, r *http.Request) {
	actor, err := h.actor.ActorID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if r.MultipartForm == nil || len(r.MultipartForm.File) != 1 || len(r.MultipartForm.File["photo"]) != 1 {
		h.photoFail(w, r, ErrPhoto)
		return
	}
	draft, err := photoDraft(r)
	if err != nil {
		h.photoFail(w, r, err)
		return
	}
	file, err := r.MultipartForm.File["photo"][0].Open()
	if err != nil {
		h.photoFail(w, r, err)
		return
	}
	defer file.Close()
	_, err = h.photos.Upload(r.Context(), actor, location(r), file, draft)
	if err != nil {
		h.photoFail(w, r, err)
		return
	}
	http.Redirect(w, r, locationURL(location(r))+"#photos", http.StatusSeeOther)
}
func (h *PhotoHandler) Change(remove bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, err := h.actor.ActorID(r)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		version, err := strconv.ParseInt(r.PostForm.Get("version"), 10, 64)
		if err != nil || version < 1 {
			h.renderer.Error(w, r, 400)
			return
		}
		var draft PhotoDraft
		if !remove {
			draft, err = photoDraft(r)
			if err != nil {
				h.photoFail(w, r, err)
				return
			}
		}
		err = h.photos.Change(r.Context(), actor, location(r), r.PathValue("photoID"), version, draft, remove)
		if err != nil {
			h.photoFail(w, r, err)
			return
		}
		http.Redirect(w, r, locationURL(location(r))+"#photos", http.StatusSeeOther)
	})
}
func (h *PhotoHandler) Image(public bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor := ""
		if !public {
			var err error
			actor, err = h.actor.ActorID(r)
			if err != nil {
				h.fail(w, r, err)
				return
			}
		}
		photo, reader, err := h.photos.Open(r.Context(), actor, r.PathValue("photoID"), public)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		defer reader.Close()
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", photo.MediaType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		w.Header().Set("Content-Disposition", `inline; filename="photo`+map[string]string{"image/jpeg": ".jpg", "image/png": ".png"}[photo.MediaType]+`"`)
		w.Header().Set("Content-Length", strconv.FormatInt(photo.ByteSize, 10))
		if r.Method != http.MethodHead {
			// Once headers are sent an I/O failure must abort, never append an HTML error.
			if _, err := io.CopyN(w, reader, photo.ByteSize); err != nil {
				panic(http.ErrAbortHandler)
			}
		}
	})
}
