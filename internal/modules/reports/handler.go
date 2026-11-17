package reports

import (
	"bytes"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"student_dormitory/internal/modules/audit"
	"student_dormitory/internal/modules/auth"
	"student_dormitory/internal/modules/inventory"
	"student_dormitory/internal/platform/httpserver"
)

type ActorSource interface {
	ActorID(*http.Request) (string, error)
}
type Handler struct {
	service  *Service
	actor    ActorSource
	renderer *httpserver.Renderer
}

func NewHandler(s *Service, a ActorSource, r *httpserver.Renderer) *Handler {
	return &Handler{service: s, actor: a, renderer: r}
}

type recordItem struct {
	inventory.RegistryRecord
	URL string
}
type view struct {
	Query                  url.Values
	Statistics             Statistics
	Registry               Registry
	History                audit.Listing
	Scopes                 []Scope
	Items                  []recordItem
	Previous, Next, CSVURL string
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	code := 500
	switch {
	case errors.Is(err, auth.ErrUnauthenticated):
		code = 401
	case errors.Is(err, auth.ErrForbidden):
		code = 403
	case errors.Is(err, ErrFilter):
		code = 400
	}
	h.renderer.Error(w, r, code)
}
func parseFilter(q url.Values) (Filter, error) {
	f := Filter{DormitoryID: q.Get("dormitory"), Query: q.Get("q"), State: q.Get("state"), Kind: q.Get("kind"), Status: q.Get("status"), Quality: q.Get("quality"), Page: 1}
	if f.State == "" {
		f.State = "active"
	}
	if q.Get("page") != "" {
		var err error
		f.Page, err = strconv.Atoi(q.Get("page"))
		if err != nil {
			return f, ErrFilter
		}
	}
	return f, nil
}
func pagination(r *http.Request, page, pages int) (string, string) {
	target := func(n int) string {
		u := *r.URL
		q := u.Query()
		q.Set("page", strconv.Itoa(n))
		u.RawQuery = q.Encode()
		return u.RequestURI()
	}
	var previous, next string
	if page > 1 {
		previous = target(page - 1)
	}
	if page < pages {
		next = target(page + 1)
	}
	return previous, next
}
func recordURL(p inventory.Location) string {
	path := "/staff/dormitories/" + url.PathEscape(p.DormitoryID)
	for _, part := range []struct{ segment, id string }{{"buildings", p.BuildingID}, {"floors", p.FloorID}, {"rooms", p.RoomID}, {"beds", p.BedID}} {
		if part.id != "" {
			path += "/" + part.segment + "/" + url.PathEscape(part.id)
		}
	}
	return path
}
func (h *Handler) Statistics(w http.ResponseWriter, r *http.Request) {
	actor, err := h.actor.ActorID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	f, err := parseFilter(r.URL.Query())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	result, err := h.service.Statistics(r.Context(), actor, f)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	v := view{Query: r.URL.Query(), Statistics: result, Scopes: result.Scopes}
	for _, row := range result.Issues {
		v.Items = append(v.Items, recordItem{RegistryRecord: row, URL: recordURL(row.Location)})
	}
	v.Previous, v.Next = pagination(r, result.Page, result.Pages)
	h.renderer.Render(w, r, 200, "statistics", httpserver.Page{Title: "Статистика реестра", Data: v})
}
func (h *Handler) Registry(download bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, err := h.actor.ActorID(r)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		f, err := parseFilter(r.URL.Query())
		if err != nil {
			h.fail(w, r, err)
			return
		}
		result, err := h.service.Registry(r.Context(), actor, f, download)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		if download {
			var buffer bytes.Buffer
			if err := WriteRegistryCSV(&buffer, result.Records); err != nil {
				h.fail(w, r, err)
				return
			}
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="registry.csv"`)
			w.Header().Set("Content-Length", strconv.Itoa(buffer.Len()))
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(200)
			if r.Method != http.MethodHead {
				_, _ = w.Write(buffer.Bytes())
			}
			return
		}
		q := r.URL.Query()
		q.Del("page")
		v := view{Query: r.URL.Query(), Registry: result, Scopes: result.Scopes, CSVURL: "/staff/registry.csv?" + q.Encode()}
		for _, row := range result.Records {
			v.Items = append(v.Items, recordItem{RegistryRecord: row, URL: recordURL(row.Location)})
		}
		v.Previous, v.Next = pagination(r, result.Page, result.Pages)
		h.renderer.Render(w, r, 200, "registry_export", httpserver.Page{Title: "Экспорт реестра", Data: v})
	})
}
func (h *Handler) History(w http.ResponseWriter, r *http.Request) {
	actor, err := h.actor.ActorID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	page := 1
	if q.Get("page") != "" {
		page, err = strconv.Atoi(q.Get("page"))
		if err != nil {
			h.fail(w, r, ErrFilter)
			return
		}
	}
	filter := audit.Filter{DormitoryID: q.Get("dormitory"), ActorID: q.Get("actor"), Action: q.Get("action"), ObjectType: q.Get("type"), ObjectID: q.Get("object"), From: q.Get("from"), To: q.Get("to"), Page: page}
	result, scopes, err := h.service.History(r.Context(), actor, filter)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	v := view{Query: q, History: result, Scopes: scopes}
	v.Previous, v.Next = pagination(r, result.Page, result.Pages)
	h.renderer.Render(w, r, 200, "history", httpserver.Page{Title: "История изменений реестра", Data: v})
}
