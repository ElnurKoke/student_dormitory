package auth

import (
	"context"
	"database/sql"
	"errors"
)

func (r *Repository) roles(ctx context.Context) ([]Role, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name FROM auth_roles ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var roles []Role
	for rows.Next() {
		var role Role
		if err := rows.Scan(&role.ID, &role.Name); err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}
	return roles, rows.Err()
}

func (r *Repository) managedUser(ctx context.Context, id string) (ManagedUser, error) {
	var user ManagedUser
	err := r.db.QueryRowContext(ctx, `SELECT id, display_name, role_version FROM auth_users WHERE id = ?`, id).Scan(&user.ID, &user.DisplayName, &user.RoleVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return user, ErrUserNotFound
	}
	if err != nil {
		return user, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT r.id, r.name FROM auth_user_roles ur JOIN auth_roles r ON r.id = ur.role_id WHERE ur.user_id = ? ORDER BY r.id`, id)
	if err != nil {
		return user, err
	}
	defer rows.Close()
	for rows.Next() {
		var role Role
		if err := rows.Scan(&role.ID, &role.Name); err != nil {
			return user, err
		}
		user.Roles = append(user.Roles, role)
	}
	return user, rows.Err()
}

func (r *Repository) lastAdmin(ctx context.Context, id string) (bool, error) {
	var last bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS (
        SELECT 1 FROM auth_users u JOIN auth_user_roles ur ON ur.user_id = u.id
        WHERE u.id = ? AND u.enabled = 1 AND ur.role_id = 'system_admin'
    ) AND NOT EXISTS (
        SELECT 1 FROM auth_users u JOIN auth_user_roles ur ON ur.user_id = u.id
        WHERE u.id <> ? AND u.enabled = 1 AND ur.role_id = 'system_admin'
    )`, id, id).Scan(&last)
	return last, err
}

func (r *Repository) replaceRole(ctx context.Context, id string, change RoleChange, role Role, at string) error {
	result, err := r.db.ExecContext(ctx, `UPDATE auth_users SET role_version = role_version + 1 WHERE id = ? AND role_version = ?`, id, change.Version)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrRoleConflict
	}
	if _, err := r.db.ExecContext(ctx, `DELETE FROM auth_user_roles WHERE user_id = ?`, id); err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx, `INSERT INTO auth_user_roles(user_id, role_id) VALUES (?, ?)`, id, role.ID); err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO auth_role_notifications(user_id, role_id, role_name, role_version, created_at) VALUES (?, ?, ?, ?, ?)`, id, role.ID, role.Name, change.Version+1, at)
	return err
}

func (r *Repository) roleNotifications(ctx context.Context, userID string) ([]RoleNotification, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, role_name, created_at, read_at FROM auth_role_notifications WHERE user_id = ? ORDER BY id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RoleNotification
	for rows.Next() {
		var n RoleNotification
		if err := rows.Scan(&n.ID, &n.RoleName, &n.CreatedAt, &n.ReadAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (r *Repository) unreadRoleNotifications(ctx context.Context, userID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM auth_role_notifications WHERE user_id = ? AND read_at = ''`, userID).Scan(&count)
	return count, err
}

func (r *Repository) readRoleNotification(ctx context.Context, userID string, id int64, at string) error {
	var readAt string
	err := r.db.QueryRowContext(ctx, `SELECT read_at FROM auth_role_notifications WHERE user_id = ? AND id = ?`, userID, id).Scan(&readAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUserNotFound
	}
	if err != nil {
		return err
	}
	if readAt != "" {
		return errRoleUnchanged
	}
	_, err = r.db.ExecContext(ctx, `UPDATE auth_role_notifications SET read_at = ? WHERE user_id = ? AND id = ?`, at, userID, id)
	return err
}
