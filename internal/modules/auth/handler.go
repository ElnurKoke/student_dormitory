package auth

import (
	"crypto/subtle"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"student_dormitory/internal/platform/httpserver"
)

type Handler struct {
	service     *Service
	development *DevelopmentProvider
	renderer    *httpserver.Renderer
	secure      bool
	crossOrigin *http.CrossOriginProtection
	eds         *EDSService
}

func NewHandler(service *Service, development *DevelopmentProvider, renderer *httpserver.Renderer, secure bool, eds *EDSService) *Handler {
	return &Handler{service: service, development: development, renderer: renderer, secure: secure, eds: eds, crossOrigin: http.NewCrossOriginProtection()}
}

func (h *Handler) cookieName() string {
	if h.secure {
		return "__Host-dorm_session"
	}
	return "dorm_session"
}

func (h *Handler) token(r *http.Request) string {
	cookie, err := r.Cookie(h.cookieName())
	if err != nil {
		return ""
	}
	return cookie.Value
}

func (h *Handler) setCookie(w http.ResponseWriter, session Session, token string) {
	http.SetCookie(w, &http.Cookie{Name: h.cookieName(), Value: token, Path: "/", HttpOnly: true, Secure: h.secure,
		SameSite: http.SameSiteLaxMode, Expires: session.ExpiresAt, MaxAge: max(1, int(time.Until(session.ExpiresAt).Seconds()))})
}

func (h *Handler) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: h.cookieName(), Value: "", Path: "/", HttpOnly: true, Secure: h.secure,
		SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0)})
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrCredentials), errors.Is(err, ErrUnauthenticated):
		status = http.StatusUnauthorized
	case errors.Is(err, ErrForbidden):
		status = http.StatusForbidden
	case errors.Is(err, ErrUnavailable):
		status = http.StatusNotFound
	case errors.Is(err, ErrUserNotFound):
		status = http.StatusNotFound
	}
	h.renderer.Error(w, r, status)
}

// Navigation decorates pages; permission checks still run in Require/services.
func (h *Handler) Navigation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || (r.URL.Path == "/static/app.css" || r.URL.Path == "/static/ncalayer-login.js") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		nav := httpserver.Navigation{LoginEnabled: h.development != nil || h.eds != nil, CurrentPath: r.URL.Path}
		session, err := h.service.Session(r.Context(), h.token(r))
		if err == nil && session.UserID != "" {
			if info, err := h.service.Navigation(r.Context(), session.UserID); err == nil {
				nav.Authenticated, nav.DisplayName, nav.CSRFToken = true, info.User.DisplayName, session.CSRFToken
				nav.Roles, nav.CanReadUsers = info.Roles, info.CanReadUsers
				nav.UnreadRoleNotifications = info.UnreadRoleNotifications
				nav.CanUseApplications = info.CanUseApplications
				nav.CanReviewApplications, nav.CanAssignApplications = info.CanReviewApplications, info.CanAssignApplications
				nav.CanReadStatistics, nav.CanExportRegistry, nav.CanReadAudit = info.CanReadStatistics, info.CanExportRegistry, info.CanReadAudit
				nav.ExpiresAt = session.ExpiresAt.UTC().Format("02.01.2006 15:04 UTC")
				for _, id := range info.DormitoryIDs {
					path := "/staff/dormitories/" + url.PathEscape(id)
					nav.Dormitories = append(nav.Dormitories, httpserver.NavigationLink{Label: id, URL: path, Active: r.URL.Path == "/staff/dormitories/"+id})
				}
			}
		}
		next.ServeHTTP(w, r.WithContext(httpserver.WithNavigation(r.Context(), nav)))
	})
}

// CSRF protects every registered changing form, including login and logout.
// The synchronizer token is bound to a server-side session, never a URL parameter.
func (h *Handler) CSRF(next http.Handler) http.Handler {
	return h.csrf(next, 64<<10, false)
}

