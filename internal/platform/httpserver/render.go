package httpserver

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
)

type Page struct {
	Title      string
	Status     int
	Message    string
	CSRFToken  string
	Data       any
	Navigation Navigation
	LoginValue string
}

type Renderer struct {
	pages map[string]*template.Template
	log   *slog.Logger
}

func NewRenderer(source fs.FS, log *slog.Logger) (*Renderer, error) {
	r := &Renderer{pages: make(map[string]*template.Template), log: log}
	for _, name := range []string{"home", "error", "login", "account", "users", "inventory_list", "inventory_detail", "inventory_form", "inventory_status", "catalog", "catalog_detail", "statistics", "registry_export", "history", "applications", "application_form", "application_detail", "application_staff", "application_notifications"} {
		t, err := template.New("page").Funcs(template.FuncMap{"t": func(s string) string { return s }, "lang": func() string { return "ru" }, "url": func(s string) string { return s }, "languageURL": func(s string) string { return s }, "catalogURL": func(s string) string { return s }}).ParseFS(source, "templates/base.html", "templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("parse %s template: %w", name, err)
		}
		r.pages[name] = t
	}
	return r, nil
}

func (r *Renderer) Render(w http.ResponseWriter, request *http.Request, status int, name string, data Page) {
	data.Navigation = navigationFromContext(request.Context())
	if name != "catalog_detail" && name != "inventory_detail" && name != "inventory_form" {
		data.Title = Text(request, data.Title)
	}
	data.Message = Text(request, data.Message)
	var buffer bytes.Buffer
	t, ok := r.pages[name]
	if !ok {
		r.log.Error("unknown template", "name", name)
		http.Error(w, Text(request, "Внутренняя ошибка сервера"), http.StatusInternalServerError)
		return
	}
	t, err := t.Clone()
	if err != nil {
		r.log.Error("clone template failed", "name", name)
		http.Error(w, Text(request, "Внутренняя ошибка сервера"), 500)
		return
	}
	lang := Language(request)
	t.Funcs(template.FuncMap{
		"t":           func(s string) string { return translate(lang, s) },
		"lang":        func() string { return lang },
		"url":         func(s string) string { return localURL(s, lang) },
		"languageURL": func(s string) string { return languageURL(request, s) },
		"catalogURL": func(id string) string {
			u := url.URL{Path: "/catalog/" + id, RawQuery: request.URL.RawQuery}
			return localURL(u.String(), lang)
		},
	})
	if err := t.ExecuteTemplate(&buffer, "base", data); err != nil {
		r.log.Error("render template failed", "name", name)
		http.Error(w, Text(request, "Внутренняя ошибка сервера"), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Language", Language(request))
	w.WriteHeader(status)
	if _, err := buffer.WriteTo(w); err != nil {
		r.log.Warn("write response failed")
	}
}

func (r *Renderer) Error(w http.ResponseWriter, request *http.Request, status int) {
	page := Page{Status: status}
	switch status {
	case http.StatusBadRequest:
		page.Title, page.Message = "Некорректный запрос", "Проверьте данные формы и откройте страницу заново."
	case http.StatusUnauthorized:
		page.Title, page.Message = "Требуется вход", "Войдите в свою учётную запись."
	case http.StatusForbidden:
		page.Title, page.Message = "Доступ запрещён", "Недостаточно полномочий или запрос не прошёл проверку безопасности."
	case http.StatusNotFound:
		page.Title, page.Message = "Страница не найдена", "Проверьте адрес или вернитесь на главную."
	case http.StatusMethodNotAllowed:
		page.Title, page.Message = "Метод не поддерживается", "Это действие недоступно для данного адреса."
	case http.StatusRequestEntityTooLarge:
		page.Title, page.Message = "Файл слишком большой", "Размер запроса превышает допустимый предел. Проверьте ограничение в форме загрузки и повторите попытку."
	case http.StatusServiceUnavailable:
		page.Title, page.Message = "Сервис временно недоступен", "Повторите попытку позже."
	default:
		page.Title, page.Message = "Внутренняя ошибка сервера", "Не удалось обработать запрос. Повторите попытку позже."
	}
	r.Render(w, request, status, "error", page)
}
