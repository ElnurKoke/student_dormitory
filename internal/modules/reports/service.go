package reports

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"student_dormitory/internal/modules/audit"
	"student_dormitory/internal/modules/auth"
	"student_dormitory/internal/modules/inventory"
	"student_dormitory/internal/platform/database"
)

type Authorizer interface {
	AuthorizeTx(context.Context, *sql.Tx, string, string, string) error
	AssignedDormitoriesTx(context.Context, *sql.Tx, string, string) ([]string, error)
}
type Service struct {
	db          *sql.DB
	permissions Authorizer
	inventory   *inventory.Repository
	audit       *audit.ReadRepository
}

func NewService(db *sql.DB, p Authorizer, i *inventory.Repository, a *audit.ReadRepository) *Service {
	return &Service{db: db, permissions: p, inventory: i, audit: a}
}
func validText(s string, max int) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= max && !strings.ContainsRune(s, 0)
}
func validateFilter(f Filter) error {
	for _, s := range []string{f.DormitoryID, f.Query} {
		if !validText(s, 200) {
			return ErrFilter
		}
	}
	if f.Page < 1 || f.Page > 1000000 {
		return ErrFilter
	}
	if f.State != "active" && f.State != "archived" && f.State != "all" {
		return ErrFilter
	}
	if f.Kind != "" && f.Kind != "dormitory" && f.Kind != "building" && f.Kind != "floor" && f.Kind != "room" && f.Kind != "bed" {
		return ErrFilter
	}
	if f.Status != "" && f.Status != "free" && f.Status != "reserved" && f.Status != "occupied" && f.Status != "maintenance" && f.Status != "unavailable" {
		return ErrFilter
	}
	if f.Quality != "" && f.Quality != "incomplete" && f.Quality != "no_photos" {
		return ErrFilter
	}
	return nil
}

