package auth

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"student_dormitory/internal/modules/audit"
	"student_dormitory/internal/platform/database"
	"student_dormitory/internal/platform/requestid"
)

func roleFixture(t *testing.T) (*Service, *sql.DB, context.Context) {
	t.Helper()
	ctx := requestid.WithID(context.Background(), "role-test-request")
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "roles.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, role := range []string{"system_admin", "student", "registry_staff", "commandant", "administration"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO auth_users(id, display_name) VALUES (?, ?)`, role, role); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO auth_user_roles(user_id, role_id) VALUES (?, ?)`, role, role); err != nil {
			t.Fatal(err)
		}
	}
	return NewService(NewRepository(db), audit.NewRecorder(db), Options{}), db, ctx
}

func roleCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestChangeRolePermissionsNotificationsAndAudit(t *testing.T) {
	s, db, ctx := roleFixture(t)
	if _, err := db.ExecContext(ctx, `INSERT INTO auth_user_dormitories(user_id, dormitory_id) VALUES ('student', 'assigned-dorm')`); err != nil {
		t.Fatal(err)
	}
	for _, actor := range []string{"", "student", "registry_staff", "commandant", "administration", "missing"} {
		err := s.ChangeRole(ctx, actor, "student", RoleChange{"system_admin", 1})
		want := ErrForbidden
		if actor == "" {
			want = ErrUnauthenticated
		}
		if !errors.Is(err, want) {
			t.Fatalf("actor %q: %v", actor, err)
		}
	}
	for _, change := range []struct {
		target string
		input  RoleChange
		want   error
	}{
		{"missing", RoleChange{"student", 1}, ErrUserNotFound},
		{"student' OR 1=1--", RoleChange{"student", 1}, ErrUserNotFound},
		{"student", RoleChange{"unknown", 1}, ErrRoleValidation},
		{"student", RoleChange{"student' OR 1=1--", 1}, ErrRoleValidation},
		{"student", RoleChange{"", 1}, ErrRoleValidation},
		{"student", RoleChange{"commandant", 0}, ErrRoleConflict},
	} {
		if err := s.ChangeRole(ctx, "system_admin", change.target, change.input); !errors.Is(err, change.want) {
			t.Fatalf("%+v: %v", change, err)
		}
	}
	// Saving an unchanged role creates neither an event nor a notification.
	if err := s.ChangeRole(ctx, "system_admin", "student", RoleChange{"student", 1}); err != nil {
		t.Fatal(err)
	}
	if roleCount(t, db, `SELECT count(*) FROM audit_events`) != 0 {
		t.Fatal("no-op audited")
	}
	if err := s.ChangeRole(ctx, "system_admin", "student", RoleChange{"commandant", 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.Authorize(ctx, "student", "applications.own", ""); !errors.Is(err, ErrForbidden) {
		t.Fatal("old role still grants access", err)
	}
	if err := s.Authorize(ctx, "student", "places.status.write", "assigned-dorm"); err != nil {
		t.Fatal("new permission not effective", err)
	}
	if err := s.Authorize(ctx, "student", "places.status.write", "other-dorm"); !errors.Is(err, ErrForbidden) {
		t.Fatal("scope bypass", err)
	}
	notes, err := s.RoleNotifications(ctx, "student")
	if err != nil || len(notes) != 1 || notes[0].RoleName != "Комендант" || notes[0].CreatedAt == "" || notes[0].ReadAt != "" {
		t.Fatal("notification missing", notes, err)
	}
	var actor, target, diff, requestID string
	if err := db.QueryRowContext(ctx, `SELECT actor_id,object_id,changes_json,request_id FROM audit_events WHERE action='auth.user_role_changed'`).Scan(&actor, &target, &diff, &requestID); err != nil {
		t.Fatal(err)
	}
	if actor != "system_admin" || target != "student" || requestID != "role-test-request" || diff != `{"before_roles":["student"],"after_roles":["commandant"]}` {
		t.Fatal("wrong audit", actor, target, diff, requestID)
	}
	if err := s.ChangeRole(ctx, "system_admin", "student", RoleChange{"registry_staff", 1}); !errors.Is(err, ErrRoleConflict) {
		t.Fatal("stale form accepted", err)
	}
	if err := s.ChangeRole(ctx, "system_admin", "student", RoleChange{"commandant", 2}); err != nil {
		t.Fatal(err)
	}
	if roleCount(t, db, `SELECT count(*) FROM auth_role_notifications`) != 1 {
		t.Fatal("duplicate notification")
	}
	if err := s.ReadRoleNotification(ctx, "commandant", notes[0].ID); !errors.Is(err, ErrUserNotFound) {
		t.Fatal("read another user's notification", err)
	}
	if others, err := s.RoleNotifications(ctx, "commandant"); err != nil || len(others) != 0 {
		t.Fatal("notification privacy", others, err)
	}
	for range 2 {
		if err := s.ReadRoleNotification(ctx, "student", notes[0].ID); err != nil {
			t.Fatal(err)
		}
	}
	if roleCount(t, db, `SELECT count(*) FROM audit_events WHERE action='auth.role_notification_read'`) != 1 {
		t.Fatal("duplicate read audit")
	}
	notes, _ = s.RoleNotifications(ctx, "student")
	if notes[0].ReadAt == "" {
		t.Fatal("read flag missing")
	}
	// Repeated changes to a previously held role are distinct events.
	if err := s.ChangeRole(ctx, "system_admin", "student", RoleChange{"student", 2}); err != nil {
		t.Fatal(err)
	}
	if roleCount(t, db, `SELECT count(*) FROM auth_role_notifications`) != 2 {
		t.Fatal("later change lost")
	}
}

func TestChangeRoleLastAdminAndMultipleRoles(t *testing.T) {
	s, db, ctx := roleFixture(t)
	if _, err := db.ExecContext(ctx, `INSERT INTO auth_user_roles(user_id, role_id) VALUES ('registry_staff', 'system_admin')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE auth_users SET enabled=0 WHERE id='registry_staff'`); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeRole(ctx, "system_admin", "system_admin", RoleChange{"student", 1}); !errors.Is(err, ErrLastAdmin) {
		t.Fatal("removed last active administrator", err)
	}
	if err := s.ChangeRole(ctx, "registry_staff", "student", RoleChange{"system_admin", 1}); !errors.Is(err, ErrForbidden) {
		t.Fatal("disabled actor accepted", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE auth_users SET enabled=1 WHERE id='registry_staff'`); err != nil {
		t.Fatal(err)
	}
	page, err := s.SearchUsers(ctx, "system_admin", "registry_staff")
	if err != nil || len(page.Users) != 1 || len(page.Users[0].Roles) != 2 || page.Users[0].SelectedRoleID() != "" || !page.CanManageRoles || len(page.Roles) != 5 {
		t.Fatal("multi-role directory", page, err)
	}
	if err := s.ChangeRole(ctx, "system_admin", "system_admin", RoleChange{"student", 1}); err != nil {
		t.Fatal("second admin did not allow self-change", err)
	}
	if err := s.ChangeRole(ctx, "system_admin", "student", RoleChange{"system_admin", 1}); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked permission still works", err)
	}
	if err := s.ChangeRole(ctx, "registry_staff", "registry_staff", RoleChange{"student", 1}); !errors.Is(err, ErrLastAdmin) {
		t.Fatal("multi-role last admin bypass", err)
	}
	if err := s.ChangeRole(ctx, "registry_staff", "student", RoleChange{"system_admin", 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeRole(ctx, "student", "registry_staff", RoleChange{"commandant", 1}); err != nil {
		t.Fatal(err)
	}
	user, err := s.repo.managedUser(ctx, "registry_staff")
	if err != nil || len(user.Roles) != 1 || user.SelectedRoleID() != "commandant" {
		t.Fatal("did not replace all previous roles", user, err)
	}
	// Holding system_admin alone never grants scoped business access.
	if err := s.Authorize(ctx, "student", "registry.read", "assigned-dorm"); !errors.Is(err, ErrForbidden) {
		t.Fatal("system admin bypass", err)
	}
}

func TestRoleChangeRollsBackOnNotificationOrAuditFailure(t *testing.T) {
	for _, table := range []string{"auth_role_notifications", "audit_events"} {
		t.Run(table, func(t *testing.T) {
			s, db, ctx := roleFixture(t)
			if _, err := db.ExecContext(ctx, `CREATE TRIGGER reject_change BEFORE INSERT ON `+table+` BEGIN SELECT RAISE(ABORT, 'test failure'); END`); err != nil {
				t.Fatal(err)
			}
			if err := s.ChangeRole(ctx, "system_admin", "student", RoleChange{"commandant", 1}); err == nil {
				t.Fatal("failure ignored")
			}
			user, err := s.repo.managedUser(ctx, "student")
			if err != nil || user.SelectedRoleID() != "student" || user.RoleVersion != 1 {
				t.Fatal("role changed after failure", user, err)
			}
			for _, table := range []string{"auth_role_notifications", "audit_events"} {
				if roleCount(t, db, `SELECT count(*) FROM `+table) != 0 {
					t.Fatal("partial write to", table)
				}
			}
		})
	}
}

func TestRoleNotificationReadRollsBackOnAuditFailure(t *testing.T) {
	s, db, ctx := roleFixture(t)
	if err := s.ChangeRole(ctx, "system_admin", "student", RoleChange{"commandant", 1}); err != nil {
		t.Fatal(err)
	}
	notes, _ := s.RoleNotifications(ctx, "student")
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER reject_read BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.ReadRoleNotification(ctx, "student", notes[0].ID); err == nil {
		t.Fatal("audit failure ignored")
	}
	notes, _ = s.RoleNotifications(ctx, "student")
	if notes[0].ReadAt != "" {
		t.Fatal("read flag not rolled back")
	}
}

func TestConcurrentRoleChangesUseExpectedVersion(t *testing.T) {
	s, db, ctx := roleFixture(t)
	results := make(chan error, 2)
	for _, role := range []string{"commandant", "registry_staff"} {
		go func() { results <- s.ChangeRole(ctx, "system_admin", "student", RoleChange{role, 1}) }()
	}
	success, conflict := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			success++
		} else if errors.Is(err, ErrRoleConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 || roleCount(t, db, `SELECT count(*) FROM auth_role_notifications`) != 1 {
		t.Fatal("concurrent overwrite", success, conflict)
	}
	page, err := s.SearchUsers(ctx, "system_admin", "student")
	if err != nil || len(page.Users) != 1 || page.Users[0].RoleVersion != 2 || strings.Contains(page.Users[0].SelectedRoleID(), "student") {
		t.Fatal("wrong final directory", page, err)
	}
}
