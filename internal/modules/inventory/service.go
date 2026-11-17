package inventory

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"

	"student_dormitory/internal/modules/audit"
	"student_dormitory/internal/modules/auth"
)

type Authorizer interface {
	Authorize(context.Context, string, string, string) error
	AuthorizeTx(context.Context, *sql.Tx, string, string, string) error
	AssignedDormitories(context.Context, string, string) ([]string, error)
}
type Service struct {
	repo        *Repository
	permissions Authorizer
	audit       *audit.Recorder
}

func NewService(repo *Repository, permissions Authorizer, recorder *audit.Recorder) *Service {
	return &Service{repo: repo, permissions: permissions, audit: recorder}
}

func (s *Service) List(ctx context.Context, actor string) (Listing, error) {
	ids, err := s.permissions.AssignedDormitories(ctx, actor, "registry.read")
	if err != nil {
		return Listing{}, err
	}
	writeIDs, writeErr := s.permissions.AssignedDormitories(ctx, actor, "registry.write")
	if writeErr != nil {
		if !errors.Is(writeErr, auth.ErrForbidden) {
			return Listing{}, writeErr
		}
		writeIDs = nil
	} // No write grant means a read-only list.
	result := Listing{}
	for _, id := range ids {
		n, err := s.repo.Get(ctx, Dormitory, id)
		if errors.Is(err, ErrNotFound) {
			if contains(writeIDs, id) {
				result.CreatableIDs = append(result.CreatableIDs, id)
			}
			continue
		}
		if err != nil {
			return Listing{}, err
		}
		result.Nodes = append(result.Nodes, n)
	}
	return result, nil
}
func contains(ids []string, id string) bool {
	for _, candidate := range ids {
		if id == candidate {
			return true
		}
	}
	return false
}

func (s *Service) Detail(ctx context.Context, actor string, p Location) (Detail, error) {
	if err := s.permissions.Authorize(ctx, actor, "registry.read", p.DormitoryID); err != nil {
		return Detail{}, err
	}
	chain, err := loadChain(ctx, s.repo, p)
	if err != nil {
		return Detail{}, err
	}
	n := chain[len(chain)-1]
	detail := Detail{Node: n, Ancestors: chain[:len(chain)-1]}
	if child := n.Kind.Child(); child != "" {
		detail.Children, err = s.repo.Children(ctx, child, n.ID)
		if err != nil {
			return Detail{}, err
		}
	}
	writeErr := s.permissions.Authorize(ctx, actor, "registry.write", p.DormitoryID)
	if writeErr != nil && !errors.Is(writeErr, auth.ErrForbidden) {
		return Detail{}, writeErr
	}
	detail.CanWrite = writeErr == nil
	if n.Kind == Dormitory || n.Kind == Room {
		detail.Photos, err = s.repo.Photos(ctx, p)
		if err != nil {
			return Detail{}, err
		}
	}
	if n.Kind == Bed {
		statusErr := s.permissions.Authorize(ctx, actor, "places.status.write", p.DormitoryID)
		if statusErr != nil && !errors.Is(statusErr, auth.ErrForbidden) {
			return Detail{}, statusErr
		}
		detail.CanChangeStatus = statusErr == nil
		detail.History, err = s.repo.BedHistory(ctx, n.ID)
		if err != nil {
			return Detail{}, err
		}
	}
	return detail, nil
}

func loadChain(ctx context.Context, repo *Repository, p Location) ([]Node, error) {
	if p.DormitoryID == "" {
		return nil, ErrNotFound
	}
	parts := []struct {
		k  Kind
		id string
	}{{Dormitory, p.DormitoryID}, {Building, p.BuildingID}, {Floor, p.FloorID}, {Room, p.RoomID}, {Bed, p.BedID}}
	var chain []Node
	for _, part := range parts {
		if part.id == "" {
			continue
		}
		n, err := repo.Get(ctx, part.k, part.id)
		if err != nil {
			return nil, err
		}
		if n.DormitoryID != p.DormitoryID || (len(chain) > 0 && (chain[len(chain)-1].Kind.Child() != part.k || n.ParentID != chain[len(chain)-1].ID)) {
			return nil, ErrNotFound
		}
		chain = append(chain, n)
	}
	return chain, nil
}
func active(chain []Node) error {
	for _, n := range chain {
		if n.Archived {
			return ErrArchived
		}
	}
	return nil
}