// MultipartCSRF shares session, origin and token checks with ordinary forms.
func (h *Handler) MultipartCSRF(next http.Handler, limit int64) http.Handler {
	return h.csrf(next, limit, true)
}
func (h *Handler) csrf(next http.Handler, limit int64, multipart bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		if err := h.crossOrigin.Check(r); err != nil {
			h.fail(w, r, ErrForbidden)
			return
		}
		session, err := h.service.Session(r.Context(), h.token(r))
		if err != nil {
			h.fail(w, r, err)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if multipart && mediaType == "multipart/form-data" {
			err = r.ParseMultipartForm(64 << 10)
			if r.MultipartForm != nil {
				defer r.MultipartForm.RemoveAll()
			}
		} else {
			err = r.ParseForm()
		}
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				h.renderer.Error(w, r, http.StatusRequestEntityTooLarge)
			} else {
				h.fail(w, r, ErrForbidden)
			}
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf_token")), []byte(session.CSRFToken)) != 1 {
			h.fail(w, r, ErrForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) Require(permission string, scope func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		session, err := h.service.Session(r.Context(), h.token(r))
		if err != nil {
			h.clearCookie(w)
			h.fail(w, r, err)
			return
		}
		dormitoryID := ""
		if scope != nil {
			dormitoryID = scope(r)
		}
		if err := h.service.Authorize(r.Context(), session.UserID, permission, dormitoryID); err != nil {
			h.fail(w, r, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) LoginForm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	session, err := h.service.Session(r.Context(), h.token(r))
	if err != nil && !errors.Is(err, ErrUnauthenticated) {
		h.fail(w, r, err)
		return
	}
	if err == nil && session.UserID != "" {
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	if err != nil {
		var token string
		session, token, err = h.service.Anonymous(r.Context())
		if err != nil {
			h.fail(w, r, err)
			return
		}
		h.setCookie(w, session, token)
	}
	h.loginPage(w, r, http.StatusOK, session.CSRFToken, "", "")
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	if h.development == nil {
		h.fail(w, r, ErrUnavailable)
		return
	}
	session, token, err := h.service.Login(r.Context(), h.token(r), h.development.Attempt(r.PostForm.Get("login"), r.PostForm.Get("password")))
	if err != nil {
		if errors.Is(err, ErrCredentials) {
			previous, sessionErr := h.service.Session(r.Context(), h.token(r))
			if sessionErr == nil {
				login := r.PostForm.Get("login")
				if len(login) > 128 {
					login = ""
				}
				h.loginPage(w, r, http.StatusUnauthorized, previous.CSRFToken, login, "Не удалось войти. Проверьте логин и пароль.")
				return
			}
		}
		h.fail(w, r, err)
		return
	}
	h.setCookie(w, session, token)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if err := h.service.Logout(r.Context(), h.token(r)); err != nil {
		h.fail(w, r, err)
		return
	}
	h.clearCookie(w)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handler) Account(w http.ResponseWriter, r *http.Request) {
	session, err := h.service.Session(r.Context(), h.token(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	user, err := h.service.User(r.Context(), session.UserID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	profile, err := h.service.CertificateProfile(r.Context(), session.UserID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.renderer.Render(w, r, http.StatusOK, "account", httpserver.Page{Title: "Мой кабинет", CSRFToken: session.CSRFToken, Data: accountView{User: user, Certificate: profile}})
}

func (h *Handler) Users(w http.ResponseWriter, r *http.Request) {
	message := ""
	if r.URL.Query().Get("saved") == "1" {
		message = "Роль сохранена. При изменении роли пользователь получает уведомление в личном кабинете."
	}
	h.usersPage(w, r, http.StatusOK, message, r.URL.Query().Get("q"))
}

func (h *Handler) usersPage(w http.ResponseWriter, r *http.Request, status int, message, query string) {
	session, err := h.service.Session(r.Context(), h.token(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	users, err := h.service.SearchUsers(r.Context(), session.UserID, query)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.renderer.Render(w, r, status, "users", httpserver.Page{Title: "Учётные записи", Status: status, Message: message, CSRFToken: session.CSRFToken, Data: users})
}

func (h *Handler) ChangeRole(w http.ResponseWriter, r *http.Request) {
	actor, err := h.ActorID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	version, err := strconv.ParseInt(r.PostForm.Get("version"), 10, 64)
	if err == nil {
		err = h.service.ChangeRole(r.Context(), actor, r.PathValue("userID"), RoleChange{RoleID: r.PostForm.Get("role_id"), Version: version})
	} else {
		err = ErrRoleConflict
	}
	query := r.PostForm.Get("q")
	if err != nil {
		status, message := 0, ""
		switch {
		case errors.Is(err, ErrRoleValidation):
			status, message = 422, "Выберите роль из списка."
		case errors.Is(err, ErrRoleConflict):
			status, message = 409, "Роли пользователя уже изменились. Проверьте обновлённые данные и повторите выбор."
		case errors.Is(err, ErrLastAdmin):
			status, message = 409, "Нельзя изменить роль последнего активного системного администратора. Сначала назначьте другого."
		default:
			h.fail(w, r, err)
			return
		}
		h.usersPage(w, r, status, message, query)
		return
	}
	http.Redirect(w, r, "/admin/users?"+url.Values{"q": {query}, "saved": {"1"}}.Encode(), http.StatusSeeOther)
}

func (h *Handler) ReadRoleNotification(w http.ResponseWriter, r *http.Request) {
	actor, err := h.ActorID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("notificationID"), 10, 64)
	if err != nil || id < 1 {
		h.fail(w, r, ErrUserNotFound)
		return
	}
	if err := h.service.ReadRoleNotification(r.Context(), actor, id); err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/account/notifications", http.StatusSeeOther)
}

// ActorID is for trusted server handlers, never a user_id submitted by a form.
func (h *Handler) ActorID(r *http.Request) (string, error) {
	session, err := h.service.Session(r.Context(), h.token(r))
	if err != nil {
		return "", err
	}
	if session.UserID == "" {
		return "", ErrUnauthenticated
	}
	return session.UserID, nil
}

func (h *Handler) CSRFToken(r *http.Request) (string, error) {
	session, err := h.service.Session(r.Context(), h.token(r))
	return session.CSRFToken, err
}
