package inventory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"student_dormitory/internal/modules/auth"
)

func bed(t *testing.T, f *fixture, room Location, number string) (Location, Node) {
	t.Helper()
	n := create(t, f, room, Bed, Draft{Number: number, Basis: "Акт ввода места"})
	room.BedID = n.ID
	return room, n
}

func counts(t *testing.T, f *fixture) (int, int) {
	t.Helper()
	var history, audit int
	if err := f.db.QueryRowContext(f.ctx, "SELECT count(*) FROM inventory_bed_history").Scan(&history); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRowContext(f.ctx, "SELECT count(*) FROM audit_events WHERE object_type = 'inventory_bed'").Scan(&audit); err != nil {
		t.Fatal(err)
	}
	return history, audit
}

func TestBedIdentityHistoryAndAudit(t *testing.T) {
	f := setupBed(t)
	room, _ := hierarchy(t, f)
	p, n := bed(t, f, room, " A-1 ")
	if n.Number != "A-1" || n.Status != BedFree || n.Version != 1 {
		t.Fatalf("bad initial bed: %+v", n)
	}
	edited, err := f.s.Save(f.ctx, "writer-a", p, Bed, Draft{Number: "A-2", Basis: "Изменение обозначения"}, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	if edited.ID != n.ID || edited.ParentID != room.RoomID || edited.Status != BedFree {
		t.Fatal("identity or status changed on edit")
	}
	n, err = f.s.ChangeBedStatus(f.ctx, "writer-a", p, 2, BedMaintenance, "  Акт № 42  ")
	if err != nil {
		t.Fatal(err)
	}
	detail, err := f.s.Detail(f.ctx, "writer-a", p)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Ancestors) != 4 || len(detail.History) != 3 {
		t.Fatal("missing chain or history")
	}
	h := detail.History[0]
	if h.OldStatus != BedFree || h.NewStatus != BedMaintenance || h.ActorID != "writer-a" || h.Basis != "Акт № 42" || h.OccurredAt != n.UpdatedAt || h.Version != 3 {
		t.Fatalf("bad status history: %+v", h)
	}
	if detail.History[2].OldStatus != "" || detail.History[2].NewStatus != BedFree {
		t.Fatal("missing creation history")
	}
	var data, actor, at string
	if err := f.db.QueryRowContext(f.ctx, "SELECT changes_json, actor_id, occurred_at FROM audit_events WHERE object_id = ? ORDER BY id DESC LIMIT 1", n.ID).Scan(&data, &actor, &at); err != nil {
		t.Fatal(err)
	}
	var diff struct {
		HistoryID     int64 `json:"history_id"`
		Before, After map[string]any
	}
	if err := json.Unmarshal([]byte(data), &diff); err != nil {
		t.Fatal(err)
	}
	if diff.HistoryID != h.ID || diff.Before["status"] != "free" || diff.After["status"] != "maintenance" || actor != "writer-a" || at == "" || strings.Contains(data, "Акт") {
		t.Fatalf("bad audit: %s", data)
	}
	for _, query := range []string{"UPDATE inventory_beds SET id = 'replacement' WHERE id = ?", "UPDATE inventory_beds SET room_id = 'replacement' WHERE id = ?"} {
		if _, err := f.db.ExecContext(f.ctx, query, n.ID); err == nil {
			t.Fatal("mutable bed identity")
		}
	}
	if _, err := f.s.ChangeBedStatus(f.ctx, "writer-a", p, 2, BedUnavailable, "Устаревшая форма"); !errors.Is(err, ErrConflict) {
		t.Fatal("stale status overwrote designation edit")
	}
	if _, err := f.s.Save(f.ctx, "writer-a", p, Bed, Draft{Number: "lost", Basis: "stale"}, false, 2); !errors.Is(err, ErrConflict) {
		t.Fatal("stale designation overwrote status")
	}
}

