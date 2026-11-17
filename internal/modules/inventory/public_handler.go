package inventory

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"student_dormitory/internal/platform/httpserver"
)

type PublicHandler struct {
	service  *PublicService
	renderer *httpserver.Renderer
}

func NewPublicHandler(s *PublicService, r *httpserver.Renderer) *PublicHandler {
	return &PublicHandler{service: s, renderer: r}
}

type publicView struct {
	Listing              PublicListing
	Dormitory            PublicDormitory
	Query                url.Values
	Previous, Next, Back string
	Error                string
}

func (h *PublicHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := PublicFilter{Name: q.Get("name"), Address: q.Get("address"), Currency: q.Get("currency"), Period: q.Get("period"), Free: q.Get("free") == "1", Accessible: q.Get("accessible") == "1", Page: 1}
	var err error
	if q.Get("page") != "" {
		f.Page, err = strconv.Atoi(q.Get("page"))
	}
	for _, pair := range []struct {
		key    string
		target **int64
	}{{"min", &f.Min}, {"max", &f.Max}} {
		if q.Get(pair.key) != "" {
			n, e := parseInteger(q.Get(pair.key))
			if e != nil {
				err = e
			}
			*pair.target = &n
		}
	}
	for _, key := range []string{"free", "accessible"} {
		if q.Get(key) != "" && q.Get(key) != "1" {
			err = ErrPublicFilter
		}
	}
	v := publicView{Query: q}
	status := http.StatusOK
	if err == nil {
		v.Listing, err = h.service.List(r.Context(), f)
	}
	if err != nil {
		if !errors.Is(err, ErrPublicFilter) && err != strconv.ErrSyntax && err != strconv.ErrRange {
			var number *strconv.NumError
			if !errors.As(err, &number) {
				h.renderer.Error(w, r, 500)
				return
			}
		}
		status = 400
		v.Error = "Проверьте фильтры: суммы — целые минимальные денежные единицы; для диапазона укажите валюту и период."
	}
	pageURL := func(page int) string {
		copy := r.URL.Query()
		copy.Set("page", strconv.Itoa(page))
		return "/catalog?" + copy.Encode()
	}
	if v.Listing.Page > 1 {
		v.Previous = pageURL(v.Listing.Page - 1)
	}
	if v.Listing.Page < v.Listing.Pages {
		v.Next = pageURL(v.Listing.Page + 1)
	}
	h.renderer.Render(w, r, status, "catalog", httpserver.Page{Title: "Каталог общежитий", Data: v})
}
func (h *PublicHandler) Detail(w http.ResponseWriter, r *http.Request) {
	listing, err := h.service.Detail(r.Context(), r.PathValue("dormitoryID"))
	if err != nil {
		status := 500
		if errors.Is(err, ErrNotFound) {
			status = 404
		}
		h.renderer.Error(w, r, status)
		return
	}
	q := r.URL.Query()
	q.Del("lang")
	back := "/catalog"
	if len(q) > 0 {
		back += "?" + q.Encode()
	}
	h.renderer.Render(w, r, 200, "catalog_detail", httpserver.Page{Title: listing.Items[0].Name, Data: publicView{Listing: listing, Dormitory: listing.Items[0], Back: back}})
}
