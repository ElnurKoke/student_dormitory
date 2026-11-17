package auth_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"student_dormitory/internal/modules/applications"
	"student_dormitory/internal/modules/audit"
	"student_dormitory/internal/modules/auth"
	"student_dormitory/internal/platform/database"
	"student_dormitory/internal/platform/nca"
	"student_dormitory/internal/platform/requestid"
)

type certificateVerifierFunc func(context.Context, string, []byte) (nca.Identity, error)

func (f certificateVerifierFunc) Verify(ctx context.Context, cms string, expected []byte) (nca.Identity, error) {
	return f(ctx, cms, expected)
}

type profileSeederFunc func(context.Context, *sql.Tx, string, auth.CertificateProfile) error

func (f profileSeederFunc) SeedCertificateProfileTx(ctx context.Context, tx *sql.Tx, user string, p auth.CertificateProfile) error {
	return f(ctx, tx, user, p)
}

type edsFixture struct {
	db       *sql.DB
	ctx      context.Context
	auth     *auth.Service
	eds      *auth.EDSService
	profiles *applications.Service
	identity nca.Identity
}

func newEDSFixture(t *testing.T) *edsFixture {
	t.Helper()
	ctx := requestid.WithID(context.Background(), "eds-service-test")
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "eds.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	recorder := audit.NewRecorder(db)
	core := auth.NewService(auth.NewRepository(db), recorder, auth.Options{EDSLogin: true, SessionTTL: time.Hour})
	f := &edsFixture{db: db, ctx: ctx, auth: core, identity: nca.Identity{IIN: "000000000001", Name: "Тестовый Студент", Email: "student@example.test"}}
	f.profiles = applications.NewService(applications.NewRepository(db), core, recorder, core)
	// This verifier isolates the account transaction tests from cryptography.
	// NCANode's strict HTTP contract is tested separately in platform/nca.
	verifier := certificateVerifierFunc(func(ctx context.Context, signature string, expected []byte) (nca.Identity, error) {
		data, err := base64.StdEncoding.DecodeString(signature)
		if err != nil || !bytes.Equal(data, expected) {
			return nca.Identity{}, nca.ErrInvalidSignature
		}
		return f.identity, nil
	})
	f.eds = auth.NewEDSService(core, verifier, f.profiles, "https://dorm.example.test")
	return f
}
func (f *edsFixture) challenge(t *testing.T) (string, auth.EDSChallenge) {
	t.Helper()
	_, token, err := f.auth.Anonymous(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := f.eds.Challenge(f.ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	return token, challenge
}
func edsCount(t *testing.T, f *edsFixture, query string, args ...any) int {
	t.Helper()
	var count int
	if err := f.db.QueryRowContext(f.ctx, query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
func TestEDSRegistrationExistingIdentityRolesAndCertificateProfile(t *testing.T) {
	f := newEDSFixture(t)
	anonymous, challenge := f.challenge(t)
	payload, err := base64.StdEncoding.DecodeString(challenge.Data)
	if err != nil || !bytes.Contains(payload, []byte(`"origin":"https://dorm.example.test"`)) || !bytes.Contains(payload, []byte(`"purpose":"student_dormitory.login"`)) || challenge.ExpiresAt > time.Now().Add(5*time.Minute).Unix() {
		t.Fatal("invalid audience/expiry", err)
	}
	session, token, err := f.eds.Login(f.ctx, anonymous, challenge.Data)
	if err != nil || token == anonymous || session.UserID == "" || session.Provider != "nca" {
		t.Fatal("EDS login", err)
	}
	if _, err := f.auth.Session(f.ctx, anonymous); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("anonymous session replay", err)
	}
	if _, _, err := f.eds.Login(f.ctx, anonymous, challenge.Data); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("signed challenge replay", err)
	}
	if err := f.auth.Authorize(f.ctx, session.UserID, "applications.own", ""); err != nil {
		t.Fatal("new account is not a student", err)
	}
	if err := f.auth.Authorize(f.ctx, session.UserID, "auth.users.roles.write", ""); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("self registration granted admin", err)
	}
	profile, err := f.auth.CertificateProfile(f.ctx, session.UserID)
	if err != nil || profile == nil || profile.IIN != f.identity.IIN || profile.Name != f.identity.Name || profile.Email != f.identity.Email || profile.VerifiedAt == "" {
		t.Fatal("certificate cabinet data", profile, err)
	}
	p, err := f.profiles.Profile(f.ctx, session.UserID)
	if err != nil || p.Version != 1 || p.Fields.Name.Value != f.identity.Name || p.Fields.Name.Source != applications.CertificateSource || p.Fields.Email.Value != f.identity.Email || p.Fields.Group.Value != "" || p.IIN.Value != f.identity.IIN || p.IIN.Source != applications.CertificateSource || p.IIN.EnteredAt != profile.VerifiedAt {
		t.Fatal("student profile not seeded", p, err)
	}
	if edsCount(t, f, `SELECT count(*) FROM auth_dev_credentials`) != 0 || edsCount(t, f, `SELECT count(*) FROM auth_eds_challenges`) != 0 {
		t.Fatal("password created or challenge unconsumed")
	}
	if edsCount(t, f, `SELECT count(*) FROM audit_events WHERE action='auth.eds_registered' AND actor_id=? AND object_id=?`, session.UserID, session.UserID) != 1 {
		t.Fatal("registration not audited")
	}
	if edsCount(t, f, `SELECT count(*) FROM audit_events WHERE changes_json LIKE ? OR object_id=? OR actor_id=?`, "%"+f.identity.IIN+"%", f.identity.IIN, f.identity.IIN) != 0 {
		t.Fatal("IIN in audit")
	}
	if err := f.auth.ProvisionNCAIdentity(f.ctx, "000000000002", "", true); err != nil {
		t.Fatal(err)
	}
	var admin string
	if err := f.db.QueryRowContext(f.ctx, `SELECT user_id FROM auth_external_identities WHERE provider='nca' AND subject='000000000002'`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	if err := f.auth.ChangeRole(f.ctx, admin, session.UserID, auth.RoleChange{RoleID: "commandant", Version: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecContext(f.ctx, `INSERT INTO auth_user_dormitories(user_id,dormitory_id) VALUES (?, 'original-dorm')`, session.UserID); err != nil {
		t.Fatal(err)
	}
	f.identity.Name = "Обновлённое ФИО сертификата"
	anonymous, challenge = f.challenge(t)
	next, _, err := f.eds.Login(f.ctx, anonymous, challenge.Data)
	if err != nil || next.UserID != session.UserID {
		t.Fatal("existing IIN created a different account", err)
	}
	if err := f.auth.Authorize(f.ctx, next.UserID, "places.status.write", "original-dorm"); err != nil {
		t.Fatal("role or scope overwritten", err)
	}
	profile, _ = f.auth.CertificateProfile(f.ctx, next.UserID)
	if profile.Name != f.identity.Name || edsCount(t, f, `SELECT count(*) FROM auth_users`) != 2 {
		t.Fatal("renewal data or account duplication")
	}
	coreDisabled := auth.NewService(auth.NewRepository(f.db), audit.NewRecorder(f.db), auth.Options{SessionTTL: time.Hour})
	if _, err := coreDisabled.Session(f.ctx, token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("EDS session accepted after disabling provider", err)
	}
}

func TestEDSChallengeIsBoundToAnonymousSessionAndLatestAttempt(t *testing.T) {
	f := newEDSFixture(t)
	one, c1 := f.challenge(t)
	two, c2 := f.challenge(t)
	if c1.Data == c2.Data {
		t.Fatal("nonce reused")
	}
	if _, _, err := f.eds.Login(f.ctx, two, c1.Data); !errors.Is(err, nca.ErrInvalidSignature) {
		t.Fatal("stolen challenge accepted", err)
	}
	replacement, err := f.eds.Challenge(f.ctx, one)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.eds.Login(f.ctx, one, c1.Data); !errors.Is(err, nca.ErrInvalidSignature) {
		t.Fatal("superseded challenge accepted", err)
	}
	if _, err := f.db.ExecContext(f.ctx, `UPDATE auth_eds_challenges SET expires_at=0`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.eds.Login(f.ctx, one, replacement.Data); !errors.Is(err, auth.ErrCredentials) {
		t.Fatal("expired challenge accepted", err)
	}
	if edsCount(t, f, `SELECT count(*) FROM auth_users`) != 0 {
		t.Fatal("invalid attempt created account")
	}
}

func TestEDSRechecksChallengeAfterNetworkVerification(t *testing.T) {
	for _, mutate := range []string{`UPDATE auth_eds_challenges SET payload='replaced'`, `UPDATE auth_eds_challenges SET expires_at=0`, `DELETE FROM auth_sessions`} {
		t.Run(mutate, func(t *testing.T) {
			f := newEDSFixture(t)
			token, c := f.challenge(t)
			verifier := certificateVerifierFunc(func(context.Context, string, []byte) (nca.Identity, error) {
				_, err := f.db.ExecContext(f.ctx, mutate)
				return f.identity, err
			})
			eds := auth.NewEDSService(f.auth, verifier, f.profiles, "https://dorm.example.test")
			if _, _, err := eds.Login(f.ctx, token, c.Data); err == nil {
				t.Fatal("changed challenge/session accepted")
			}
			if edsCount(t, f, `SELECT count(*) FROM auth_users`) != 0 {
				t.Fatal("created account after race")
			}
		})
	}
}

func TestEDSRejectsDisabledIdentityWithoutCreatingAnotherAccount(t *testing.T) {
	f := newEDSFixture(t)
	token, c := f.challenge(t)
	session, _, err := f.eds.Login(f.ctx, token, c.Data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecContext(f.ctx, `UPDATE auth_users SET enabled=0 WHERE id=?`, session.UserID); err != nil {
		t.Fatal(err)
	}
	token, c = f.challenge(t)
	if _, _, err := f.eds.Login(f.ctx, token, c.Data); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("disabled account recreated", err)
	}
	if edsCount(t, f, `SELECT count(*) FROM auth_users`) != 1 {
		t.Fatal("disabled account duplicated")
	}
}

func TestEDSLoginRollsBackEveryRequiredWrite(t *testing.T) {
	for _, table := range []string{"auth_users", "auth_external_identities", "auth_user_roles", "auth_certificate_profiles", "application_profiles", "auth_sessions", "audit_events"} {
		t.Run(table, func(t *testing.T) {
			f := newEDSFixture(t)
			token, c := f.challenge(t)
			condition := ""
			if table == "auth_sessions" {
				condition = " WHEN NEW.identity_provider='nca'"
			}
			if _, err := f.db.ExecContext(f.ctx, `CREATE TRIGGER reject_eds BEFORE INSERT ON `+table+condition+` BEGIN SELECT RAISE(ABORT,'test failure'); END`); err != nil {
				t.Fatal(err)
			}
			if _, _, err := f.eds.Login(f.ctx, token, c.Data); err == nil {
				t.Fatal("failure ignored")
			}
			for _, table := range []string{"auth_users", "auth_external_identities", "auth_user_roles", "auth_certificate_profiles", "application_profiles"} {
				if edsCount(t, f, `SELECT count(*) FROM `+table) != 0 {
					t.Fatal("partial write", table)
				}
			}
			if _, err := f.auth.Session(f.ctx, token); err != nil {
				t.Fatal("anonymous session was lost on rollback", err)
			}
			if edsCount(t, f, `SELECT count(*) FROM auth_eds_challenges`) != 1 {
				t.Fatal("challenge lost on rollback")
			}
		})
	}
}

func TestEDSLoginRollsBackProfileContractFailure(t *testing.T) {
	f := newEDSFixture(t)
	token, c := f.challenge(t)
	profiles := profileSeederFunc(func(ctx context.Context, tx *sql.Tx, id string, p auth.CertificateProfile) error {
		if err := f.profiles.SeedCertificateProfileTx(ctx, tx, id, p); err != nil {
			return err
		}
		return errors.New("test downstream failure")
	})
	verifier := certificateVerifierFunc(func(context.Context, string, []byte) (nca.Identity, error) { return f.identity, nil })
	eds := auth.NewEDSService(f.auth, verifier, profiles, "https://dorm.example.test")
	if _, _, err := eds.Login(f.ctx, token, c.Data); err == nil {
		t.Fatal("profile failure ignored")
	}
	if edsCount(t, f, `SELECT count(*) FROM auth_users`) != 0 || edsCount(t, f, `SELECT count(*) FROM application_profiles`) != 0 {
		t.Fatal("profile contract escaped transaction")
	}
}

func TestConcurrentEDSLoginsCreateOneLocalIdentity(t *testing.T) {
	f := newEDSFixture(t)
	one, c1 := f.challenge(t)
	two, c2 := f.challenge(t)
	type result struct {
		session auth.Session
		err     error
	}
	results := make(chan result, 2)
	for _, attempt := range []struct{ token, signature string }{{one, c1.Data}, {two, c2.Data}} {
		go func() {
			session, _, err := f.eds.Login(f.ctx, attempt.token, attempt.signature)
			results <- result{session, err}
		}()
	}
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.session.UserID != second.session.UserID {
		t.Fatal("concurrent duplicate", first.err, second.err)
	}
	if edsCount(t, f, `SELECT count(*) FROM auth_users`) != 1 || edsCount(t, f, `SELECT count(*) FROM auth_external_identities WHERE provider='nca'`) != 1 || edsCount(t, f, `SELECT count(*) FROM auth_sessions WHERE identity_provider='nca'`) != 2 {
		t.Fatal("identity duplication")
	}
}
