package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"student_dormitory/internal/modules/audit"
)

// DevelopmentCredential is returned only to development provisioning code.
// Never pass it to a logger or return it from an HTTP endpoint.
type DevelopmentCredential struct {
	UserID   string `json:"user_id"`
	Login    string `json:"login"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

func (s *Service) ProvisionDevelopment(ctx context.Context, dormitories []string, write func([]DevelopmentCredential) error) error {
	return s.provisionDevelopment(ctx, dormitories, write, false)
}

func developmentRoles() []string {
	return []string{"student", "registry_staff", "commandant", "administration", "system_admin"}
}

func (s *Service) provisionDevelopment(ctx context.Context, dormitories []string, write func([]DevelopmentCredential) error, once bool) error {
	if !s.options.DevelopmentLogin {
		return ErrUnavailable
	}
	if write == nil {
		return fmt.Errorf("credential output is required")
	}
	for _, id := range dormitories {
		if strings.TrimSpace(id) == "" || id == "new" || strings.ContainsAny(id, "/\\") {
			return fmt.Errorf("invalid dormitory scope ID")
		}
	}
	if once {
		count, err := s.repo.developmentUserCount(ctx)
		if err != nil {
			return err
		}
		if count == len(developmentRoles()) {
			return nil
		}
		if count != 0 {
			return fmt.Errorf("incomplete development user batch; existing accounts must be preserved")
		}
	}
	var credentials []DevelopmentCredential
	var hashes []string
	for _, role := range developmentRoles() {
		credential := DevelopmentCredential{UserID: randomToken(), Login: "dev_" + role, Password: randomToken(), Role: role}
		hash, err := hashPassword(credential.Password)
		if err != nil {
			return err
		}
		credentials = append(credentials, credential)
		hashes = append(hashes, hash)
	}
	alreadyProvisioned := errors.New("development users already provisioned")
	err := s.audit.RecordChange(ctx, func(ctx context.Context, tx *sql.Tx) (audit.Event, error) {
		repo := s.repo.WithTx(tx)
		if once {
			// Recheck under SQLite's write lock before creating users or a file.
			count, err := repo.developmentUserCount(ctx)
			if err != nil {
				return audit.Event{}, err
			}
			if count == len(developmentRoles()) {
				return audit.Event{}, alreadyProvisioned
			}
			if count != 0 {
				return audit.Event{}, fmt.Errorf("incomplete development user batch")
			}
		}
		for i, credential := range credentials {
			var scopes []string
			if credential.Role != "student" && credential.Role != "system_admin" {
				scopes = dormitories
			}
			if err := repo.createDevelopmentUser(ctx, User{ID: credential.UserID, DisplayName: credential.Role}, credential.Login, hashes[i], credential.Role, scopes); err != nil {
				return audit.Event{}, err
			}
		}
		if err := write(credentials); err != nil {
			return audit.Event{}, err
		}
		return audit.Event{ActorID: "system:dev-users", Action: "auth.dev_users.created", ObjectType: "auth_user_batch", ObjectID: randomToken(), Result: "success"}, nil
	})
	if errors.Is(err, alreadyProvisioned) {
		return nil
	}
	return err
}
