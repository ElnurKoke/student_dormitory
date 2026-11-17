package reports

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"student_dormitory/internal/modules/audit"
	"student_dormitory/internal/modules/auth"
	"student_dormitory/internal/modules/inventory"
	"student_dormitory/internal/platform/database"
)

type reportFixture struct {
	db  *sql.DB
	s   *Service
	ctx context.Context
}

func setupReports(t *testing.T) *reportFixture {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "reports.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	f := &reportFixture{db: db, ctx: ctx}
	f.s = NewService(db, auth.NewService(auth.NewRepository(db), audit.NewRecorder(db), auth.Options{}), inventory.NewRepository(db), audit.NewReadRepository())
	f.exec(t, `INSERT INTO auth_users(id,display_name) VALUES('admin','Admin'),('writer','Writer'),('sys','System'),('student','Student');
 INSERT INTO auth_user_roles(user_id,role_id) VALUES('admin','administration'),('writer','registry_staff'),('sys','system_admin'),('student','student');
 INSERT INTO auth_user_dormitories(user_id,dormitory_id) VALUES('admin','allowed'),('admin','empty'),('admin','archive'),('writer','allowed'),('sys','allowed');`)
	for _, id := range []string{"allowed", "denied", "empty", "archive"} {
		f.exec(t, `INSERT INTO inventory_dormitories(id,name,address,operator,use_basis,contacts,archived) VALUES(?,?,?,?,?,?,?)`, id, id+" Жатақхана", "Мекенжай", "Operator", "Basis", "Contacts", id == "archive")
		if id == "empty" {
			continue
		}
		f.exec(t, `INSERT INTO inventory_buildings(id,dormitory_id,name) VALUES(?,?,'Корпус')`, id+"-b", id)
		f.exec(t, `INSERT INTO inventory_floors(id,building_id,number) VALUES(?,?,'1')`, id+"-f", id+"-b")
		f.exec(t, `INSERT INTO inventory_rooms(id,floor_id,number,capacity,area_hundredths,equipment,sanitary,accessible,price_minor,currency,price_period) VALUES(?,?,'101',10,2025,'Стол','Душ',1,12345,'KZT','месяц')`, id+"-r", id+"-f")
		for i, status := range []string{"free", "reserved", "occupied", "maintenance", "unavailable", "free"} {
			f.exec(t, `INSERT INTO inventory_beds(id,room_id,number,status,archived) VALUES(?,?,?,?,?)`, fmt.Sprintf("%s-p%d", id, i), id+"-r", fmt.Sprint(i), status, i == 5)
		}
	}
	// Missing mandatory equipment is possible in legacy/imported rows. Empty
	// optional descriptions and absent photographs are separate conditions.
	f.exec(t, `UPDATE inventory_rooms SET equipment='' WHERE id='allowed-r'`)
	f.exec(t, `INSERT INTO inventory_photos(id,dormitory_id,storage_key,media_type,byte_size,width,height,position) VALUES('photo-1','allowed','key-1','image/png',1,1,1,0),('photo-2','allowed','key-2','image/png',1,1,1,1)`)
	for _, scope := range []string{"allowed", "denied", "archive"} {
		f.event(t, scope, "2026-11-15T00:00:00.000Z", "inventory.room.updated", scope+"-r", `{"before":{"equipment":"Стол"},"after":{"equipment":"<script>Қазақша</script>","price_minor":9007199254740993,"storage_key":"PRIVATE-STORAGE","token":"PRIVATE-TOKEN"}}`)
	}
	f.exec(t, `INSERT INTO audit_events(actor_id,action,object_type,object_id,result,request_id) VALUES('admin','auth.login','auth_session','PRIVATE-SESSION','success','auth-request')`)
	return f
}
func (f *reportFixture) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := f.db.ExecContext(f.ctx, q, args...); err != nil {
		t.Fatal(err)
	}
}
func (f *reportFixture) event(t *testing.T, scope, at, action, object, changes string) {
	t.Helper()
	f.exec(t, `INSERT INTO audit_events(actor_id,occurred_at,action,object_type,object_id,result,request_id,dormitory_id,changes_json) VALUES('admin',?,?,'inventory_room',?,'success','report-request',?,?)`, at, action, object, scope, changes)
}
func baseFilter() Filter { return Filter{State: "active", Page: 1} }
func TestStatisticsUsesActualAuthorizedActiveRegistry(t *testing.T) {
	f := setupReports(t)
	filter := baseFilter()
	got, err := f.s.Statistics(f.ctx, "admin", filter)
	if err != nil {
		t.Fatal(err)
	}
	if got.DormitoriesCount != 2 || got.Rooms != 1 || got.Places != 5 || got.Free != 1 || got.Reserved != 1 || got.Occupied != 1 || got.Maintenance != 1 || got.Unavailable != 1 || got.Incomplete != 1 || got.WithoutPhotos != 2 {
		t.Fatalf("wrong counts %+v", got)
	}
	if got.Free+got.Reserved+got.Occupied+got.Maintenance+got.Unavailable != got.Places || len(got.Dormitories) != 2 {
		t.Fatal("status/group totals inconsistent")
	}
	for _, group := range got.Dormitories {
		if group.ID == "denied" || group.ID == "archive" {
			t.Fatal("hidden group exposed")
		}
	}
	filter.Quality = "incomplete"
	got, err = f.s.Statistics(f.ctx, "admin", filter)
	if err != nil || got.TotalIssues != 1 || got.Places != 5 {
		t.Fatal("quality filter changes totals", err)
	}
	// Changes to status and all ancestor archive flags affect the next report.
	f.exec(t, `UPDATE inventory_beds SET status='maintenance' WHERE id='allowed-p0'`)
	got, err = f.s.Statistics(f.ctx, "admin", baseFilter())
	if err != nil || got.Free != 0 || got.Maintenance != 2 {
		t.Fatal("stale statuses")
	}
	for _, entry := range []struct{ table, id string }{{"inventory_rooms", "allowed-r"}, {"inventory_floors", "allowed-f"}, {"inventory_buildings", "allowed-b"}} {
		f.exec(t, `UPDATE `+entry.table+` SET archived=1 WHERE id=?`, entry.id)
		got, err = f.s.Statistics(f.ctx, "admin", baseFilter())
		if err != nil || got.Places != 0 || got.Rooms != 0 {
			t.Fatalf("archived parent counted: %s", entry.table)
		}
		f.exec(t, `UPDATE `+entry.table+` SET archived=0 WHERE id=?`, entry.id)
	}
}
func TestReportServicesCheckGrantsAndRevocations(t *testing.T) {
	f := setupReports(t)
	for _, actor := range []string{"sys", "student", "writer", ""} {
		_, err := f.s.Statistics(f.ctx, actor, baseFilter())
		if !errors.Is(err, auth.ErrForbidden) && !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("stats allowed for %s: %v", actor, err)
		}
		_, _, err = f.s.History(f.ctx, actor, audit.Filter{Page: 1})
		if !errors.Is(err, auth.ErrForbidden) && !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatal("history permissions bypassed")
		}
	}
	for _, actor := range []string{"sys", "student", ""} {
		if _, err := f.s.Registry(f.ctx, actor, baseFilter(), true); !errors.Is(err, auth.ErrForbidden) && !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatal("export permissions bypassed")
		}
	}
	denied := baseFilter()
	denied.DormitoryID = "denied"
	if _, err := f.s.Statistics(f.ctx, "admin", denied); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("foreign stats scope allowed")
	}
	if _, err := f.s.Registry(f.ctx, "writer", denied, true); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("foreign export scope allowed")
	}
	if _, _, err := f.s.History(f.ctx, "admin", audit.Filter{DormitoryID: "denied", Page: 1}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("foreign audit scope allowed")
	}
	f.exec(t, `DELETE FROM auth_user_dormitories WHERE user_id='admin' AND dormitory_id='allowed'`)
	got, err := f.s.Statistics(f.ctx, "admin", baseFilter())
	if err != nil || got.Places != 0 || got.DormitoriesCount != 1 {
		t.Fatal("revoked scope remains in statistics")
	}
	history, _, err := f.s.History(f.ctx, "admin", audit.Filter{Page: 1})
	if err != nil || history.Total != 1 || history.Entries[0].DormitoryID != "archive" {
		t.Fatal("revoked scope remains in history")
	}
	f.exec(t, `DELETE FROM auth_role_permissions WHERE role_id='registry_staff' AND permission_id='registry.export'`)
	if _, err := f.s.Registry(f.ctx, "writer", baseFilter(), true); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("revoked export grant remains")
	}
	f.exec(t, `UPDATE auth_users SET enabled=0 WHERE id='admin'`)
	if _, err := f.s.Statistics(f.ctx, "admin", baseFilter()); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("disabled user sees statistics")
	}
}
func TestRegistryFiltersAndExportCounts(t *testing.T) {
	f := setupReports(t)
	filter := baseFilter()
	filter.Kind = "bed"
	filter.Status = "reserved"
	got, err := f.s.Registry(f.ctx, "writer", filter, true)
	if err != nil || got.Total != 1 || got.Records[0].Node.ID != "allowed-p1" {
		t.Fatal("status filter failed", err)
	}
	filter = baseFilter()
	filter.Kind = "room"
	filter.Query = "ЖАТАҚХАНА"
	got, err = f.s.Registry(f.ctx, "writer", filter, true)
	if err != nil || got.Total != 1 {
		t.Fatal("Unicode query failed", err)
	}
	filter.Query = "denied"
	got, err = f.s.Registry(f.ctx, "writer", filter, true)
	if err != nil || got.Total != 0 {
		t.Fatal("foreign query exposed rows")
	}
	filter = baseFilter()
	filter.Quality = "incomplete"
	got, err = f.s.Registry(f.ctx, "writer", filter, true)
	if err != nil || got.Total != 1 {
		t.Fatal("completeness filter failed")
	}
	filter.Quality = "no_photos"
	got, err = f.s.Registry(f.ctx, "writer", filter, true)
	if err != nil || got.Total != 1 || got.Records[0].Node.Kind != inventory.Room {
		t.Fatal("photo filter failed")
	}
	filter = baseFilter()
	filter.State = "archived"
	got, err = f.s.Registry(f.ctx, "admin", filter, true)
	if err != nil || got.Total != 11 {
		t.Fatalf("archive filter wrong %d %v", got.Total, err)
	}
	// 10 objects from the archived chain, plus one explicitly archived bed in allowed.
	for _, row := range got.Records {
		if !row.EffectiveArchived || strings.Contains(row.Node.ID, "denied") {
			t.Fatal("archive or scope filter failed")
		}
	}
	filter.State = "all"
	got, err = f.s.Registry(f.ctx, "writer", filter, true)
	if err != nil || got.Total != 10 {
		t.Fatalf("all registry objects wrong %d", got.Total)
	}
}
func TestHistoryFiltersPaginationAndImmutableAudit(t *testing.T) {
	f := setupReports(t)
	f.event(t, "allowed", "2026-11-14T23:59:59.999Z", "inventory.room.updated", "old", `{}`)
	f.event(t, "allowed", "2026-11-16T00:00:00.000Z", "inventory.room.updated", "next", `{}`)
	got, _, err := f.s.History(f.ctx, "admin", audit.Filter{Page: 1, From: "2026-11-15", To: "2026-11-15", ObjectID: "allowed-r", ActorID: "admin", Action: "inventory.room.updated", ObjectType: "inventory_room"})
	if err != nil || got.Total != 1 {
		t.Fatal("inclusive UTC day or field filters failed", err)
	}
	raw := fmt.Sprint(got.Entries[0].Changes)
	if strings.Contains(raw, "PRIVATE-") || !strings.Contains(raw, "9007199254740993") {
		t.Fatal("unsafe or inaccurate diff", raw)
	}
	for i := 0; i < 55; i++ {
		f.event(t, "allowed", "2026-11-15T12:00:00.000Z", "test.page", fmt.Sprint(i), `{}`)
	}
	ids := map[int64]bool{}
	for page := 1; page <= 2; page++ {
		got, _, err = f.s.History(f.ctx, "admin", audit.Filter{Page: page, Action: "test.page"})
		if err != nil || got.Total != 55 || got.Pages != 2 {
			t.Fatal("audit pagination metadata", err)
		}
		for _, event := range got.Entries {
			if ids[event.ID] {
				t.Fatal("duplicate event across pages")
			}
			ids[event.ID] = true
		}
	}
	if len(ids) != 55 {
		t.Fatal("audit pagination loses events")
	}
	if _, err := f.db.ExecContext(f.ctx, `UPDATE audit_events SET action='forged'`); err == nil {
		t.Fatal("audit update allowed")
	}
	if _, err := f.db.ExecContext(f.ctx, `DELETE FROM audit_events`); err == nil {
		t.Fatal("audit delete allowed")
	}
	for _, filter := range []audit.Filter{{Page: 0}, {Page: 1, From: "2026-13-01"}, {Page: 1, From: "2026-11-16", To: "2026-11-15"}, {Page: 1, To: "9999-12-31"}} {
		if _, _, err = f.s.History(f.ctx, "admin", filter); !errors.Is(err, ErrFilter) {
			t.Fatal("invalid history filter accepted")
		}
	}
}

func TestReportsRequireRegistryReadAndReleaseCancelledTransactions(t *testing.T) {
	f := setupReports(t)
	f.exec(t, `DELETE FROM auth_role_permissions WHERE role_id='administration' AND permission_id='registry.read'`)
	if _, err := f.s.Statistics(f.ctx, "admin", baseFilter()); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("statistics bypassed registry read")
	}
	if _, err := f.s.Registry(f.ctx, "admin", baseFilter(), true); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("export bypassed registry read")
	}
	if _, _, err := f.s.History(f.ctx, "admin", audit.Filter{Page: 1}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("audit bypassed registry read")
	}
	cancelled, cancel := context.WithCancel(f.ctx)
	cancel()
	if _, err := f.s.Registry(cancelled, "writer", baseFilter(), true); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored")
	}
	if _, err := f.s.Registry(f.ctx, "writer", baseFilter(), true); err != nil {
		t.Fatal("cancelled read leaked transaction", err)
	}
}