func (s *Service) Save(ctx context.Context, actor string, p Location, k Kind, draft Draft, create bool, version int64) (Node, error) {
	// For creation p names the parent; for dormitories it names a reserved scope.
	if err := s.permissions.Authorize(ctx, actor, "registry.read", p.DormitoryID); err != nil {
		return Node{}, err
	}
	if err := s.permissions.Authorize(ctx, actor, "registry.write", p.DormitoryID); err != nil {
		return Node{}, err
	}
	if create && k == Dormitory && p.DormitoryID == "new" {
		return Node{}, ValidationError{"dormitory_id": "ID «new» зарезервирован. Выберите другое назначение общежития."}
	}
	if k == Bed {
		if err := validateBasis(draft.Basis); err != nil {
			return Node{}, err
		}
	}
	fields, err := validate(k, draft)
	if err != nil {
		return Node{}, err
	}
	var saved Node
	err = s.audit.RecordChange(ctx, func(ctx context.Context, tx *sql.Tx) (audit.Event, error) {
		if err := s.permissions.AuthorizeTx(ctx, tx, actor, "registry.read", p.DormitoryID); err != nil {
			return audit.Event{}, err
		}
		if err := s.permissions.AuthorizeTx(ctx, tx, actor, "registry.write", p.DormitoryID); err != nil {
			return audit.Event{}, err
		}
		repo := s.repo.WithTx(tx)
		var before *Node
		if create {
			saved = Node{ID: rand.Text(), Kind: k, DormitoryID: p.DormitoryID, Fields: fields}
			if k == Dormitory {
				saved.ID = p.DormitoryID
			} else {
				chain, err := loadChain(ctx, repo, p)
				if err != nil {
					return audit.Event{}, err
				}
				if chain[len(chain)-1].Kind.Child() != k {
					return audit.Event{}, ErrNotFound
				}
				if err := active(chain); err != nil {
					return audit.Event{}, err
				}
				saved.ParentID = p.ID()
				if k == Bed {
					saved.Status = BedFree
					if err := checkBedCapacity(ctx, repo, chain[len(chain)-1]); err != nil {
						return audit.Event{}, err
					}
				}
			}
			if err := repo.Insert(ctx, saved); err != nil {
				return audit.Event{}, err
			}
		} else {
			chain, err := loadChain(ctx, repo, p)
			if err != nil {
				return audit.Event{}, err
			}
			if err := active(chain); err != nil {
				return audit.Event{}, err
			}
			old := chain[len(chain)-1]
			if old.Kind != k {
				return audit.Event{}, ErrNotFound
			}
			if old.Version != version {
				return audit.Event{}, ErrConflict
			}
			before = &old
			saved = old
			saved.Fields = fields
			if k == Room {
				count, err := repo.ActiveBedCount(ctx, old.ID)
				if err != nil {
					return audit.Event{}, err
				}
				if saved.Capacity < count {
					return audit.Event{}, ValidationError{"capacity": "Вместимость не может быть меньше числа действующих койко-мест."}
				}
			}
			if err := repo.Update(ctx, saved, version); err != nil {
				return audit.Event{}, err
			}
		}
		saved, err = repo.Get(ctx, k, saved.ID)
		if err != nil {
			return audit.Event{}, err
		}
		action := "updated"
		if create {
			action = "created"
		}
		return recordInventoryChange(ctx, repo, actor, action, draft.Basis, before, saved)
	})
	return saved, err
}

func (s *Service) Archive(ctx context.Context, actor string, p Location, version int64, archived bool) (Node, error) {
	return s.ArchiveWithBasis(ctx, actor, p, version, archived, "")
}

