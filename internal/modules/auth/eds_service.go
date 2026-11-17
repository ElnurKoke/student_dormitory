package auth

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"student_dormitory/internal/modules/audit"
	"student_dormitory/internal/platform/nca"
)

const edsChallengeTTL = 5 * time.Minute

type CertificateVerifier interface {
	Verify(context.Context, string, []byte) (nca.Identity, error)
}

type CertificateProfile struct {
	nca.Identity
	VerifiedAt string
}

type CertificateProfileSeeder interface {
	SeedCertificateProfileTx(context.Context, *sql.Tx, string, CertificateProfile) error
}

type EDSService struct {
	auth     *Service
	verifier CertificateVerifier
	profiles CertificateProfileSeeder
	origin   string
}

func NewEDSService(service *Service, verifier CertificateVerifier, profiles CertificateProfileSeeder, origin string) *EDSService {
	return &EDSService{auth: service, verifier: verifier, profiles: profiles, origin: origin}
}

type EDSChallenge struct {
	Data      string `json:"data"`
	ExpiresAt int64  `json:"expires_at"`
}

func (s *EDSService) anonymous(ctx context.Context, token string) (Session, error) {
	session, err := s.auth.Session(ctx, token)
	if err != nil {
		return Session{}, err
	}
	if session.UserID != "" || session.Provider != "anonymous" {
		return Session{}, ErrForbidden
	}
	return session, nil
}

func (s *EDSService) Challenge(ctx context.Context, token string) (EDSChallenge, error) {
	session, err := s.anonymous(ctx, token)
	if err != nil {
		return EDSChallenge{}, err
	}
	expiresAt := min(s.auth.now().Add(edsChallengeTTL).Unix(), session.ExpiresAt.Unix())
	payload, err := json.Marshal(struct {
		Purpose   string `json:"purpose"`
		Origin    string `json:"origin"`
		Nonce     string `json:"nonce"`
		ExpiresAt int64  `json:"expires_at"`
	}{"student_dormitory.login", s.origin, randomToken(), expiresAt})
	if err != nil {
		return EDSChallenge{}, err
	}
	err = s.auth.audit.RecordChange(ctx, func(ctx context.Context, tx *sql.Tx) (audit.Event, error) {
		repo := s.auth.repo.WithTx(tx)
		current, err := repo.session(ctx, hashToken(token), s.auth.now())
		if err != nil {
			return audit.Event{}, err
		}
		if current.UserID != "" || current.Provider != "anonymous" {
			return audit.Event{}, ErrForbidden
		}
		if err := repo.putEDSChallenge(ctx, current.ID, edsChallenge{string(payload), expiresAt}, s.auth.now().Unix()); err != nil {
			return audit.Event{}, err
		}
		return audit.Event{ActorID: "anonymous", Action: "auth.eds_challenge_created", ObjectType: "auth_session", ObjectID: current.ID, Result: "success"}, nil
	})
	if err != nil {
		return EDSChallenge{}, err
	}
	return EDSChallenge{Data: base64.StdEncoding.EncodeToString(payload), ExpiresAt: expiresAt}, nil
}

func (s *EDSService) Login(ctx context.Context, previousToken, signature string) (Session, string, error) {
	previous, err := s.anonymous(ctx, previousToken)
	if err != nil {
		return Session{}, "", err
	}
	challenge, err := s.auth.repo.edsChallenge(ctx, previous.ID, s.auth.now().Unix())
	if err != nil {
		return Session{}, "", err
	}
	// Network verification occurs before taking SQLite's write lock. The challenge
	// and the anonymous session are rechecked under that lock before any mutation.
	identity, err := s.verifier.Verify(ctx, signature, []byte(challenge.Payload))
	if err != nil {
		return Session{}, "", err
	}
	if err := identity.Validate(); err != nil {
		return Session{}, "", err
	}
	var next Session
	var token string
	err = s.auth.audit.RecordChange(ctx, func(ctx context.Context, tx *sql.Tx) (audit.Event, error) {
		repo := s.auth.repo.WithTx(tx)
		current, err := repo.session(ctx, hashToken(previousToken), s.auth.now())
		if err != nil {
			return audit.Event{}, err
		}
		if current.UserID != "" || current.Provider != "anonymous" {
			return audit.Event{}, ErrForbidden
		}
		latest, err := repo.edsChallenge(ctx, current.ID, s.auth.now().Unix())
		if err != nil {
			return audit.Event{}, err
		}
		if latest.Payload != challenge.Payload {
			return audit.Event{}, ErrCredentials
		}
		user, enabled, err := repo.ncaUser(ctx, identity.IIN)
		created := errors.Is(err, sql.ErrNoRows)
		if err != nil && !created {
			return audit.Event{}, err
		}
		if !created && !enabled {
			return audit.Event{}, ErrUnauthenticated
		}
		if created {
			user = User{ID: randomToken(), DisplayName: identity.Name}
			if err := repo.createNCAUser(ctx, user, identity.IIN, "student"); err != nil {
				return audit.Event{}, err
			}
		}
		profile := CertificateProfile{Identity: identity, VerifiedAt: s.auth.now().UTC().Format(time.RFC3339Nano)}
		if err := repo.saveCertificateProfile(ctx, user.ID, profile); err != nil {
			return audit.Event{}, err
		}
		if err := s.profiles.SeedCertificateProfileTx(ctx, tx, user.ID, profile); err != nil {
			return audit.Event{}, err
		}
		next, token = s.auth.newSession(user.ID, "nca", s.auth.options.SessionTTL)
		if err := repo.deleteSession(ctx, current.TokenHash); err != nil {
			return audit.Event{}, err
		}
		if err := repo.insertSession(ctx, next, s.auth.now()); err != nil {
			return audit.Event{}, err
		}
		event := audit.Event{ActorID: user.ID, Action: "auth.login", ObjectType: "auth_session", ObjectID: next.ID, Result: "success"}
		if created {
			event.Action, event.ObjectType, event.ObjectID = "auth.eds_registered", "auth_user", user.ID
		}
		return event, nil
	})
	if err != nil {
		return Session{}, "", err
	}
	return next, token, nil
}

func (s *Service) CertificateProfile(ctx context.Context, actor string) (*CertificateProfile, error) {
	if err := s.Authorize(ctx, actor, "account.read", ""); err != nil {
		return nil, err
	}
	return s.repo.certificateProfile(ctx, actor)
}
