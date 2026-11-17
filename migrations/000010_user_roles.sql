INSERT INTO auth_permissions(id, scoped) VALUES ('auth.users.roles.write', 0);
INSERT INTO auth_role_permissions(role_id, permission_id) VALUES ('system_admin', 'auth.users.roles.write');

ALTER TABLE auth_users ADD COLUMN role_version INTEGER NOT NULL DEFAULT 1 CHECK(role_version > 0);
CREATE TABLE auth_role_notifications (
    id INTEGER PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES auth_users(id),
    role_id TEXT NOT NULL REFERENCES auth_roles(id),
    role_name TEXT NOT NULL,
    role_version INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    read_at TEXT NOT NULL DEFAULT '',
    UNIQUE(user_id, role_version)
);
