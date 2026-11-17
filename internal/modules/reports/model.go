package reports

import (
	"errors"
	"student_dormitory/internal/modules/inventory"
)

var ErrFilter = errors.New("invalid report filter")

const PageSize = 50

type Filter struct {
	DormitoryID, Query, State, Kind, Status, Quality string
	Page                                             int
}
type Scope struct{ ID, Name string }
type Counts struct{ DormitoriesCount, Rooms, Places, Free, Reserved, Occupied, Maintenance, Unavailable, Incomplete, WithoutPhotos int64 }
type DormitoryStats struct {
	Scope
	Counts
}
type Statistics struct {
	Counts
	Dormitories              []DormitoryStats
	Issues                   []inventory.RegistryRecord
	Scopes                   []Scope
	AsOf                     string
	TotalIssues, Page, Pages int
}
type Registry struct {
	Records            []inventory.RegistryRecord
	Scopes             []Scope
	AsOf               string
	Total, Page, Pages int
}