// scopes intersects the operation grant with registry.read assignments. Checks
// and all following reads share tx, so revocations cannot race between them.
func (s *Service) scopes(ctx context.Context, tx *sql.Tx, actor, permission, selected string) ([]string, error) {
	ids, err := s.permissions.AssignedDormitoriesTx(ctx, tx, actor, permission)
	if err != nil {
		return nil, err
	}
	read, err := s.permissions.AssignedDormitoriesTx(ctx, tx, actor, "registry.read")
	if err != nil {
		return nil, err
	}
	readable := map[string]bool{}
	for _, id := range read {
		readable[id] = true
	}
	var allowed []string
	for _, id := range ids {
		if readable[id] {
			allowed = append(allowed, id)
		}
	}
	if selected != "" {
		if err := s.permissions.AuthorizeTx(ctx, tx, actor, permission, selected); err != nil {
			return nil, err
		}
		if err := s.permissions.AuthorizeTx(ctx, tx, actor, "registry.read", selected); err != nil {
			return nil, err
		}
		for _, id := range allowed {
			if id == selected {
				return []string{id}, nil
			}
		}
		return nil, auth.ErrForbidden
	}
	return allowed, nil
}
func (s *Service) records(ctx context.Context, tx *sql.Tx, ids []string) ([]inventory.RegistryRecord, []Scope, string, error) {
	repo := s.inventory.WithTx(tx)
	nodes, err := repo.ReportNodes(ctx, ids)
	if err != nil {
		return nil, nil, "", err
	}
	photos, err := repo.ReportPhotoCounts(ctx, ids)
	if err != nil {
		return nil, nil, "", err
	}
	records, err := inventory.RegistryRecords(nodes, photos)
	if err != nil {
		return nil, nil, "", err
	}
	var scopes []Scope
	for _, row := range records {
		if row.Node.Kind == inventory.Dormitory {
			scopes = append(scopes, Scope{ID: row.Node.ID, Name: row.Node.Name})
		}
	}
	sort.Slice(scopes, func(i, j int) bool {
		if scopes[i].Name != scopes[j].Name {
			return scopes[i].Name < scopes[j].Name
		}
		return scopes[i].ID < scopes[j].ID
	})
	// Timestamp is an observation time of the transaction, not a process forecast.
	return records, scopes, time.Now().UTC().Format(time.RFC3339Nano), nil
}
func (s *Service) Statistics(ctx context.Context, actor string, f Filter) (Statistics, error) {
	var result Statistics
	if err := validateFilter(f); err != nil {
		return result, err
	}
	// Statistics always describes the active registry, including unpublished stock.
	if f.Query != "" || f.State != "active" || f.Kind != "" || f.Status != "" {
		return result, ErrFilter
	}
	err := database.WithinTx(ctx, s.db, func(ctx context.Context, tx *sql.Tx) error {
		ids, err := s.scopes(ctx, tx, actor, "statistics.read", f.DormitoryID)
		if err != nil {
			return err
		}
		rows, scopes, asOf, err := s.records(ctx, tx, ids)
		if err != nil {
			return err
		}
		result.Scopes, result.AsOf, result.Page = scopes, asOf, f.Page
		groups := map[string]*DormitoryStats{}
		for _, scope := range scopes {
			groups[scope.ID] = &DormitoryStats{Scope: scope}
		}
		var issues []inventory.RegistryRecord
		add := func(c *Counts, row inventory.RegistryRecord) {
			switch row.Node.Kind {
			case inventory.Dormitory:
				c.DormitoriesCount++
			case inventory.Room:
				c.Rooms++
			case inventory.Bed:
				c.Places++
				switch row.Node.Status {
				case inventory.BedFree:
					c.Free++
				case inventory.BedReserved:
					c.Reserved++
				case inventory.BedOccupied:
					c.Occupied++
				case inventory.BedMaintenance:
					c.Maintenance++
				case inventory.BedUnavailable:
					c.Unavailable++
				}
			}
			if row.PhotoApplicable() {
				if len(row.Errors) > 0 {
					c.Incomplete++
				}
				if row.MissingPhotos() {
					c.WithoutPhotos++
				}
			}
		}
		for _, row := range rows {
			if row.EffectiveArchived {
				continue
			}
			add(&result.Counts, row)
			add(&groups[row.Node.DormitoryID].Counts, row)
			if row.PhotoApplicable() && (len(row.Errors) > 0 || row.MissingPhotos()) {
				if f.Quality == "incomplete" && len(row.Errors) == 0 {
					continue
				}
				if f.Quality == "no_photos" && !row.MissingPhotos() {
					continue
				}
				issues = append(issues, row)
			}
		}
		for _, scope := range scopes {
			if group := groups[scope.ID]; group.DormitoriesCount > 0 {
				result.Dormitories = append(result.Dormitories, *group)
			}
		}
		result.TotalIssues = len(issues)
		result.Pages = (len(issues) + PageSize - 1) / PageSize
		start := (f.Page - 1) * PageSize
		if start < len(issues) {
			result.Issues = issues[start:min(start+PageSize, len(issues))]
		}
		return nil
	})
	if err != nil {
		return Statistics{}, err
	}
	return result, nil
}
func (s *Service) Registry(ctx context.Context, actor string, f Filter, all bool) (Registry, error) {
	var result Registry
	if err := validateFilter(f); err != nil {
		return result, err
	}
	err := database.WithinTx(ctx, s.db, func(ctx context.Context, tx *sql.Tx) error {
		ids, err := s.scopes(ctx, tx, actor, "registry.export", f.DormitoryID)
		if err != nil {
			return err
		}
		rows, scopes, asOf, err := s.records(ctx, tx, ids)
		if err != nil {
			return err
		}
		result.Scopes, result.AsOf, result.Page = scopes, asOf, f.Page
		var matches []inventory.RegistryRecord
		for _, row := range rows {
			if f.State == "active" && row.EffectiveArchived || f.State == "archived" && !row.EffectiveArchived {
				continue
			}
			if f.Kind != "" && string(row.Node.Kind) != f.Kind {
				continue
			}
			if f.Status != "" && (row.Node.Kind != inventory.Bed || string(row.Node.Status) != f.Status) {
				continue
			}
			if f.Quality == "incomplete" && len(row.Errors) == 0 || f.Quality == "no_photos" && !row.MissingPhotos() {
				continue
			}
			text := strings.Join([]string{row.Node.ID, row.Node.Label(), row.DormitoryName, row.Address, row.BuildingName, row.FloorNumber, row.RoomNumber}, " ")
			if !strings.Contains(strings.ToLower(text), strings.ToLower(strings.TrimSpace(f.Query))) {
				continue
			}
			matches = append(matches, row)
		}
		result.Total = len(matches)
		result.Pages = (len(matches) + PageSize - 1) / PageSize
		if all {
			result.Records = matches
		} else {
			start := (f.Page - 1) * PageSize
			if start < len(matches) {
				result.Records = matches[start:min(start+PageSize, len(matches))]
			}
		}
		return nil
	})
	if err != nil {
		return Registry{}, err
	}
	return result, nil
}
func (s *Service) History(ctx context.Context, actor string, f audit.Filter) (audit.Listing, []Scope, error) {
	if f.Page < 1 || f.Page > 1000000 {
		return audit.Listing{}, nil, ErrFilter
	}
	for _, text := range []string{f.DormitoryID, f.ActorID, f.Action, f.ObjectType, f.ObjectID} {
		if !validText(text, 200) {
			return audit.Listing{}, nil, ErrFilter
		}
	}
	var from, to time.Time
	var err error
	if f.From != "" {
		from, err = time.Parse("2006-01-02", f.From)
		if err != nil || from.Year() < 1 || from.Year() > 9998 {
			return audit.Listing{}, nil, ErrFilter
		}
		f.From = from.Format("2006-01-02T15:04:05.000Z")
	}
	if f.To != "" {
		to, err = time.Parse("2006-01-02", f.To)
		if err != nil || to.Year() < 1 || to.Year() > 9998 || !from.IsZero() && from.After(to) {
			return audit.Listing{}, nil, ErrFilter
		}
		f.To = to.AddDate(0, 0, 1).Format("2006-01-02T15:04:05.000Z")
	}
	var result audit.Listing
	var scopes []Scope
	err = database.WithinTx(ctx, s.db, func(ctx context.Context, tx *sql.Tx) error {
		ids, err := s.scopes(ctx, tx, actor, "audit.read", f.DormitoryID)
		if err != nil {
			return err
		}
		// Names are obtained only through the inventory contract, never by the HTTP layer.
		nodes, err := s.inventory.WithTx(tx).ReportDormitories(ctx, ids)
		if err != nil {
			return err
		}
		for _, n := range nodes {
			if n.Kind == inventory.Dormitory {
				scopes = append(scopes, Scope{ID: n.ID, Name: n.Name})
			}
		}
		result, err = s.audit.List(ctx, tx, ids, f)
		return err
	})
	if err != nil {
		return audit.Listing{}, nil, err
	}
	return result, scopes, nil
}
