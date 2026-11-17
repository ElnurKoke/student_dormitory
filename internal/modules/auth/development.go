package auth

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
)

const passwordIterations = 600_000

func randomToken() string {
	var bytes [32]byte
	rand.Read(bytes[:])
	return base64.RawURLEncoding.EncodeToString(bytes[:])
}

func hashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

func hashPassword(password string) (string, error) {
	salt := randomToken()
	key, err := pbkdf2.Key(sha256.New, password, []byte(salt), passwordIterations, 32)
	return salt + ":" + base64.RawURLEncoding.EncodeToString(key), err
}

type DevelopmentProvider struct{ repo *Repository }

func NewDevelopmentProvider(repo *Repository) *DevelopmentProvider {
	return &DevelopmentProvider{repo: repo}
}

// Each attempt owns its proof; the service only depends on IdentityVerifier.
func (p *DevelopmentProvider) Attempt(login, password string) IdentityVerifier {
	return &developmentAttempt{repo: p.repo, login: login, password: password}
}

type developmentAttempt struct {
	repo            *Repository
	login, password string
}

func (a *developmentAttempt) Verify(ctx context.Context) (Identity, error) {
	if len(a.login) > 128 || len(a.password) > 128 || a.login == "" || a.password == "" {
		return Identity{}, ErrCredentials
	}
	stored, err := a.repo.credential(ctx, a.login)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Identity{}, err
	}
	missing := errors.Is(err, sql.ErrNoRows)
	// Unknown users still pay the password derivation cost; there is no fallback password.
	if missing {
		stored = randomToken() + ":" + randomToken()
	}
	salt, encoded, ok := strings.Cut(stored, ":")
	if !ok {
		return Identity{}, ErrCredentials
	}
	want, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return Identity{}, ErrCredentials
	}
	got, err := pbkdf2.Key(sha256.New, a.password, []byte(salt), passwordIterations, 32)
	if err != nil {
		return Identity{}, err
	}
	if subtle.ConstantTimeCompare(got, want) != 1 || missing {
		return Identity{}, ErrCredentials
	}
	return Identity{Provider: "development", Subject: a.login}, nil
}
