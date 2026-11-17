package auth

import (
	"context"
	"database/sql"
	"errors"
)

type edsChallenge struct {
	Payload   string
	ExpiresAt int64
}

func (r *Repository) putEDSChallenge(ctx context.Context, sessionID string, challenge edsChallenge, now int64) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM auth_eds_challenges WHERE expires_at <= ?`, now); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO auth_eds_challenges(session_id, payload, expires_at) VALUES (?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET payload=excluded.payload, expires_at=excluded.expires_at`, sessionID, challenge.Payload, challenge.ExpiresAt)
	return err
}
func (r *Repository) edsChallenge(ctx context.Context, sessionID string, now int64) (edsChallenge, error) {
	var challenge edsChallenge
	err := r.db.QueryRowContext(ctx, `SELECT payload, expires_at FROM auth_eds_challenges WHERE session_id=? AND expires_at>?`, sessionID, now).Scan(&challenge.Payload, &challenge.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return challenge, ErrCredentials
	}
	return challenge, err
}

// Lookup includes disabled users: a disabled identity must never create a new account.
func (r *Repository) ncaUser(ctx context.Context, iin string) (User, bool, error) {
	var user User
	var enabled bool
	err := r.db.QueryRowContext(ctx, `SELECT u.id, u.display_name, u.enabled FROM auth_users u
		JOIN auth_external_identities i ON i.user_id=u.id WHERE i.provider='nca' AND i.subject=?`, iin).Scan(&user.ID, &user.DisplayName, &enabled)
	return user, enabled, err
}
func (r *Repository) createNCAUser(ctx context.Context, user User, iin, role string) error {
	if _, err := r.db.ExecContext(ctx, `INSERT INTO auth_users(id, display_name) VALUES (?, ?)`, user.ID, user.DisplayName); err != nil {
		return err
	}
	if err := r.bindNCAIdentity(ctx, user.ID, iin); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO auth_user_roles(user_id, role_id) VALUES (?, ?)`, user.ID, role)
	return err
}
func (r *Repository) bindNCAIdentity(ctx context.Context, userID, iin string) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO auth_external_identities(provider, subject, user_id) VALUES ('nca', ?, ?)`, iin, userID)
	return err
}
func (r *Repository) hasNCAIdentity(ctx context.Context, userID string) (bool, error) {
	var found bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM auth_external_identities WHERE provider='nca' AND user_id=?)`, userID).Scan(&found)
	return found, err
}
func (r *Repository) anyActiveAdmin(ctx context.Context) (bool, error) {
	var found bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM auth_users u JOIN auth_user_roles ur ON ur.user_id=u.id WHERE u.enabled=1 AND ur.role_id='system_admin')`).Scan(&found)
	return found, err
}
func (r *Repository) saveCertificateProfile(ctx context.Context, userID string, p CertificateProfile) error {
	if _, err := r.db.ExecContext(ctx, `UPDATE auth_users SET display_name=? WHERE id=?`, p.Name, userID); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO auth_certificate_profiles(user_id, name, email, verified_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET name=excluded.name, email=excluded.email, verified_at=excluded.verified_at`, userID, p.Name, p.Email, p.VerifiedAt)
	return err
}
func (r *Repository) certificateProfile(ctx context.Context, userID string) (*CertificateProfile, error) {
	var p CertificateProfile
	err := r.db.QueryRowContext(ctx, `SELECT i.subject, p.name, p.email, p.verified_at FROM auth_certificate_profiles p
		JOIN auth_external_identities i ON i.user_id=p.user_id AND i.provider='nca' WHERE p.user_id=?`, userID).Scan(&p.IIN, &p.Name, &p.Email, &p.VerifiedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &p, err
}
