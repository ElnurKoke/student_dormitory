package inventory

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"student_dormitory/internal/modules/audit"
	"student_dormitory/internal/modules/auth"
	"student_dormitory/internal/platform/database"
	"student_dormitory/internal/platform/requestid"
	"testing"
	"time"
)

type fixture struct {
	db  *sql.DB
	s   *Service
	ctx context.Context
}

func setup(t *testing.T) *fixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(requestid.WithID(context.Background(), requestid.New()), 15*time.Second)
	t.Cleanup(cancel)
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "inventory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.ExecContext(ctx, `INSERT INTO auth_users (id, display_name) VALUES ('writer-a','Writer A'),('writer-b','Writer B'),('sys','System');
 INSERT INTO auth_user_roles (user_id,role_id) VALUES ('writer-a','registry_staff'),('writer-b','registry_staff'),('sys','system_admin');
 INSERT INTO auth_user_dormitories (user_id,dormitory_id) VALUES ('writer-a','dorm-a'),('writer-b','dorm-b'),('sys','dorm-a');`)
	if err != nil {
		t.Fatal(err)
	}
	permissions := auth.NewService(auth.NewRepository(db), audit.NewRecorder(db), auth.Options{})
	return &fixture{db: db, s: NewService(NewRepository(db), permissions, audit.NewRecorder(db)), ctx: ctx}
}
func dormDraft() Draft {
	return Draft{Name: "Общежитие", Address: "Адрес", Operator: "Оператор", UseBasis: "Договор", Contacts: "Телефон оператора", Description: "Описание", Published: true}
}
func roomDraft() Draft {
	return Draft{Number: "101", Capacity: "2", Area: "18,50", Equipment: "Столы и кровати", Sanitary: "Душ на этаже", Accessible: true, AccessibilityNotes: "Есть лифт", PriceMinor: "12345", Currency: "kzt", PricePeriod: "месяц"}
}
func create(t *testing.T, f *fixture, p Location, k Kind, d Draft) Node {
	t.Helper()
	n, err := f.s.Save(f.ctx, "writer-a", p, k, d, true, 0)
	if err != nil {
		t.Fatalf("create %s: %v", k, err)
	}
	return n
}
func hierarchy(t *testing.T, f *fixture) (Location, []Node) {
	t.Helper()
	p := Location{DormitoryID: "dorm-a"}
	d := create(t, f, p, Dormitory, dormDraft())
	b := create(t, f, p, Building, Draft{Name: "Корпус A"})
	p.BuildingID = b.ID
	floor := create(t, f, p, Floor, Draft{Number: "1"})
	p.FloorID = floor.ID
	r := create(t, f, p, Room, roomDraft())
	p.RoomID = r.ID
	return p, []Node{d, b, floor, r}
}
func TestInventoryCRUDAndExactAmounts(t *testing.T) {
	f := setup(t)
	p, nodes := hierarchy(t, f)
	detail, err := f.s.Detail(f.ctx, "writer-a", p)
	if err != nil {
		t.Fatal(err)
	}
	room := detail.Node
	if room.PriceMinor != 12345 || room.Currency != "KZT" || room.PricePeriod != "месяц" || room.AreaHundredths != 1850 || room.Capacity != 2 || !room.Accessible {
		t.Fatalf("incorrect persisted room: %+v", room)
	}
	if len(detail.Ancestors) != 3 || room.DormitoryID != "dorm-a" {
		t.Fatal("broken hierarchy")
	}
	draft := room.Draft()
	draft.PriceMinor = "9007199254740993"
	draft.Number = "102"
	saved, err := f.s.Save(f.ctx, "writer-a", p, Room, draft, false, room.Version)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID != room.ID || saved.ParentID != room.ParentID || saved.PriceMinor != 9007199254740993 || saved.Version != 2 {
		t.Fatal("ID, links or money changed incorrectly")
	}
	if _, err := f.s.Save(f.ctx, "writer-a", p, Room, draft, false, room.Version); !errors.Is(err, ErrConflict) {
		t.Fatal("stale version accepted")
	}
	var storageType, changes, actor, request string
	if err := f.db.QueryRowContext(f.ctx, "SELECT typeof(price_minor) FROM inventory_rooms WHERE id = ?", room.ID).Scan(&storageType); err != nil || storageType != "integer" {
		t.Fatal("money is not an integer")
	}
	if err := f.db.QueryRowContext(f.ctx, "SELECT changes_json, actor_id, request_id FROM audit_events WHERE object_id = ? ORDER BY id DESC LIMIT 1", room.ID).Scan(&changes, &actor, &request); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(changes, `"price_minor":9007199254740993`) || !strings.Contains(changes, `"price_minor":12345`) || actor != "writer-a" || request != requestid.FromContext(f.ctx) {
		t.Fatal("audit lacks accurate before/after, actor or request")
	}
	for _, n := range nodes {
		if n.ID == "" {
			t.Fatal("missing permanent ID")
		}
	}
	if _, err := f.db.ExecContext(f.ctx, "UPDATE inventory_rooms SET id = ? WHERE id = ?", "replacement", room.ID); err == nil {
		t.Fatal("permanent ID mutable in DB")
	}
	if _, err := f.db.ExecContext(f.ctx, "UPDATE inventory_rooms SET price_minor = 1.25 WHERE id = ?", room.ID); err == nil {
		t.Fatal("fractional minor units accepted in DB")
	}
}
func TestRoomNumberUniqueIncludingArchive(t *testing.T) {
	f := setup(t)
	p, _ := hierarchy(t, f)
	if _, err := f.s.Archive(f.ctx, "writer-a", p, 1, true); err != nil {
		t.Fatal(err)
	}
	draft := roomDraft()
	draft.Number = " 101 "
	if _, err := f.s.Save(f.ctx, "writer-a", p.Parent(), Room, draft, true, 0); err == nil {
		t.Fatal("duplicate archived room accepted")
	}
	parent := p.Parent().Parent()
	floor := create(t, f, parent, Floor, Draft{Number: "2"})
	parent.FloorID = floor.ID
	if _, err := f.s.Save(f.ctx, "writer-a", parent, Room, draft, true, 0); err != nil {
		t.Fatalf("same number on another floor rejected: %v", err)
	}
}
func TestArchiveRetainsLinksAndRequiresOrder(t *testing.T) {
	f := setup(t)
	p, nodes := hierarchy(t, f)
	if _, err := f.s.Archive(f.ctx, "writer-a", Location{DormitoryID: p.DormitoryID}, 1, true); !errors.Is(err, ErrActiveChildren) {
		t.Fatal("archived parent with active descendants")
	}
	paths := []Location{p, p.Parent(), p.Parent().Parent(), {DormitoryID: p.DormitoryID}}
	for _, path := range paths {
		if _, err := f.s.Archive(f.ctx, "writer-a", path, 1, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.s.Archive(f.ctx, "writer-a", p, 2, false); !errors.Is(err, ErrArchived) {
		t.Fatal("restored under archived parent")
	}
	if _, err := f.s.Save(f.ctx, "writer-a", p, Room, roomDraft(), false, 2); !errors.Is(err, ErrArchived) {
		t.Fatal("edited archived hierarchy")
	}
	if _, err := f.s.Save(f.ctx, "writer-a", p.Parent(), Room, roomDraft(), true, 0); !errors.Is(err, ErrArchived) {
		t.Fatal("created under archived parent")
	}
	for i := len(paths) - 1; i >= 0; i-- {
		detail, err := f.s.Detail(f.ctx, "writer-a", paths[i])
		if err != nil || !detail.Node.Archived {
			t.Fatal("archive hidden from administration")
		}
		if _, err := f.s.Archive(f.ctx, "writer-a", paths[i], 2, false); err != nil {
			t.Fatal(err)
		}
	}
	detail, err := f.s.Detail(f.ctx, "writer-a", p)
	if err != nil || detail.Node.ParentID != nodes[2].ID || detail.Node.ID != nodes[3].ID {
		t.Fatal("archive damaged links or ID")
	}
	// Referenced parent records cannot be deleted, irrespective of archive state.
	if _, err := f.db.ExecContext(f.ctx, "DELETE FROM inventory_floors WHERE id = ?", p.FloorID); err == nil {
		t.Fatal("deleted referenced floor")
	}
	if _, err := f.db.ExecContext(f.ctx, "DELETE FROM inventory_buildings WHERE id = ?", p.BuildingID); err == nil {
		t.Fatal("deleted referenced building")
	}
	if _, err := f.db.ExecContext(f.ctx, "DELETE FROM inventory_dormitories WHERE id = ?", p.DormitoryID); err == nil {
		t.Fatal("deleted referenced dormitory")
	}
	// A future dependent table also gets real SQLite FK protection for rooms.
	if _, err := f.db.ExecContext(f.ctx, "CREATE TABLE dependent_fixture (room_id TEXT REFERENCES inventory_rooms(id) ON DELETE RESTRICT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecContext(f.ctx, "INSERT INTO dependent_fixture (room_id) VALUES (?)", p.RoomID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecContext(f.ctx, "DELETE FROM inventory_rooms WHERE id = ?", p.RoomID); err == nil {
		t.Fatal("deleted referenced room")
	}
}
func TestScopeAndAuditFailure(t *testing.T) {
	f := setup(t)
	p, _ := hierarchy(t, f)
	other := Location{DormitoryID: "dorm-b"}
	if _, err := f.s.Save(f.ctx, "writer-b", other, Dormitory, dormDraft(), true, 0); err != nil {
		t.Fatal(err)
	}
	for _, actor := range []string{"writer-b", "sys"} {
		if _, err := f.s.Detail(f.ctx, actor, p); !errors.Is(err, auth.ErrForbidden) {
			t.Fatal("cross-scope or system admin read allowed")
		}
		if _, err := f.s.Save(f.ctx, actor, p, Room, roomDraft(), false, 1); !errors.Is(err, auth.ErrForbidden) {
			t.Fatal("cross-scope or system admin write allowed")
		}
	}
	spoofed := p
	spoofed.DormitoryID = "dorm-b"
	if _, err := f.s.Detail(f.ctx, "writer-b", spoofed); !errors.Is(err, ErrNotFound) {
		t.Fatal("nested path bypasses actual ownership")
	}
	if _, err := f.db.ExecContext(f.ctx, `CREATE TRIGGER reject_inventory_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'audit offline'); END`); err != nil {
		t.Fatal(err)
	}
	draft := roomDraft()
	draft.Number = "changed"
	if _, err := f.s.Save(f.ctx, "writer-a", p, Room, draft, false, 1); err == nil {
		t.Fatal("unaudited edit committed")
	}
	if _, err := f.s.Archive(f.ctx, "writer-a", p, 1, true); err == nil {
		t.Fatal("unaudited archive committed")
	}
	draft.Number = "new-room"
	if _, err := f.s.Save(f.ctx, "writer-a", p.Parent(), Room, draft, true, 0); err == nil {
		t.Fatal("unaudited creation committed")
	}
	detail, err := f.s.Detail(f.ctx, "writer-a", p)
	if err != nil || detail.Node.Number != "101" || detail.Node.Version != 1 || detail.Node.Archived {
		t.Fatal("business change survived audit failure")
	}
	var count int
	if err := f.db.QueryRowContext(f.ctx, "SELECT count(*) FROM inventory_rooms").Scan(&count); err != nil || count != 1 {
		t.Fatal("failed creation survived rollback")
	}
}
func TestValidation(t *testing.T) {
	for field, value := range map[string]string{"capacity": "1.5", "area": "1.001", "price_minor": "9223372036854775808", "currency": "12K", "price_period": ""} {
		d := roomDraft()
		switch field {
		case "capacity":
			d.Capacity = value
		case "area":
			d.Area = value
		case "price_minor":
			d.PriceMinor = value
		case "currency":
			d.Currency = value
		case "price_period":
			d.PricePeriod = value
		}
		_, err := validate(Room, d)
		var fields ValidationError
		if !errors.As(err, &fields) || fields[field] == "" {
			t.Fatalf("missing validation for %s", field)
		}
	}
	for _, bad := range []string{"-1", "1e3", "NaN", "1.00", ""} {
		d := roomDraft()
		d.PriceMinor = bad
		if _, err := validate(Room, d); err == nil {
			t.Fatalf("bad money accepted: %s", bad)
		}
	}
}