func TestManualBedTransitionMatrix(t *testing.T) {
	states := []BedStatus{BedFree, BedReserved, BedOccupied, BedMaintenance, BedUnavailable}
	allowed := map[[2]BedStatus]bool{{BedFree, BedMaintenance}: true, {BedFree, BedUnavailable}: true, {BedMaintenance, BedFree}: true, {BedUnavailable, BedFree}: true}
	for _, from := range states {
		for _, to := range append(states, BedStatus("forged")) {
			t.Run(string(from)+"/"+string(to), func(t *testing.T) {
				f := setupBed(t)
				room, _ := hierarchy(t, f)
				p, n := bed(t, f, room, "1")
				// Seed future process states directly in this test only; there is no manual API for them.
				if _, err := f.db.ExecContext(f.ctx, "UPDATE inventory_beds SET status = ? WHERE id = ?", from, n.ID); err != nil {
					t.Fatal(err)
				}
				_, err := f.s.ChangeBedStatus(f.ctx, "writer-a", p, 1, to, "Акт")
				valid := allowed[[2]BedStatus{from, to}]
				if valid && err != nil {
					t.Fatal(err)
				}
				if !valid {
					var validation ValidationError
					if !errors.As(err, &validation) || validation["status"] == "" {
						t.Fatalf("forbidden transition returned %v", err)
					}
				}
				current, err := f.s.Detail(f.ctx, "writer-a", p)
				if err != nil {
					t.Fatal(err)
				}
				want, version := from, int64(1)
				total := 1
				if valid {
					want, version, total = to, 2, 2
				}
				h, a := counts(t, f)
				if current.Node.Status != want || current.Node.Version != version || h != total || a != total {
					t.Fatal("invalid transition mutated state or valid transition incomplete")
				}
			})
		}
	}
}

