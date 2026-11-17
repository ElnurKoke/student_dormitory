package inventory

import (
	"errors"
	"strconv"
)

type Kind string

const (
	Dormitory Kind = "dormitory"
	Building  Kind = "building"
	Floor     Kind = "floor"
	Room      Kind = "room"
	Bed       Kind = "bed"
)

var (
	ErrCapacity       = errors.New("room capacity exceeded")
	ErrBedInUse       = errors.New("reserved or occupied bed cannot be archived")
	ErrNotFound       = errors.New("inventory object not found")
	ErrConflict       = errors.New("inventory version conflict")
	ErrArchived       = errors.New("inventory parent or object archived")
	ErrActiveChildren = errors.New("active child objects prevent archiving")
)

type ValidationError map[string]string

func (e ValidationError) Error() string { return "inventory validation failed" }

type Location struct{ DormitoryID, BuildingID, FloorID, RoomID, BedID string }

func (p Location) Kind() Kind {
	if p.BedID != "" {
		return Bed
	}
	if p.RoomID != "" {
		return Room
	}
	if p.FloorID != "" {
		return Floor
	}
	if p.BuildingID != "" {
		return Building
	}
	return Dormitory
}
func (p Location) ID() string {
	switch p.Kind() {
	case Bed:
		return p.BedID
	case Room:
		return p.RoomID
	case Floor:
		return p.FloorID
	case Building:
		return p.BuildingID
	default:
		return p.DormitoryID
	}
}
func (p Location) Parent() Location {
	switch p.Kind() {
	case Bed:
		p.BedID = ""
	case Room:
		p.RoomID = ""
	case Floor:
		p.FloorID = ""
	case Building:
		p.BuildingID = ""
	default:
		p.DormitoryID = ""
	}
	return p
}
func (k Kind) Child() Kind {
	switch k {
	case Dormitory:
		return Building
	case Building:
		return Floor
	case Floor:
		return Room
	case Room:
		return Bed
	}
	return ""
}
func (k Kind) Label() string {
	switch k {
	case Dormitory:
		return "Общежитие"
	case Building:
		return "Корпус"
	case Floor:
		return "Этаж"
	case Room:
		return "Комната"
	case Bed:
		return "Койко-место"
	}
	return "Объект"
}

type Fields struct {
	Name, Number, Address, Operator, UseBasis, Contacts, Description string
	Published                                                        bool
	Capacity, AreaHundredths, PriceMinor                             int64
	Equipment, Sanitary, AccessibilityNotes, Currency, PricePeriod   string
	Accessible                                                       bool
}
type Node struct {
	ID, ParentID, DormitoryID string
	Kind                      Kind
	Fields
	Archived  bool
	Version   int64
	UpdatedAt string
	Status    BedStatus
}

func (n Node) Label() string {
	if n.Kind == Room || n.Kind == Floor || n.Kind == Bed {
		return n.Number
	}
	return n.Name
}
func (n Node) Area() string {
	return strconv.FormatInt(n.AreaHundredths/100, 10) + "." + twoDigits(n.AreaHundredths%100)
}
func twoDigits(n int64) string {
	if n < 10 {
		return "0" + strconv.FormatInt(n, 10)
	}
	return strconv.FormatInt(n, 10)
}

// Draft carries form values independently of HTTP and preserves invalid input.
type Draft struct {
	Basis                                                            string
	Name, Number, Address, Operator, UseBasis, Contacts, Description string
	Published                                                        bool
	Capacity, Area, PriceMinor                                       string
	Equipment, Sanitary, AccessibilityNotes, Currency, PricePeriod   string
	Accessible                                                       bool
}

func (n Node) Draft() Draft {
	return Draft{Name: n.Name, Number: n.Number, Address: n.Address, Operator: n.Operator, UseBasis: n.UseBasis, Contacts: n.Contacts, Description: n.Description, Published: n.Published,
		Capacity: strconv.FormatInt(n.Capacity, 10), Area: n.Area(), PriceMinor: strconv.FormatInt(n.PriceMinor, 10), Equipment: n.Equipment, Sanitary: n.Sanitary, Accessible: n.Accessible, AccessibilityNotes: n.AccessibilityNotes, Currency: n.Currency, PricePeriod: n.PricePeriod}
}

type Listing struct {
	Nodes        []Node
	CreatableIDs []string
}
type Detail struct {
	Photos          []Photo
	Node            Node
	Ancestors       []Node
	Children        []Node
	CanWrite        bool
	CanChangeStatus bool
	History         []BedChange
}
