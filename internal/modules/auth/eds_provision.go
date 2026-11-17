package auth

import (
	"context"
	"database/sql"
	"errors"

	"student_dormitory/internal/modules/audit"
	"student_dormitory/internal/platform/nca"
)

var ErrIdentityConflict = errors.New("NCA identity is already linked")
var ErrAdminExists = errors.New("an active system administrator already exists")
var errIdentityUnchanged = errors.New("NCA identity unchanged")

// ProvisionNCAIdentity is an offline operator command, never an HTTP registration
// endpoint. It links an existing local account or creates the first administrator.
// A certificate must still be verified by NCANode before any login is possible.
func (s *Service) ProvisionNCAIdentity(ctx context.Context, iin, userID string, bootstrapAdmin bool) error {
	if !nca.ValidIIN(iin) || (bootstrapAdmin == (userID != "")) {
		return ErrCredentials
	}
	err := s.audit.RecordChange(ctx, func(ctx context.Context, tx *sql.Tx) (audit.Event, error) {
		repo := s.repo.WithTx(tx)
		linked, _, err := repo.ncaUser(ctx, iin)
		if err == nil {
			if !bootstrapAdmin && linked.ID == userID {
				return audit.Event{}, errIdentityUnchanged
			}
			return audit.Event{}, ErrIdentityConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return audit.Event{}, err
		}
		action := "auth.nca_identity_linked"
		if bootstrapAdmin {
			exists, err := repo.anyActiveAdmin(ctx)
			if err != nil {
				return audit.Event{}, err
			}
			if exists {
				return audit.Event{}, ErrAdminExists
			}
			userID = randomToken()
			if err := repo.createNCAUser(ctx, User{ID: userID, DisplayName: "Системный администратор"}, iin, "system_admin"); err != nil {
				return audit.Event{}, err
			}
			action = "auth.nca_admin_bootstrapped"
		} else {
			if _, err := repo.managedUser(ctx, userID); err != nil {
				return audit.Event{}, err
			}
			exists, err := repo.hasNCAIdentity(ctx, userID)
			if err != nil {
				return audit.Event{}, err
			}
			if exists {
				return audit.Event{}, ErrIdentityConflict
			}
			if err := repo.bindNCAIdentity(ctx, userID, iin); err != nil {
				return audit.Event{}, err
			}
		}
		return audit.Event{ActorID: "system:eds-account", Action: action, ObjectType: "auth_user", ObjectID: userID, Result: "success"}, nil
	})
	if errors.Is(err, errIdentityUnchanged) {
		return nil
	}
	return err
}