func TestBedChangeRequiresBasisAndRollsBackEveryWrite(t *testing.T) {
	for _, failure := range []string{"basis", "history", "audit", "canceled"} {
		t.Run(failure, func(t *testing.T) {
			f := setupBed(t)
			room, _ := hierarchy(t, f)
			p, n := bed(t, f, room, "1")
			ctx, basis := f.ctx, "Основание"
			switch failure {
			case "basis":
				basis = " \n\t "
			case "history":
				if _, err := f.db.ExecContext(f.ctx, `CREATE TRIGGER reject_history BEFORE INSERT ON inventory_bed_history BEGIN SELECT RAISE(ABORT, 'history failed'); END`); err != nil {
					t.Fatal(err)
				}
			case "audit":
				if _, err := f.db.ExecContext(f.ctx, `CREATE TRIGGER reject_bed_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'audit failed'); END`); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if _, err := f.s.ChangeBedStatus(ctx, "writer-a", p, n.Version, BedMaintenance, basis); err == nil {
				t.Fatal("failed operation accepted")
			}
			if failure != "canceled" {
				if _, err := f.s.Save(ctx, "writer-a", p, Bed, Draft{Number: "changed", Basis: basis}, false, 1); err == nil {
					t.Fatal("failed designation edit committed")
				}
				if _, err := f.s.ArchiveWithBasis(ctx, "writer-a", p, 1, true, basis); err == nil {
					t.Fatal("failed archive committed")
				}
				if _, err := f.s.Save(ctx, "writer-a", room, Bed, Draft{Number: "new", Basis: basis}, true, 0); err == nil {
					t.Fatal("failed creation committed")
				}
			}
			detail, err := f.s.Detail(f.ctx, "writer-a", p)
			if err != nil {
				t.Fatal(err)
			}
			h, a := counts(t, f)
			var beds int
			if err := f.db.QueryRowContext(f.ctx, "SELECT count(*) FROM inventory_beds").Scan(&beds); err != nil {
				t.Fatal(err)
			}
			if detail.Node.Status != BedFree || detail.Node.Number != "1" || detail.Node.Archived || detail.Node.Version != 1 || h != 1 || a != 1 || beds != 1 {
				t.Fatal("failure left partial change")
			}
		})
	}
}

func TestBedConcurrentStatusConflict(t *testing.T) {
	f := setupBed(t)
	room, _ := hierarchy(t, f)
	p, _ := bed(t, f, room, "1")
	// Exercise the immediate transaction contract on independent physical connections.
	f.db.SetMaxOpenConns(4)
	type outcome struct {
		next BedStatus
		err  error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	for _, next := range []BedStatus{BedMaintenance, BedUnavailable} {
		go func(next BedStatus) {
			<-start
			_, err := f.s.ChangeBedStatus(f.ctx, "writer-a", p, 1, next, string(next))
			results <- outcome{next, err}
		}(next)
	}
	close(start)
	var winner BedStatus
	conflicts := 0
	for range 2 {
		r := <-results
		if r.err == nil {
			if winner != "" {
				t.Fatal("both conflicting changes succeeded")
			}
			winner = r.next
		} else if errors.Is(r.err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(r.err)
		}
	}
	if winner == "" || conflicts != 1 {
		t.Fatal("expected one winner and one version conflict")
	}
	d, err := f.s.Detail(f.ctx, "writer-a", p)
	if err != nil {
		t.Fatal(err)
	}
	h, a := counts(t, f)
	if d.Node.Version != 2 || d.Node.Status != winner || h != 2 || a != 2 || d.History[0].Basis != string(winner) {
		t.Fatal("loser left data or winner lost")
	}
}

func TestBedCapacityArchiveRestoreAndUniqueDesignation(t *testing.T) {
	f := setupBed(t)
	room, nodes := hierarchy(t, f)
	p, n := bed(t, f, room, "1")
	bed(t, f, room, "2")
	if _, err := f.s.ChangeBedStatus(f.ctx, "writer-a", p, 1, BedMaintenance, "Ремонт"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Save(f.ctx, "writer-a", room, Bed, Draft{Number: "3", Basis: "Ввод"}, true, 0); !errors.Is(err, ErrCapacity) {
		t.Fatal("maintenance bed excluded from capacity")
	}
	draft := nodes[3].Draft()
	draft.Capacity = "1"
	if _, err := f.s.Save(f.ctx, "writer-a", room, Room, draft, false, 1); err == nil {
		t.Fatal("room shrunk below active beds")
	}
	if _, err := f.db.ExecContext(f.ctx, "UPDATE inventory_rooms SET capacity = 1 WHERE id = ?", room.RoomID); err == nil {
		t.Fatal("DB allows capacity below active beds")
	}
	if _, err := f.db.ExecContext(f.ctx, "INSERT INTO inventory_beds (id,room_id,number) VALUES ('overflow',?,'3')", room.RoomID); err == nil {
		t.Fatal("DB allows excess bed")
	}
	if _, err := f.s.Archive(f.ctx, "writer-a", room, 1, true); !errors.Is(err, ErrActiveChildren) {
		t.Fatal("room archived with active beds")
	}
	archived, err := f.s.ArchiveWithBasis(f.ctx, "writer-a", p, 2, true, "Вывод из фонда")
	if err != nil {
		t.Fatal(err)
	}
	if archived.ID != n.ID || archived.Status != BedMaintenance {
		t.Fatal("archive changed identity or status")
	}
	if _, err := f.s.Save(f.ctx, "writer-a", room, Bed, Draft{Number: " 1 ", Basis: "Дубль"}, true, 0); err == nil {
		t.Fatal("duplicate archived designation accepted")
	}
	bed(t, f, room, "3")
	if _, err := f.s.ArchiveWithBasis(f.ctx, "writer-a", p, 3, false, "Возврат"); !errors.Is(err, ErrCapacity) {
		t.Fatal("restore exceeds capacity")
	}
	if _, err := f.db.ExecContext(f.ctx, "UPDATE inventory_beds SET archived = 0 WHERE id = ?", p.BedID); err == nil {
		t.Fatal("DB restore exceeds capacity")
	}
	// Same designation in a different room is valid.
	otherDraft := roomDraft()
	otherDraft.Number = "102"
	other := create(t, f, room.Parent(), Room, otherDraft)
	otherRoom := room
	otherRoom.RoomID = other.ID
	bed(t, f, otherRoom, "1")
	draft.Capacity = "3"
	if _, err := f.s.Save(f.ctx, "writer-a", room, Room, draft, false, 1); err != nil {
		t.Fatal(err)
	}
	restored, err := f.s.ArchiveWithBasis(f.ctx, "writer-a", p, 3, false, "Возврат")
	if err != nil {
		t.Fatal(err)
	}
	if restored.Version != 4 || restored.Status != BedMaintenance || restored.Archived {
		t.Fatal("bad restore")
	}
	draft.Capacity = "2"
	if _, err := f.s.Save(f.ctx, "writer-a", room, Room, draft, false, 2); err == nil {
		t.Fatal("restore not counted in capacity")
	}
}

func TestConcurrentBedCreationRespectsCapacity(t *testing.T) {
	f := setupBed(t)
	room, nodes := hierarchy(t, f)
	d := nodes[3].Draft()
	d.Capacity = "1"
	if _, err := f.s.Save(f.ctx, "writer-a", room, Room, d, false, 1); err != nil {
		t.Fatal(err)
	}
	f.db.SetMaxOpenConns(4)
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := range 2 {
		go func(i int) {
			<-start
			_, err := f.s.Save(f.ctx, "writer-a", room, Bed, Draft{Number: fmt.Sprint(i), Basis: "Ввод"}, true, 0)
			results <- err
		}(i)
	}
	close(start)
	success, full := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			success++
		} else if errors.Is(err, ErrCapacity) {
			full++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || full != 1 {
		t.Fatal("concurrent capacity overflow")
	}
	h, a := counts(t, f)
	if h != 1 || a != 1 {
		t.Fatal("failed creation left history or audit")
	}
}

type rejectBedTx struct{ Authorizer }

func (a rejectBedTx) AuthorizeTx(context.Context, *sql.Tx, string, string, string) error {
	return auth.ErrForbidden
}

func TestBedScopeAndTransactionalPermissions(t *testing.T) {
	f := setupBed(t)
	room, _ := hierarchy(t, f)
	p, _ := bed(t, f, room, "1")
	for _, actor := range []string{"writer-b", "sys"} {
		if _, err := f.s.ChangeBedStatus(f.ctx, actor, p, 1, BedMaintenance, "Акт"); !errors.Is(err, auth.ErrForbidden) {
			t.Fatal("unauthorized status change")
		}
		if _, err := f.s.Detail(f.ctx, actor, p); !errors.Is(err, auth.ErrForbidden) {
			t.Fatal("history exposed")
		}
	}
	spoof := p
	spoof.RoomID = "missing"
	if _, err := f.s.ChangeBedStatus(f.ctx, "writer-a", spoof, 1, BedMaintenance, "Акт"); !errors.Is(err, ErrNotFound) {
		t.Fatal("forged parent chain accepted")
	}
	f.s.permissions = rejectBedTx{f.s.permissions}
	if _, err := f.s.ChangeBedStatus(f.ctx, "writer-a", p, 1, BedMaintenance, "Акт"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("missing transactional permission check")
	}
	if _, err := f.s.Save(f.ctx, "writer-a", room, Bed, Draft{Number: "2", Basis: "Акт"}, true, 0); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("missing transactional create permission")
	}
	if _, err := f.s.ArchiveWithBasis(f.ctx, "writer-a", p, 1, true, "Акт"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("missing transactional archive permission")
	}
	h, a := counts(t, f)
	if h != 1 || a != 1 {
		t.Fatal("denied change left data")
	}
}

func TestConcurrentRoomShrinkAndBedCreation(t *testing.T) {
	f := setupBed(t)
	room, nodes := hierarchy(t, f)
	bed(t, f, room, "1")
	f.db.SetMaxOpenConns(4)
	d := nodes[3].Draft()
	d.Capacity = "1"
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() { <-start; _, err := f.s.Save(f.ctx, "writer-a", room, Room, d, false, 1); results <- err }()
	go func() {
		<-start
		_, err := f.s.Save(f.ctx, "writer-a", room, Bed, Draft{Number: "2", Basis: "Ввод"}, true, 0)
		results <- err
	}()
	close(start)
	successes := 0
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else {
			var fields ValidationError
			if !errors.Is(err, ErrCapacity) && (!errors.As(err, &fields) || fields["capacity"] == "") {
				t.Fatal(err)
			}
		}
	}
	if successes != 1 {
		t.Fatal("expected exactly one compatible change")
	}
	var capacity, count int64
	if err := f.db.QueryRowContext(f.ctx, `SELECT capacity, (SELECT count(*) FROM inventory_beds WHERE room_id = r.id AND archived = 0) FROM inventory_rooms r WHERE id = ?`, room.RoomID).Scan(&capacity, &count); err != nil {
		t.Fatal(err)
	}
	if count > capacity || (capacity != 1 && capacity != 2) {
		t.Fatal("race broke capacity invariant")
	}
}

func TestArchivedBedAndParentGuards(t *testing.T) {
	f := setupBed(t)
	room, _ := hierarchy(t, f)
	p, _ := bed(t, f, room, "1")
	if _, err := f.s.ArchiveWithBasis(f.ctx, "writer-a", p, 1, true, "Вывод"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ChangeBedStatus(f.ctx, "writer-a", p, 2, BedMaintenance, "Акт"); !errors.Is(err, ErrArchived) {
		t.Fatal("archived bed status changed")
	}
	if _, err := f.s.Archive(f.ctx, "writer-a", room, 1, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ArchiveWithBasis(f.ctx, "writer-a", p, 2, false, "Ввод"); !errors.Is(err, ErrArchived) {
		t.Fatal("bed restored below archived room")
	}
	if _, err := f.s.Save(f.ctx, "writer-a", room, Bed, Draft{Number: "2", Basis: "Ввод"}, true, 0); !errors.Is(err, ErrArchived) {
		t.Fatal("bed created below archived room")
	}
	if _, err := f.s.Archive(f.ctx, "writer-a", room, 2, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecContext(f.ctx, `CREATE TRIGGER reject_restore_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'audit failed'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ArchiveWithBasis(f.ctx, "writer-a", p, 2, false, "Ввод"); err == nil {
		t.Fatal("unaudited restore committed")
	}
	d, err := f.s.Detail(f.ctx, "writer-a", p)
	if err != nil {
		t.Fatal(err)
	}
	h, a := counts(t, f)
	if !d.Node.Archived || d.Node.Version != 2 || h != 2 || a != 2 {
		t.Fatal("failed restore left data")
	}
}

func setupBed(t *testing.T) *fixture {
	t.Helper()
	f := setup(t)
	if _, err := f.db.ExecContext(f.ctx, "INSERT INTO auth_user_roles (user_id, role_id) VALUES ('writer-a','commandant')"); err != nil {
		t.Fatal(err)
	}
	return f
}