func (s *Service) ArchiveWithBasis(ctx context.Context, actor string, p Location, version int64, archived bool, basis string) (Node, error) {
	if err := s.permissions.Authorize(ctx, actor, "registry.read", p.DormitoryID); err != nil {
		return Node{}, err
	}
	if err := s.permissions.Authorize(ctx, actor, "registry.write", p.DormitoryID); err != nil {
		return Node{}, err
	}
	if p.Kind() == Bed {
		if err := validateBasis(basis); err != nil {
			return Node{}, err
		}
	}
	var saved Node
	err := s.audit.RecordChange(ctx, func(ctx context.Context, tx *sql.Tx) (audit.Event, error) {
		if err := s.permissions.AuthorizeTx(ctx, tx, actor, "registry.read", p.DormitoryID); err != nil {
			return audit.Event{}, err
		}
		if err := s.permissions.AuthorizeTx(ctx, tx, actor, "registry.write", p.DormitoryID); err != nil {
			return audit.Event{}, err
		}
		repo := s.repo.WithTx(tx)
		chain, err := loadChain(ctx, repo, p)
		if err != nil {
			return audit.Event{}, err
		}
		old := chain[len(chain)-1]
		if old.Version != version || old.Archived == archived {
			return audit.Event{}, ErrConflict
		}
		if old.Kind == Bed && (old.Status == BedReserved || old.Status == BedOccupied) {
			return audit.Event{}, ErrBedInUse
		}
		if !archived && old.Kind == Bed {
			if err := checkBedCapacity(ctx, repo, chain[len(chain)-2]); err != nil {
				return audit.Event{}, err
			}
		}
		if archived {
			busy, err := repo.HasActiveDescendants(ctx, old)
			if err != nil {
				return audit.Event{}, err
			}
			if busy {
				return audit.Event{}, ErrActiveChildren
			}
		} else if err := active(chain[:len(chain)-1]); err != nil {
			return audit.Event{}, err
		}
		if err := repo.SetArchived(ctx, old, archived, version); err != nil {
			return audit.Event{}, err
		}
		saved, err = repo.Get(ctx, old.Kind, old.ID)
		if err != nil {
			return audit.Event{}, err
		}
		action := "restored"
		if archived {
			action = "archived"
		}
		return recordInventoryChange(ctx, repo, actor, action, basis, &old, saved)
	})
	return saved, err
}

func snapshot(n Node) map[string]any {
	m := map[string]any{"id": n.ID, "kind": n.Kind, "parent_id": n.ParentID, "dormitory_id": n.DormitoryID, "archived": n.Archived, "version": n.Version}
	switch n.Kind {
	case Dormitory:
		m["name"], m["address"], m["operator"], m["use_basis"], m["contacts"], m["description"], m["published"] = n.Name, n.Address, n.Operator, n.UseBasis, n.Contacts, n.Description, n.Published
	case Bed:
		m["number"], m["status"] = n.Number, n.Status
	case Building:
		m["name"] = n.Name
	case Floor:
		m["number"] = n.Number
	case Room:
		m["number"], m["capacity"], m["area_hundredths"], m["equipment"], m["sanitary"], m["accessible"], m["accessibility_notes"], m["price_minor"], m["currency"], m["price_period"] = n.Number, n.Capacity, n.AreaHundredths, n.Equipment, n.Sanitary, n.Accessible, n.AccessibilityNotes, n.PriceMinor, n.Currency, n.PricePeriod
	}
	return m
}
func event(actor, action string, before *Node, after Node) (audit.Event, error) {
	var old any
	if before != nil {
		old = snapshot(*before)
	}
	data, err := json.Marshal(map[string]any{"before": old, "after": snapshot(after)})
	return audit.Event{ActorID: actor, Action: "inventory." + string(after.Kind) + "." + action, ObjectType: "inventory_" + string(after.Kind), ObjectID: after.ID, Result: "success", DormitoryID: after.DormitoryID, ChangesJSON: string(data)}, err
}
