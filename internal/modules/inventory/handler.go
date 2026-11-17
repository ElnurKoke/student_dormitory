package inventory

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"student_dormitory/internal/modules/auth"
	"student_dormitory/internal/platform/httpserver"
)

type ActorSource interface {
	ActorID(*http.Request) (string, error)
	CSRFToken(*http.Request) (string, error)
}
type Handler struct {
	service  *Service
	actor    ActorSource
	renderer *httpserver.Renderer
}

func NewHandler(s *Service, actor ActorSource, r *httpserver.Renderer) *Handler {
	return &Handler{service: s, actor: actor, renderer: r}
}

type item struct {
	Node Node
	URL  string
}
type view struct {
	Photos                                                                      []Photo
	CanWritePhotos                                                              bool
	Node                                                                        Node
	Kind                                                                        Kind
	Draft                                                                       Draft
	Errors                                                                      ValidationError
	Items                                                                       []item
	Breadcrumbs                                                                 []httpserver.NavigationLink
	CreatableIDs                                                                []string
	ChosenID, BaseURL, ActionURL, CancelURL, NewURL, ChildLabel, Message, State string
	CanWrite, CanAdd, CanRestore, Create, HasError                              bool
	Version                                                                     int64
	History                                                                     []BedChange
	CanChangeStatus                                                             bool
	CanWriteStatus                                                              bool
	Basis                                                                       string
	NextStatus                                                                  BedStatus
}

