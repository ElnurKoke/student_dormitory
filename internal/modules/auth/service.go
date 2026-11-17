package auth

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"student_dormitory/internal/modules/audit"
)

type Service struct {
	repo    *Repository
	audit   *audit.Recorder
	options Options
	now     func() time.Time
}

func NewService(repo *Repository, recorder *audit.Recorder, options Options) *Service {
	return &Service{repo: repo, audit: recorder, options: options, now: time.Now}
}

func (s *Service) Session(ctx context.Context, token string) (Session, error) {
	if len(token) != 43 {
		return Session{}, ErrUnauthenticated
	}
	session, err := s.repo.session(ctx, hashToken(token), s.now())
	if err != nil {
		return Session{}, err
	}
	if (session.Provider == "development" && !s.options.DevelopmentLogin) || (session.Provider == "nca" && !s.options.EDSLogin) {
		return Session{}, ErrUnauthenticated
	}
	return session, nil
}

func (s *Service) newSession(userID, provider string, ttl time.Duration) (Session, string) {
	token := randomToken()
	return Session{ID: randomToken(), TokenHash: hashToken(token), UserID: userID, Provider: provider,
		CSRFToken: randomToken(), ExpiresAt: s.now().Add(ttl).Truncate(time.Second)}, token
}

func (s *Service) Anonymous(ctx context.Context) (Session, string, error) {
	// This is authentication-neutral pre-login state. Only the development HTTP
	// route exposes it today; a future verified SSO flow can reuse the lifecycle.
	session, token := s.newSession("", "anonymous", 10*time.Minute)
	err := s.repo.insertSession(ctx, session, s.now())
	return session, token, err
}

func (s *Service) Login(ctx context.Context, previousToken string, verifier IdentityVerifier) (Session, string, error) {
	identity, err := verifier.Verify(ctx)
	if err != nil {
		return Session{}, "", err
	}
	if identity.Provider == "development" && !s.options.DevelopmentLogin {
		return Session{}, "", ErrUnavailable
	}
	var next Session
	var token string
	err = s.audit.RecordChange(ctx, func(ctx context.Context, tx *sql.Tx) (audit.Event, error) {
		repo := s.repo.WithTx(tx)
		previous, err := repo.session(ctx, hashToken(previousToken), s.now())
		if err != nil {
			return audit.Event{}, err
		}
		if previous.UserID != "" {
			return audit.Event{}, ErrForbidden
		}
		user, err := repo.userByIdentity(ctx, identity)
		if err != nil {
			return audit.Event{}, err
		}
		next, token = s.newSession(user.ID, identity.Provider, s.options.SessionTTL)
		if err := repo.deleteSession(ctx, previous.TokenHash); err != nil {
			return audit.Event{}, err
		}
		if err := repo.insertSession(ctx, next, s.now()); err != nil {
			return audit.Event{}, err
		}
		return audit.Event{ActorID: user.ID, Action: "auth.login", ObjectType: "auth_session", ObjectID: next.ID, Result: "success"}, nil
	})
	if err != nil {
		return Session{}, "", err
	}
	return next, token, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	return s.audit.RecordChange(ctx, func(ctx context.Context, tx *sql.Tx) (audit.Event, error) {
		repo := s.repo.WithTx(tx)
		session, err := repo.session(ctx, hashToken(token), s.now())
		if err != nil {
			return audit.Event{}, err
		}
		if session.UserID == "" {
			return audit.Event{}, ErrUnauthenticated
		}
		if err := repo.deleteSession(ctx, session.TokenHash); err != nil {
			return audit.Event{}, err
		}
		return audit.Event{ActorID: session.UserID, Action: "auth.logout", ObjectType: "auth_session", ObjectID: session.ID, Result: "success"}, nil
	})
}

func (s *Service) Authorize(ctx context.Context, userID, permission, dormitoryID string) error {
	return authorize(ctx, s.repo, userID, permission, dormitoryID)
}

// AuthorizeTx rechecks current grants in the same transaction as a mutation.
// Using the original DB here would deadlock a single-connection SQLite pool.
func (s *Service) AuthorizeTx(ctx context.Context, tx *sql.Tx, userID, permission, dormitoryID string) error {
	return authorize(ctx, s.repo.WithTx(tx), userID, permission, dormitoryID)
}

func authorize(ctx context.Context, repo *Repository, userID, permission, dormitoryID string) error {
	if userID == "" {
		return ErrUnauthenticated
	}
	allowed, err := repo.allowed(ctx, userID, permission, dormitoryID)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}

func (s *Service) AssignedDormitories(ctx context.Context, userID, permission string) ([]string, error) {
	if userID == "" {
		return nil, ErrUnauthenticated
	}
	allowed, err := s.repo.hasPermission(ctx, userID, permission)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrForbidden
	}
	return s.repo.assignedDormitories(ctx, userID, permission)
}

