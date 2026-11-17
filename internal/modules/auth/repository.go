package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type Repository struct{ db executor }

func NewRepository(db *sql.DB) *Repository          { return &Repository{db: db} }
func (r *Repository) WithTx(tx *sql.Tx) *Repository { return &Repository{db: tx} }

func (r *Repository) credential(ctx context.Context, login string) (string, error) {
	var hash string
	err := r.db.QueryRowContext(ctx, `SELECT password_hash FROM auth_dev_credentials WHERE provider = 'development' AND subject = ?`, login).Scan(&hash)
	return hash, err
}

func (r *Repository) userByIdentity(ctx context.Context, identity Identity) (User, error) {
	var user User
	err := r.db.QueryRowContext(ctx, `SELECT u.id, u.display_name FROM auth_users u
		JOIN auth_external_identities i ON i.user_id = u.id
		WHERE i.provider = ? AND i.subject = ? AND u.enabled = 1`, identity.Provider, identity.Subject).Scan(&user.ID, &user.DisplayName)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUnauthenticated
	}
	return user, err
}

func (r *Repository) user(ctx context.Context, id string) (User, error) {
	var user User
	err := r.db.QueryRowContext(ctx, "SELECT id, display_name FROM auth_users WHERE id = ? AND enabled = 1", id).Scan(&user.ID, &user.DisplayName)
	return user, err
}

func (r *Repository) session(ctx context.Context, hash string, now time.Time) (Session, error) {
	var session Session
	var expiry int64
	err := r.db.QueryRowContext(ctx, `SELECT s.id, s.token_hash, coalesce(s.user_id, ''), s.identity_provider, s.csrf_token, s.expires_at
		FROM auth_sessions s LEFT JOIN auth_users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ? AND (s.user_id IS NULL OR u.enabled = 1)`, hash, now.Unix()).Scan(
		&session.ID, &session.TokenHash, &session.UserID, &session.Provider, &session.CSRFToken, &expiry)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrUnauthenticated
	}
	session.ExpiresAt = time.Unix(expiry, 0)
	return session, err
}

func (r *Repository) insertSession(ctx context.Context, session Session, now time.Time) error {
	var userID any
	if session.UserID != "" {
		userID = session.UserID
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO auth_sessions
		(id, token_hash, user_id, identity_provider, csrf_token, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		session.ID, session.TokenHash, userID, session.Provider, session.CSRFToken, session.ExpiresAt.Unix(), now.Unix())
	return err
}

func (r *Repository) deleteSession(ctx context.Context, hash string) error {
	result, err := r.db.ExecContext(ctx, "DELETE FROM auth_sessions WHERE token_hash = ?", hash)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count != 1 {
		return ErrUnauthenticated
	}
	return err
}

func (r *Repository) allowed(ctx context.Context, userID, permission, dormitoryID string) (bool, error) {
	var allowed bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM auth_users u
		JOIN auth_user_roles ur ON ur.user_id = u.id
		JOIN auth_role_permissions rp ON rp.role_id = ur.role_id
		JOIN auth_permissions p ON p.id = rp.permission_id
		WHERE u.id = ? AND u.enabled = 1 AND p.id = ? AND
		(p.scoped = 0 OR EXISTS (SELECT 1 FROM auth_user_dormitories d WHERE d.user_id = u.id AND d.dormitory_id = ?))
	)`, userID, permission, dormitoryID).Scan(&allowed)
	return allowed, err
}

func (r *Repository) users(ctx context.Context) ([]User, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT id, display_name FROM auth_users ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var user User
		if err := rows.Scan(&user.ID, &user.DisplayName); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func (r *Repository) roleNames(ctx context.Context, userID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT r.name FROM auth_roles r
		JOIN auth_user_roles ur ON ur.role_id = r.id WHERE ur.user_id = ? ORDER BY r.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var roles []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		roles = append(roles, name)
	}
	return roles, rows.Err()
}

func (r *Repository) readableDormitories(ctx context.Context, userID string) ([]string, error) {
	return r.assignedDormitories(ctx, userID, "registry.read")
}

func (r *Repository) assignedDormitories(ctx context.Context, userID, permission string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT d.dormitory_id FROM auth_user_dormitories d
		JOIN auth_users u ON u.id = d.user_id WHERE d.user_id = ? AND u.enabled = 1 AND EXISTS (
			SELECT 1 FROM auth_user_roles ur JOIN auth_role_permissions rp ON rp.role_id = ur.role_id
			WHERE ur.user_id = d.user_id AND rp.permission_id = ?
		) ORDER BY d.dormitory_id`, userID, permission)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *Repository) hasPermission(ctx context.Context, userID, permission string) (bool, error) {
	var allowed bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM auth_users u
		JOIN auth_user_roles ur ON ur.user_id = u.id
		JOIN auth_role_permissions rp ON rp.role_id = ur.role_id
		WHERE u.id = ? AND u.enabled = 1 AND rp.permission_id = ?)`, userID, permission).Scan(&allowed)
	return allowed, err
}

func (r *Repository) createDevelopmentUser(ctx context.Context, user User, login, hash, role string, dormitories []string) error {
	if _, err := r.db.ExecContext(ctx, "INSERT INTO auth_users (id, display_name) VALUES (?, ?)", user.ID, user.DisplayName); err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx, "INSERT INTO auth_external_identities (provider, subject, user_id) VALUES ('development', ?, ?)", login, user.ID); err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx, "INSERT INTO auth_dev_credentials (provider, subject, password_hash) VALUES ('development', ?, ?)", login, hash); err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx, "INSERT INTO auth_user_roles (user_id, role_id) VALUES (?, ?)", user.ID, role); err != nil {
		return err
	}
	for _, id := range dormitories {
		if _, err := r.db.ExecContext(ctx, "INSERT INTO auth_user_dormitories (user_id, dormitory_id) VALUES (?, ?)", user.ID, id); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) developmentUserCount(ctx context.Context) (int, error) {
	roles := developmentRoles()
	logins := make([]any, len(roles))
	for i, role := range roles {
		logins[i] = "dev_" + role
	}
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM auth_external_identities
		WHERE provider = 'development' AND subject IN (?, ?, ?, ?, ?)`, logins...).Scan(&count)
	return count, err
}

// UsersWithPermission is an internal directory contract, not an HTTP user list.
func (r *Repository) UsersWithPermission(ctx context.Context, permission string) ([]User, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT u.id,u.display_name FROM auth_users u JOIN auth_user_roles ur ON ur.user_id=u.id JOIN auth_role_permissions rp ON rp.role_id=ur.role_id JOIN auth_permissions p ON p.id=rp.permission_id WHERE u.enabled=1 AND p.scoped=0 AND p.id=? ORDER BY u.display_name,u.id`, permission)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.DisplayName); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