func location(r *http.Request) Location {
	return Location{DormitoryID: r.PathValue("dormitoryID"), BuildingID: r.PathValue("buildingID"), FloorID: r.PathValue("floorID"), RoomID: r.PathValue("roomID"), BedID: r.PathValue("bedID")}
}
func locationURL(p Location) string {
	path := "/staff/dormitories"
	for _, part := range []struct{ label, id string }{{"", p.DormitoryID}, {"buildings", p.BuildingID}, {"floors", p.FloorID}, {"rooms", p.RoomID}, {"beds", p.BedID}} {
		if part.id != "" {
			if part.label != "" {
				path += "/" + part.label
			}
			path += "/" + url.PathEscape(part.id)
		}
	}
	return path
}
func childLocation(p Location, n Node) Location {
	switch n.Kind {
	case Dormitory:
		p.DormitoryID = n.ID
	case Building:
		p.BuildingID = n.ID
	case Floor:
		p.FloorID = n.ID
	case Room:
		p.RoomID = n.ID
	case Bed:
		p.BedID = n.ID
	}
	return p
}
func childSegment(k Kind) string {
	switch k {
	case Building:
		return "buildings"
	case Floor:
		return "floors"
	case Room:
		return "rooms"
	case Bed:
		return "beds"
	}
	return ""
}
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, auth.ErrUnauthenticated):
		status = 401
	case errors.Is(err, auth.ErrForbidden):
		status = 403
	case errors.Is(err, ErrNotFound):
		status = 404
	case errors.Is(err, ErrConflict), errors.Is(err, ErrArchived), errors.Is(err, ErrActiveChildren), errors.Is(err, ErrCapacity), errors.Is(err, ErrBedInUse):
		status = 409
	}
	if status == 409 {
		h.renderer.Render(w, r, status, "error", httpserver.Page{Title: "Действие пока невозможно", Status: status, Message: errorMessage(err)})
		return
	}
	h.renderer.Error(w, r, status)
}
func errorMessage(err error) string {
	switch {
	case errors.Is(err, ErrCapacity):
		return "Вместимость комнаты исчерпана. Добавить или восстановить место нельзя."
	case errors.Is(err, ErrBedInUse):
		return "Забронированное или занятое место нельзя архивировать. Требуется соответствующий процесс."
	case errors.Is(err, ErrConflict):
		return "Запись уже изменена или создана. Обновите страницу перед повторным сохранением."
	case errors.Is(err, ErrArchived):
		return "Объект или его родитель находится в архиве. Сначала восстановите родителей, затем сам объект."
	case errors.Is(err, ErrActiveChildren):
		return "Сначала архивируйте активные дочерние объекты. Существующие связи и записи будут сохранены."
	}
	return "Не удалось сохранить изменения. Повторите попытку позже."
}
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	actor, err := h.actor.ActorID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	listing, err := h.service.List(r.Context(), actor)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	v := view{Kind: Dormitory, CreatableIDs: listing.CreatableIDs, CanAdd: len(listing.CreatableIDs) > 0, NewURL: "/staff/dormitories/new", BaseURL: "/staff/dormitories", State: r.URL.Query().Get("state")}
	if r.URL.Query().Get("pending") == "1" {
		v.Message = "Назначенное общежитие ещё не внесено в реестр. Обратитесь к сотруднику реестра для создания записи."
	}
	for _, n := range listing.Nodes {
		if includeState(v.State, n.Archived) {
			v.Items = append(v.Items, item{Node: n, URL: locationURL(Location{DormitoryID: n.ID})})
		}
	}
	h.renderer.Render(w, r, 200, "inventory_list", httpserver.Page{Title: "Реестр общежитий", Data: v})
}
func includeState(state string, archived bool) bool {
	return (state != "active" && state != "archived") || (state == "active" && !archived) || (state == "archived" && archived)
}
func (h *Handler) detailView(r *http.Request, actor string, p Location) (view, error) {
	d, err := h.service.Detail(r.Context(), actor, p)
	if err != nil {
		return view{}, err
	}
	v := view{Node: d.Node, Kind: d.Node.Kind, BaseURL: locationURL(p), CanWrite: d.CanWrite, CanRestore: d.CanWrite, State: r.URL.Query().Get("state"), Version: d.Node.Version}
	ancestorsActive := active(d.Ancestors) == nil
	v.Photos = d.Photos
	v.CanWritePhotos = d.CanWrite && ancestorsActive && !d.Node.Archived
	v.CanRestore = v.CanRestore && ancestorsActive
	v.CanWriteStatus = d.CanChangeStatus
	v.History = d.History
	v.CanChangeStatus = d.CanChangeStatus && ancestorsActive && !d.Node.Archived && d.Node.Kind == Bed
	v.CanAdd = d.CanWrite && !d.Node.Archived && ancestorsActive && d.Node.Kind.Child() != ""
	v.ChildLabel = d.Node.Kind.Child().Label()
	v.NewURL = v.BaseURL + "/" + childSegment(d.Node.Kind.Child()) + "/new"
	var at Location
	for _, n := range d.Ancestors {
		at = childLocation(at, n)
		v.Breadcrumbs = append(v.Breadcrumbs, httpserver.NavigationLink{Prefix: n.Kind.Label(), Label: n.Label(), URL: locationURL(at)})
	}
	for _, n := range d.Children {
		if includeState(v.State, n.Archived) {
			v.Items = append(v.Items, item{Node: n, URL: locationURL(childLocation(p, n))})
		}
	}
	return v, nil
}
func (h *Handler) Show(w http.ResponseWriter, r *http.Request) {
	actor, err := h.actor.ActorID(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	p := location(r)
	v, err := h.detailView(r, actor, p)
	if errors.Is(err, ErrNotFound) && p.Kind() == Dormitory {
		if list, e := h.service.List(r.Context(), actor); e == nil {
			target := "/staff/dormitories?pending=1"
			if contains(list.CreatableIDs, p.DormitoryID) {
				target = "/staff/dormitories/new?scope=" + url.QueryEscape(p.DormitoryID)
			}
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	switch r.URL.Query().Get("saved") {
	case "status":
		v.Message = "Статус изменён. Основание и история сохранены."
	case "created":
		v.Message = "Объект создан."
	case "updated":
		v.Message = "Изменения сохранены."
	case "archived":
		v.Message = "Объект архивирован. Связи и история сохранены."
	case "restored":
		v.Message = "Объект восстановлен."
	}
	token, err := h.actor.CSRFToken(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.renderer.Render(w, r, 200, "inventory_detail", httpserver.Page{Title: httpserver.Text(r, v.Kind.Label()) + " " + v.Node.Label(), CSRFToken: token, Data: v})
}
func draft(r *http.Request) Draft {
	f := r.PostForm
	return Draft{Basis: f.Get("basis"), Name: f.Get("name"), Number: f.Get("number"), Address: f.Get("address"), Operator: f.Get("operator"), UseBasis: f.Get("use_basis"), Contacts: f.Get("contacts"), Description: f.Get("description"), Published: f.Get("published") == "on",
		Capacity: f.Get("capacity"), Area: f.Get("area"), PriceMinor: f.Get("price_minor"), Equipment: f.Get("equipment"), Sanitary: f.Get("sanitary"), AccessibilityNotes: f.Get("accessibility_notes"), Accessible: f.Get("accessible") == "on", Currency: f.Get("currency"), PricePeriod: f.Get("price_period")}
}
func (h *Handler) Form(k Kind, create bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, err := h.actor.ActorID(r)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		p := location(r)
		v := view{Kind: k, Create: create, Errors: ValidationError{}}
		if k == Dormitory && create {
			list, err := h.service.List(r.Context(), actor)
			if err != nil {
				h.fail(w, r, err)
				return
			}
			if len(list.CreatableIDs) == 0 && r.Method != http.MethodPost {
				h.fail(w, r, auth.ErrForbidden)
				return
			}
			v.CreatableIDs = list.CreatableIDs
			v.ChosenID = r.URL.Query().Get("scope")
			if !contains(v.CreatableIDs, v.ChosenID) && len(v.CreatableIDs) > 0 {
				v.ChosenID = v.CreatableIDs[0]
			}
			p.DormitoryID = v.ChosenID
			v.ActionURL = "/staff/dormitories/new"
			v.CancelURL = "/staff/dormitories"
		} else {
			d, err := h.service.Detail(r.Context(), actor, p)
			if err != nil {
				h.fail(w, r, err)
				return
			}
			if !d.CanWrite {
				h.fail(w, r, auth.ErrForbidden)
				return
			}
			if d.Node.Archived || active(d.Ancestors) != nil {
				h.fail(w, r, ErrArchived)
				return
			}
			v.Node = d.Node
			v.CancelURL = locationURL(p)
			v.ActionURL = v.CancelURL + "/edit"
			if create {
				v.ActionURL = v.CancelURL + "/" + childSegment(k) + "/new"
			} else {
				v.Draft = d.Node.Draft()
				v.Version = d.Node.Version
			}
		}
		status := 200
		if r.Method == http.MethodPost {
			v.Draft = draft(r)
			if create && k == Dormitory {
				p.DormitoryID = r.PostForm.Get("dormitory_id")
				v.ChosenID = p.DormitoryID
			}
			if !create {
				v.Version, err = strconv.ParseInt(r.PostForm.Get("version"), 10, 64)
				if err != nil || v.Version < 1 {
					h.renderer.Error(w, r, 400)
					return
				}
			}
			n, err := h.service.Save(r.Context(), actor, p, k, v.Draft, create, v.Version)
			if err == nil {
				target := p
				if create {
					target = childLocation(p, n)
				}
				saved := "updated"
				if create {
					saved = "created"
				}
				http.Redirect(w, r, locationURL(target)+"?saved="+saved, http.StatusSeeOther)
				return
			}
			var fields ValidationError
			switch {
			case errors.As(err, &fields):
				v.Errors = fields
				status = 422
			case errors.Is(err, ErrConflict), errors.Is(err, ErrArchived), errors.Is(err, ErrCapacity):
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
		title := httpserver.Text(r, "Редактирование") + ": " + httpserver.Text(r, k.Label())
		if create {
			title = httpserver.Text(r, "Создание") + ": " + httpserver.Text(r, k.Label())
		}
		h.renderer.Render(w, r, status, "inventory_form", httpserver.Page{Title: title, CSRFToken: token, Data: v})
	})
}
func (h *Handler) Archive(archived bool) http.Handler {
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
		p := location(r)
		_, err = h.service.ArchiveWithBasis(r.Context(), actor, p, version, archived, r.PostForm.Get("basis"))
		if err != nil {
			var fields ValidationError
			if errors.As(err, &fields) {
				h.renderer.Render(w, r, 422, "error", httpserver.Page{Title: "Укажите основание", Status: 422, Message: fields["basis"]})
				return
			}
			if errors.Is(err, ErrActiveChildren) || errors.Is(err, ErrConflict) || errors.Is(err, ErrArchived) || errors.Is(err, ErrCapacity) || errors.Is(err, ErrBedInUse) {
				v, e := h.detailView(r, actor, p)
				if e != nil {
					h.fail(w, r, e)
					return
				}
				v.Message = errorMessage(err)
				v.HasError = true
				token, e := h.actor.CSRFToken(r)
				if e != nil {
					h.fail(w, r, e)
					return
				}
				h.renderer.Render(w, r, 409, "inventory_detail", httpserver.Page{Title: httpserver.Text(r, v.Kind.Label()) + " " + v.Node.Label(), CSRFToken: token, Data: v})
				return
			}
			h.fail(w, r, err)
			return
		}
		action := "restored"
		if archived {
			action = "archived"
		}
		http.Redirect(w, r, locationURL(p)+"?saved="+action, http.StatusSeeOther)
	})
}
