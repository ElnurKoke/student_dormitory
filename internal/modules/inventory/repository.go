package inventory

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/mattn/go-sqlite3"
)

type executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
type Repository struct {
	db   executor
	root *sql.DB
}

func NewRepository(db *sql.DB) *Repository          { return &Repository{db: db, root: db} }
func (r *Repository) WithTx(tx *sql.Tx) *Repository { return &Repository{db: tx} }

// SQL identifiers come exclusively from this closed set, never form values.
type schema struct {
	table, parent, from, root string
	columns                   []string
}

func schemaFor(k Kind) (schema, error) {
	s := schema{}
	switch k {
	case Dormitory:
		s = schema{table: "inventory_dormitories", from: "inventory_dormitories n", root: "n.id", columns: []string{"name", "address", "operator", "use_basis", "contacts", "description", "published"}}
	case Building:
		s = schema{table: "inventory_buildings", parent: "dormitory_id", from: "inventory_buildings n", root: "n.dormitory_id", columns: []string{"name"}}
	case Floor:
		s = schema{table: "inventory_floors", parent: "building_id", from: "inventory_floors n JOIN inventory_buildings b ON b.id=n.building_id", root: "b.dormitory_id", columns: []string{"number"}}
	case Room:
		s = schema{table: "inventory_rooms", parent: "floor_id", from: "inventory_rooms n JOIN inventory_floors f ON f.id=n.floor_id JOIN inventory_buildings b ON b.id=f.building_id", root: "b.dormitory_id", columns: []string{"number", "capacity", "area_hundredths", "equipment", "sanitary", "accessible", "accessibility_notes", "price_minor", "currency", "price_period"}}
	case Bed:
		s = schema{table: "inventory_beds", parent: "room_id", from: "inventory_beds n JOIN inventory_rooms r ON r.id=n.room_id JOIN inventory_floors f ON f.id=r.floor_id JOIN inventory_buildings b ON b.id=f.building_id", root: "b.dormitory_id", columns: []string{"number", "status"}}
	default:
		return s, ErrNotFound
	}
	return s, nil
}
func selectSQL(s schema) string {
	parent := "''"
	if s.parent != "" {
		parent = "n." + s.parent
	}
	columns := []string{"n.id", parent, s.root, "n.archived", "n.version", "n.updated_at"}
	for _, column := range s.columns {
		columns = append(columns, "n."+column)
	}
	return "SELECT " + strings.Join(columns, ",") + " FROM " + s.from
}
func scanNode(scanner interface{ Scan(...any) error }, k Kind) (Node, error) {
	n := Node{Kind: k}
	dest := []any{&n.ID, &n.ParentID, &n.DormitoryID, &n.Archived, &n.Version, &n.UpdatedAt}
	switch k {
	case Dormitory:
		dest = append(dest, &n.Name, &n.Address, &n.Operator, &n.UseBasis, &n.Contacts, &n.Description, &n.Published)
	case Building:
		dest = append(dest, &n.Name)
	case Floor:
		dest = append(dest, &n.Number)
	case Room:
		dest = append(dest, &n.Number, &n.Capacity, &n.AreaHundredths, &n.Equipment, &n.Sanitary, &n.Accessible, &n.AccessibilityNotes, &n.PriceMinor, &n.Currency, &n.PricePeriod)
	case Bed:
		dest = append(dest, &n.Number, &n.Status)
	}
	err := scanner.Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, ErrNotFound
	}
	return n, err
}
func (r *Repository) Get(ctx context.Context, k Kind, id string) (Node, error) {
	s, err := schemaFor(k)
	if err != nil {
		return Node{}, err
	}
	return scanNode(r.db.QueryRowContext(ctx, selectSQL(s)+" WHERE n.id = ?", id), k)
}
func (r *Repository) Children(ctx context.Context, k Kind, parentID string) ([]Node, error) {
	s, err := schemaFor(k)
	if err != nil {
		return nil, err
	}
	if s.parent == "" {
		return nil, ErrNotFound
	}
	rows, err := r.db.QueryContext(ctx, selectSQL(s)+" WHERE n."+s.parent+" = ? ORDER BY n.archived, n."+s.columns[0]+", n.id", parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nodes []Node
	for rows.Next() {
		node, err := scanNode(rows, k)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, rows.Err()
}
func values(n Node) []any {
	switch n.Kind {
	case Dormitory:
		return []any{n.Name, n.Address, n.Operator, n.UseBasis, n.Contacts, n.Description, n.Published}
	case Building:
		return []any{n.Name}
	case Floor:
		return []any{n.Number}
	case Room:
		return []any{n.Number, n.Capacity, n.AreaHundredths, n.Equipment, n.Sanitary, n.Accessible, n.AccessibilityNotes, n.PriceMinor, n.Currency, n.PricePeriod}
	case Bed:
		return []any{n.Number, n.Status}
	}
	return nil
}
func (r *Repository) Insert(ctx context.Context, n Node) error {
	s, err := schemaFor(n.Kind)
	if err != nil {
		return err
	}
	columns := []string{"id"}
	args := []any{n.ID}
	if s.parent != "" {
		columns = append(columns, s.parent)
		args = append(args, n.ParentID)
	}
	columns = append(columns, s.columns...)
	args = append(args, values(n)...)
	_, err = r.db.ExecContext(ctx, "INSERT INTO "+s.table+" ("+strings.Join(columns, ",")+") VALUES ("+strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+")", args...)
	return writeError(n.Kind, err)
}
func (r *Repository) Update(ctx context.Context, n Node, version int64) error {
	s, err := schemaFor(n.Kind)
	if err != nil {
		return err
	}
	assignments := []string{}
	for _, column := range s.columns {
		assignments = append(assignments, column+" = ?")
	}
	args := append(values(n), n.ID, version)
	result, err := r.db.ExecContext(ctx, "UPDATE "+s.table+" SET "+strings.Join(assignments, ",")+", version = version + 1, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ? AND version = ? AND archived = 0", args...)
	if err != nil {
		return writeError(n.Kind, err)
	}
	return changed(result)
}
func (r *Repository) SetArchived(ctx context.Context, n Node, archived bool, version int64) error {
	s, err := schemaFor(n.Kind)
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, "UPDATE "+s.table+" SET archived = ?, version = version + 1, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ? AND version = ?", archived, n.ID, version)
	if err != nil {
		return err
	}
	return changed(result)
}
func changed(result sql.Result) error {
	count, err := result.RowsAffected()
	if err == nil && count != 1 {
		return ErrConflict
	}
	return err
}
func writeError(k Kind, err error) error {
	var e sqlite3.Error
	if errors.As(err, &e) && (e.ExtendedCode == sqlite3.ErrConstraintUnique || e.ExtendedCode == sqlite3.ErrConstraintPrimaryKey) {
		if k == Room {
			return ValidationError{"number": "Комната с таким номером уже существует на этом этаже, в том числе в архиве."}
		}
		if k == Bed {
			return ValidationError{"number": "Койко-место с таким обозначением уже существует в этой комнате, в том числе в архиве."}
		}
		return ErrConflict
	}
	return err
}

func (r *Repository) HasActiveDescendants(ctx context.Context, n Node) (bool, error) {
	var query string
	switch n.Kind {
	case Dormitory:
		query = `SELECT EXISTS(
	 SELECT 1 FROM inventory_buildings b WHERE b.dormitory_id = ?1 AND b.archived = 0
	 UNION ALL SELECT 1 FROM inventory_floors f JOIN inventory_buildings b ON b.id=f.building_id WHERE b.dormitory_id = ?1 AND f.archived = 0
	 UNION ALL SELECT 1 FROM inventory_rooms r JOIN inventory_floors f ON f.id=r.floor_id JOIN inventory_buildings b ON b.id=f.building_id WHERE b.dormitory_id = ?1 AND r.archived = 0)`
	case Building:
		query = `SELECT EXISTS(SELECT 1 FROM inventory_floors WHERE building_id = ?1 AND archived = 0
	 UNION ALL SELECT 1 FROM inventory_rooms r JOIN inventory_floors f ON f.id=r.floor_id WHERE f.building_id = ?1 AND r.archived = 0)`
	case Floor:
		query = `SELECT EXISTS(SELECT 1 FROM inventory_rooms WHERE floor_id = ? AND archived = 0)`
	case Room:
		query = `SELECT EXISTS(SELECT 1 FROM inventory_beds WHERE room_id = ? AND archived = 0)`
	case Bed:
		return false, nil
	default:
		return false, ErrNotFound
	}
	var active bool
	err := r.db.QueryRowContext(ctx, query, n.ID).Scan(&active)
	if err == nil && !active && n.Kind != Bed {
		// Also inspect beds below archived intermediate nodes; never hide active descendants.
		var bedQuery string
		switch n.Kind {
		case Dormitory:
			bedQuery = `SELECT EXISTS(SELECT 1 FROM inventory_beds n JOIN inventory_rooms r ON r.id=n.room_id JOIN inventory_floors f ON f.id=r.floor_id JOIN inventory_buildings b ON b.id=f.building_id WHERE b.dormitory_id = ? AND n.archived = 0)`
		case Building:
			bedQuery = `SELECT EXISTS(SELECT 1 FROM inventory_beds n JOIN inventory_rooms r ON r.id=n.room_id JOIN inventory_floors f ON f.id=r.floor_id WHERE f.building_id = ? AND n.archived = 0)`
		case Floor:
			bedQuery = `SELECT EXISTS(SELECT 1 FROM inventory_beds n JOIN inventory_rooms r ON r.id=n.room_id WHERE r.floor_id = ? AND n.archived = 0)`
		}
		if bedQuery != "" {
			err = r.db.QueryRowContext(ctx, bedQuery, n.ID).Scan(&active)
		}
	}
	return active, err
}

func (r *Repository) HasInventory(ctx context.Context) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM inventory_dormitories)`).Scan(&exists)
	return exists, err
}