func (s *Service) User(ctx context.Context, id string) (User, error) { return s.repo.user(ctx, id) }

func (s *Service) Users(ctx context.Context, actorID string) ([]User, error) {
	if err := s.Authorize(ctx, actorID, "auth.users.read", ""); err != nil {
		return nil, err
	}
	return s.repo.users(ctx)
}

func (s *Service) Navigation(ctx context.Context, userID string) (Navigation, error) {
	user, err := s.repo.user(ctx, userID)
	if err != nil {
		return Navigation{}, err
	}
	roles, err := s.repo.roleNames(ctx, userID)
	if err != nil {
		return Navigation{}, err
	}
	dormitories, err := s.repo.readableDormitories(ctx, userID)
	if err != nil {
		return Navigation{}, err
	}
	canReadUsers, err := s.repo.allowed(ctx, userID, "auth.users.read", "")
	if err != nil {
		return Navigation{}, err
	}
	canUseApplications, err := s.repo.allowed(ctx, userID, "applications.own", "")
	if err != nil {
		return Navigation{}, err
	}
	canReviewApplications, err := s.repo.allowed(ctx, userID, "applications.review", "")
	if err != nil {
		return Navigation{}, err
	}
	canAssignApplications, err := s.repo.allowed(ctx, userID, "applications.assign", "")
	if err != nil {
		return Navigation{}, err
	}
	nav := Navigation{CanReviewApplications: canReviewApplications, CanAssignApplications: canAssignApplications, CanUseApplications: canUseApplications, User: user, Roles: roles, DormitoryIDs: dormitories, CanReadUsers: canReadUsers}
	nav.UnreadRoleNotifications, err = s.repo.unreadRoleNotifications(ctx, userID)
	if err != nil {
		return Navigation{}, err
	}
	for _, flag := range []struct {
		permission string
		value      *bool
	}{{"statistics.read", &nav.CanReadStatistics}, {"registry.export", &nav.CanExportRegistry}, {"audit.read", &nav.CanReadAudit}} {
		ids, err := s.repo.assignedDormitories(ctx, userID, flag.permission)
		if err != nil {
			return Navigation{}, err
		}
		for _, id := range ids {
			for _, readable := range dormitories {
				if id == readable {
					*flag.value = true
				}
			}
		}
	}
	return nav, nil
}

func (s *Service) SearchUsers(ctx context.Context, actorID, query string) (UserSearch, error) {
	users, err := s.Users(ctx, actorID)
	if err != nil {
		return UserSearch{}, err
	}
	query = strings.TrimSpace(query)
	if runes := []rune(query); len(runes) > 128 {
		query = string(runes[:128])
	}
	result := UserSearch{Query: query, Total: len(users)}
	result.Roles, err = s.repo.roles(ctx)
	if err != nil {
		return UserSearch{}, err
	}
	result.CanManageRoles, err = s.repo.allowed(ctx, actorID, "auth.users.roles.write", "")
	if err != nil {
		return UserSearch{}, err
	}
	for _, user := range users {
		if strings.Contains(strings.ToLower(user.DisplayName), strings.ToLower(query)) || strings.Contains(strings.ToLower(user.ID), strings.ToLower(query)) {
			managed, err := s.repo.managedUser(ctx, user.ID)
			if err != nil {
				return UserSearch{}, err
			}
			result.Users = append(result.Users, managed)
		}
	}
	return result, nil
}

// AssignedDormitoriesTx reads current grants and assignments in the same snapshot
// as the protected report. It does not use the original DB connection.
func (s *Service) AssignedDormitoriesTx(ctx context.Context, tx *sql.Tx, userID, permission string) ([]string, error) {
	if userID == "" {
		return nil, ErrUnauthenticated
	}
	repo := s.repo.WithTx(tx)
	allowed, err := repo.hasPermission(ctx, userID, permission)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrForbidden
	}
	return repo.assignedDormitories(ctx, userID, permission)
}

// DevelopmentActorIDs resolves server-owned development identities for the
// explicit demo command. It neither assigns roles nor accepts a form user_id.
func (s *Service) DevelopmentActorIDs(ctx context.Context) (string, string, error) {
	if !s.options.DevelopmentLogin {
		return "", "", ErrUnavailable
	}
	writer, err := s.repo.userByIdentity(ctx, Identity{Provider: "development", Subject: "dev_registry_staff"})
	if err != nil {
		return "", "", err
	}
	commandant, err := s.repo.userByIdentity(ctx, Identity{Provider: "development", Subject: "dev_commandant"})
	if err != nil {
		return "", "", err
	}
	return writer.ID, commandant.ID, nil
}

// UsersWithPermission supplies eligible local accounts to a trusted module.
func (s *Service) UsersWithPermission(ctx context.Context, permission string) ([]User, error) {
	return s.repo.UsersWithPermission(ctx, permission)
}
