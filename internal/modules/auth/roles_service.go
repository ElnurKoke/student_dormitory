package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"student_dormitory/internal/modules/audit"
)

var errRoleUnchanged = errors.New("role state unchanged")

// ChangeRole treats the submitted role as an administrator command, never as
// the caller's identity. Authorization and all writes use the audit transaction.
func (s *Service) ChangeRole(ctx context.Context, actorID, userID string, change RoleChange) error {
	err := s.audit.RecordChange(ctx, func(ctx context.Context, tx *sql.Tx) (audit.Event, error) {
		if err := s.AuthorizeTx(ctx, tx, actorID, "auth.users.roles.write", ""); err != nil {
			return audit.Event{}, err
		}
		if change.Version < 1 {
			return audit.Event{}, ErrRoleConflict
		}
		repo := s.repo.WithTx(tx)
		user, err := repo.managedUser(ctx, userID)
		if err != nil {
			return audit.Event{}, err
		}
		if user.RoleVersion != change.Version {
			return audit.Event{}, ErrRoleConflict
		}
		roles, err := repo.roles(ctx)
		if err != nil {
			return audit.Event{}, err
		}
		var selected Role
		for _, role := range roles {
			if role.ID == change.RoleID {
				selected = role
				break
			}
		}
		if selected.ID == "" {
			return audit.Event{}, ErrRoleValidation
		}
		if user.SelectedRoleID() == selected.ID {
			return audit.Event{}, errRoleUnchanged
		}
		if selected.ID != "system_admin" {
			last, err := repo.lastAdmin(ctx, userID)
			if err != nil {
				return audit.Event{}, err
			}
			if last {
				return audit.Event{}, ErrLastAdmin
			}
		}
		if err := repo.replaceRole(ctx, userID, change, selected, s.now().UTC().Format(time.RFC3339Nano)); err != nil {
			return audit.Event{}, err
		}
		before := make([]string, 0, len(user.Roles))
		for _, role := range user.Roles {
			before = append(before, role.ID)
		}
		diff, err := json.Marshal(struct {
			Before []string `json:"before_roles"`
			After  []string `json:"after_roles"`
		}{before, []string{selected.ID}})
		if err != nil {
			return audit.Event{}, err
		}
		return audit.Event{ActorID: actorID, Action: "auth.user_role_changed", ObjectType: "auth_user", ObjectID: userID, Result: "success", ChangesJSON: string(diff)}, nil
	})
	if errors.Is(err, errRoleUnchanged) {
		return nil
	}
	return err
}

func (s *Service) RoleNotifications(ctx context.Context, userID string) ([]RoleNotification, error) {
	if err := s.Authorize(ctx, userID, "account.read", ""); err != nil {
		return nil, err
	}
	return s.repo.roleNotifications(ctx, userID)
}

func (s *Service) ReadRoleNotification(ctx context.Context, userID string, id int64) error {
	err := s.audit.RecordChange(ctx, func(ctx context.Context, tx *sql.Tx) (audit.Event, error) {
		if err := s.AuthorizeTx(ctx, tx, userID, "account.read", ""); err != nil {
			return audit.Event{}, err
		}
		if err := s.repo.WithTx(tx).readRoleNotification(ctx, userID, id, s.now().UTC().Format(time.RFC3339Nano)); err != nil {
			return audit.Event{}, err
		}
		return audit.Event{ActorID: userID, Action: "auth.role_notification_read", ObjectType: "auth_role_notification", ObjectID: strconv.FormatInt(id, 10), Result: "success"}, nil
	})
	if errors.Is(err, errRoleUnchanged) {
		return nil
	}
	return err
}
