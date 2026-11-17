package applications

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"student_dormitory/internal/modules/audit"
	"student_dormitory/internal/modules/auth"
	"student_dormitory/internal/platform/database"
	"student_dormitory/internal/platform/requestid"
)

func fixture(t *testing.T) (*Service, *sql.DB, context.Context) {
	t.Helper()
	ctx := requestid.WithID(context.Background(), "applications-test")
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, id := range []string{"one", "two", "admin"} {
		if _, err := db.ExecContext(ctx, "INSERT INTO auth_users(id,display_name) VALUES (?,?)", id, id); err != nil {
			t.Fatal(err)
		}
		role := "student"
		if id == "admin" {
			role = "system_admin"
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO auth_user_roles VALUES (?,?)", id, role); err != nil {
			t.Fatal(err)
		}
	}
	a := audit.NewRecorder(db)
	identity := auth.NewService(auth.NewRepository(db), a, auth.Options{})
	return NewService(NewRepository(db), identity, a, identity), db, ctx
}
func valid() Input {
	return Input{"Студент <script>alert(1)</script>", "student@example.test", "Группа 1", "Прошу предоставить общежитие"}
}
func ready(t *testing.T, s *Service, ctx context.Context) Application {
	t.Helper()
	a, err := s.Create(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Edit(ctx, "one", a.ID, valid(), a.Version); err != nil {
		t.Fatal(err)
	}
	a, _, err = s.Detail(ctx, "one", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestProfileSnapshotSubmissionAndWithdrawal(t *testing.T) {
	s, db, ctx := fixture(t)
	if err := s.SaveProfile(ctx, "one", valid(), 0); err != nil {
		t.Fatal(err)
	}
	p, err := s.Profile(ctx, "one")
	if err != nil || p.Version != 1 {
		t.Fatal(p, err)
	}
	for _, f := range []Field{p.Fields.Name, p.Fields.Email, p.Fields.Group} {
		if f.Source != StudentSource || f.EnteredAt == "" {
			t.Fatal("missing provenance")
		}
	}
	a, err := s.Create(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	changed := valid()
	changed.Name = "Другое имя"
	if err := s.SaveProfile(ctx, "one", changed, p.Version); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProfile(ctx, "one", valid(), p.Version); !errors.Is(err, ErrConflict) {
		t.Fatal("stale profile accepted", err)
	}
	a, _, _ = s.Detail(ctx, "one", a.ID)
	if a.Fields.Name != p.Fields.Name {
		t.Fatal("snapshot changed")
	}
	if err := s.Edit(ctx, "one", a.ID, valid(), a.Version); err != nil {
		t.Fatal(err)
	}
	a, _, _ = s.Detail(ctx, "one", a.ID)
	if a.Fields.Name != p.Fields.Name {
		t.Fatal("unchanged field date lost")
	}
	if _, err := s.Submit(ctx, "one", a.ID, a.SubmitKey, "test-v0", true, a.Version); !errors.Is(err, ErrValidation) {
		t.Fatal(err)
	}
	if _, err := s.Submit(ctx, "one", a.ID, a.SubmitKey, ConsentVersion, false, a.Version); !errors.Is(err, ErrValidation) {
		t.Fatal(err)
	}
	saved, err := s.Submit(ctx, "one", a.ID, a.SubmitKey, ConsentVersion, true, a.Version)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Registration < 1 || saved.SubmittedAt == "" || saved.ConsentAcceptedAt != saved.SubmittedAt || saved.Status != Submitted {
		t.Fatal(saved)
	}
	replay, err := s.Submit(ctx, "one", a.ID, a.SubmitKey, ConsentVersion, true, a.Version)
	if err != nil || replay.Number() != saved.Number() {
		t.Fatal("replay", err)
	}
	if n := count(t, db, "SELECT count(*) FROM audit_events WHERE action='applications.submitted'"); n != 1 {
		t.Fatal("duplicate audit", n)
	}
	if err := s.Edit(ctx, "one", a.ID, valid(), saved.Version); !errors.Is(err, ErrConflict) {
		t.Fatal("submitted editable", err)
	}
	if err := s.Withdraw(ctx, "one", a.ID, a.Version); !errors.Is(err, ErrConflict) {
		t.Fatal("stale withdrawal", err)
	}
	if err := s.Withdraw(ctx, "one", a.ID, saved.Version); err != nil {
		t.Fatal(err)
	}
	withdrawn, h, err := s.Detail(ctx, "one", a.ID)
	if err != nil || withdrawn.Status != Withdrawn || len(h) != 4 || withdrawn.Number() != saved.Number() || withdrawn.SubmittedAt != saved.SubmittedAt {
		t.Fatal(withdrawn, h, err)
	}
	if err := s.Withdraw(ctx, "one", a.ID, withdrawn.Version); !errors.Is(err, ErrConflict) {
		t.Fatal("repeat withdrawal", err)
	}
	if _, err := s.Submit(ctx, "one", a.ID, a.SubmitKey, ConsentVersion, true, withdrawn.Version); !errors.Is(err, ErrConflict) {
		t.Fatal("resubmission", err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE application_consents SET body='changed'"); err == nil {
		t.Fatal("consent mutable")
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM application_history"); err == nil {
		t.Fatal("history mutable")
	}
	var payload string
	if err := db.QueryRowContext(ctx, "SELECT group_concat(changes_json) FROM audit_events").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, valid().Email) || strings.Contains(payload, valid().Name) {
		t.Fatal("personal data in audit")
	}
}
func TestOwnershipPermissionsAndStates(t *testing.T) {
	s, db, ctx := fixture(t)
	a := ready(t, s, ctx)
	if _, _, err := s.Detail(ctx, "two", a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign read", err)
	}
	if err := s.Edit(ctx, "two", a.ID, valid(), a.Version); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign edit", err)
	}
	if _, err := s.Submit(ctx, "two", a.ID, a.SubmitKey, ConsentVersion, true, a.Version); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign submit", err)
	}
	if err := s.Withdraw(ctx, "two", a.ID, a.Version); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign withdraw", err)
	}
	list, err := s.List(ctx, "two")
	if err != nil || len(list) != 0 {
		t.Fatal(list, err)
	}
	for _, actor := range []string{"admin", ""} {
		if _, _, err := s.Detail(ctx, actor, a.ID); err == nil {
			t.Fatal("access bypass")
		}
		if _, err := s.Create(ctx, actor); err == nil {
			t.Fatal("create bypass")
		}
	}
	if err := s.Withdraw(ctx, "one", a.ID, a.Version); !errors.Is(err, ErrConflict) {
		t.Fatal("draft withdrawal", err)
	}
	saved, err := s.Submit(ctx, "one", a.ID, a.SubmitKey, ConsentVersion, true, a.Version)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []Status{UnderReview, NeedsInformation, Verified, Withdrawn} {
		if _, err := db.ExecContext(ctx, "UPDATE applications SET status=? WHERE id=?", status, a.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.Withdraw(ctx, "one", a.ID, saved.Version); !errors.Is(err, ErrConflict) {
			t.Fatal(status, err)
		}
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM auth_user_roles WHERE user_id='one'"); err != nil {
		t.Fatal(err)
	}
	if err := s.Edit(ctx, "one", a.ID, valid(), saved.Version); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("revoked permission", err)
	}
}
func TestSubmissionConcurrentReplayAndNoPeriodLimit(t *testing.T) {
	s, db, ctx := fixture(t)
	a := ready(t, s, ctx)
	var wg sync.WaitGroup
	results := make(chan Application, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.Submit(ctx, "one", a.ID, a.SubmitKey, ConsentVersion, true, a.Version)
			results <- r
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for r := range results {
		if r.Registration != 1 {
			t.Fatal("duplicate number", r.Registration)
		}
	}
	b := ready(t, s, ctx)
	if _, err := s.Submit(ctx, "one", b.ID, a.SubmitKey, ConsentVersion, true, b.Version); !errors.Is(err, ErrConflict) {
		t.Fatal("foreign request key", err)
	}
	second, err := s.Submit(ctx, "one", b.ID, b.SubmitKey, ConsentVersion, true, b.Version)
	if err != nil || second.Registration != 2 {
		t.Fatal("period restricted", err)
	}
	if count(t, db, "SELECT count(*) FROM application_registrations") != 2 {
		t.Fatal("duplicate registration")
	}
}
func TestValidationAndAtomicRollback(t *testing.T) {
	s, db, ctx := fixture(t)
	a, err := s.Create(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Submit(ctx, "one", a.ID, a.SubmitKey, ConsentVersion, true, a.Version); !errors.Is(err, ErrValidation) {
		t.Fatal("incomplete submission", err)
	}
	bad := valid()
	bad.Request = strings.Repeat("а", 2001)
	if err := s.Edit(ctx, "one", a.ID, bad, a.Version); !errors.Is(err, ErrValidation) {
		t.Fatal("oversize", err)
	}
	a = ready(t, s, ctx)
	for _, table := range []string{"audit_events", "application_history", "application_registrations"} {
		if _, err := db.ExecContext(ctx, "CREATE TRIGGER reject_change BEFORE INSERT ON "+table+" BEGIN SELECT RAISE(ABORT,'unavailable'); END"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Submit(ctx, "one", a.ID, a.SubmitKey, ConsentVersion, true, a.Version); err == nil {
			t.Fatal("failed insert committed", table)
		}
		current, _, err := s.Detail(ctx, "one", a.ID)
		if err != nil || current.Status != Draft || current.Version != a.Version || current.ConsentVersion != "" || current.Registration != 0 {
			t.Fatal("partial submission", table, current, err)
		}
		if _, err := db.ExecContext(ctx, "DROP TRIGGER reject_change"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, "CREATE TRIGGER reject_change BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT,'unavailable'); END"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProfile(ctx, "one", valid(), 0); err == nil {
		t.Fatal("profile without audit")
	}
	p, _ := s.Profile(ctx, "one")
	if p.Version != 0 {
		t.Fatal("profile not rolled back")
	}
	if err := s.Edit(ctx, "one", a.ID, Input{Name: "changed"}, a.Version); err == nil {
		t.Fatal("draft without audit")
	}
	if _, err := s.Create(ctx, "one"); err == nil {
		t.Fatal("creation without audit")
	}
	if _, err := db.ExecContext(ctx, "DROP TRIGGER reject_change"); err != nil {
		t.Fatal(err)
	}
	saved, err := s.Submit(ctx, "one", a.ID, a.SubmitKey, ConsentVersion, true, a.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "CREATE TRIGGER reject_change BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT,'unavailable'); END"); err != nil {
		t.Fatal(err)
	}
	if err := s.Withdraw(ctx, "one", a.ID, saved.Version); err == nil {
		t.Fatal("withdrawal without audit")
	}
	current, _, _ := s.Detail(ctx, "one", a.ID)
	if current.Status != Submitted {
		t.Fatal("withdrawal not rolled back")
	}
}

type revokedInTransaction struct {
	Permissions
	checked bool
}

func (p *revokedInTransaction) AuthorizeTx(context.Context, *sql.Tx, string, string, string) error {
	p.checked = true
	return auth.ErrForbidden
}
func TestMutationRechecksPermissionInTransaction(t *testing.T) {
	s, db, ctx := fixture(t)
	a := ready(t, s, ctx)
	p := &revokedInTransaction{Permissions: s.permissions}
	s.permissions = p
	if _, err := s.Submit(ctx, "one", a.ID, a.SubmitKey, ConsentVersion, true, a.Version); !errors.Is(err, auth.ErrForbidden) || !p.checked {
		t.Fatal("transaction grant not checked", err)
	}
	if count(t, db, "SELECT count(*) FROM application_registrations") != 0 {
		t.Fatal("write despite revoked grant")
	}
}
