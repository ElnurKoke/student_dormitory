package inventory

import (
	"errors"
	"sort"
)

// RegistryRecord is closed business data, never a public catalogue model.
type RegistryRecord struct {
	Node                                                          Node
	Location                                                      Location
	DormitoryName, Address, BuildingName, FloorNumber, RoomNumber string
	EffectiveArchived                                             bool
	PhotoCount                                                    int64
	Errors                                                        ValidationError
}

func (r RegistryRecord) PhotoApplicable() bool {
	return r.Node.Kind == Dormitory || r.Node.Kind == Room
}
func (r RegistryRecord) MissingPhotos() bool { return r.PhotoApplicable() && r.PhotoCount == 0 }

// RegistryRecords reconstructs the actual full parent chain, including archives.
// Missing ancestors fail closed instead of making an orphan appear active.
func RegistryRecords(nodes []Node, photos map[ReportPhotoOwner]int64) ([]RegistryRecord, error) {
	byKind := map[Kind]map[string]RegistryRecord{}
	var result []RegistryRecord
	for _, n := range nodes {
		record := RegistryRecord{Node: n, EffectiveArchived: n.Archived, PhotoCount: photos[ReportPhotoOwner{Kind: n.Kind, ID: n.ID}]}
		if n.Kind == Dormitory {
			record.Location.DormitoryID = n.ID
			record.DormitoryName = n.Name
			record.Address = n.Address
		} else {
			parentKind := map[Kind]Kind{Building: Dormitory, Floor: Building, Room: Floor, Bed: Room}[n.Kind]
			parent, ok := byKind[parentKind][n.ParentID]
			if !ok || parent.Location.DormitoryID != n.DormitoryID {
				return nil, ErrNotFound
			}
			record.Location = parent.Location
			record.DormitoryName = parent.DormitoryName
			record.Address = parent.Address
			record.BuildingName = parent.BuildingName
			record.FloorNumber = parent.FloorNumber
			record.RoomNumber = parent.RoomNumber
			record.EffectiveArchived = record.EffectiveArchived || parent.EffectiveArchived
			switch n.Kind {
			case Building:
				record.Location.BuildingID = n.ID
				record.BuildingName = n.Name
			case Floor:
				record.Location.FloorID = n.ID
				record.FloorNumber = n.Number
			case Room:
				record.Location.RoomID = n.ID
				record.RoomNumber = n.Number
			case Bed:
				record.Location.BedID = n.ID
			}
		}
		_, err := validate(n.Kind, n.Draft())
		var fields ValidationError
		if errors.As(err, &fields) {
			record.Errors = fields
		} else if err != nil {
			return nil, err
		}
		if byKind[n.Kind] == nil {
			byKind[n.Kind] = map[string]RegistryRecord{}
		}
		byKind[n.Kind][n.ID] = record
		result = append(result, record)
	}
	// Stable order is independent of batching and maps.
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.Node.DormitoryID != b.Node.DormitoryID {
			return a.Node.DormitoryID < b.Node.DormitoryID
		}
		if a.Node.Kind != b.Node.Kind {
			return a.Node.Kind < b.Node.Kind
		}
		return a.Node.ID < b.Node.ID
	})
	return result, nil
}
