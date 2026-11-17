package auth

import (
	"context"
	"errors"
	"time"
)

var (
	ErrUnauthenticated = errors.New("authentication required")
	ErrForbidden       = errors.New("permission denied")
	ErrCredentials     = errors.New("invalid credentials")
	ErrUnavailable     = errors.New("development login unavailable")
	ErrUserNotFound    = errors.New("user or role notification not found")
	ErrRoleValidation  = errors.New("invalid role")
	ErrRoleConflict    = errors.New("user roles changed")
	ErrLastAdmin       = errors.New("last enabled system administrator")
)

type User struct {
	ID          string
	DisplayName string
}

type Identity struct{ Provider, Subject string }

// IdentityVerifier validates proof for one attempt. A future SSO adapter can
// verify its callback/code without changing local user IDs or permission checks.
// Only trusted application wiring may supply a verifier; never accept identity
// assertions directly from an HTTP form.
type IdentityVerifier interface {
	Verify(context.Context) (Identity, error)
}

type Session struct {
	ID, TokenHash, CSRFToken, Provider string
	UserID                             string
	ExpiresAt                          time.Time
}

type Options struct {
	DevelopmentLogin bool
	EDSLogin         bool
	SessionTTL       time.Duration
}

type Navigation struct {
	UnreadRoleNotifications                            int
	User                                               User
	Roles                                              []string
	DormitoryIDs                                       []string
	CanUseApplications                                 bool
	CanReviewApplications, CanAssignApplications       bool
	CanReadUsers                                       bool
	CanReadStatistics, CanExportRegistry, CanReadAudit bool
}

type UserSearch struct {
	Users          []ManagedUser
	Query          string
	Total          int
	Roles          []Role
	CanManageRoles bool
}

type Role struct{ ID, Name string }

type ManagedUser struct {
	User
	Roles       []Role
	RoleVersion int64
}

func (u ManagedUser) SelectedRoleID() string {
	if len(u.Roles) == 1 {
		return u.Roles[0].ID
	}
	return ""
}

type RoleChange struct {
	RoleID  string
	Version int64
}

type RoleNotification struct {
	ID                          int64
	RoleName, CreatedAt, ReadAt string
}
