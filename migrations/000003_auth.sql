CREATE TABLE auth_users (
    id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1))
);
CREATE TABLE auth_external_identities (
    provider TEXT NOT NULL,
    subject TEXT NOT NULL,
    user_id TEXT NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
    PRIMARY KEY (provider, subject)
);
CREATE INDEX auth_identity_user_idx ON auth_external_identities(user_id);
CREATE TABLE auth_dev_credentials (
    provider TEXT NOT NULL CHECK (provider = 'development'),
    subject TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    PRIMARY KEY (provider, subject),
    FOREIGN KEY (provider, subject) REFERENCES auth_external_identities(provider, subject) ON DELETE CASCADE
);
CREATE TABLE auth_roles (id TEXT PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE auth_permissions (id TEXT PRIMARY KEY, scoped INTEGER NOT NULL CHECK (scoped IN (0, 1)));
CREATE TABLE auth_role_permissions (
    role_id TEXT NOT NULL REFERENCES auth_roles(id) ON DELETE CASCADE,
    permission_id TEXT NOT NULL REFERENCES auth_permissions(id) ON DELETE CASCADE,
    PRIMARY KEY (role_id, permission_id)
);
CREATE TABLE auth_user_roles (
    user_id TEXT NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
    role_id TEXT NOT NULL REFERENCES auth_roles(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, role_id)
);
-- These are scope IDs only. The registry does not exist yet; a future migration
-- must bind them to real dormitory IDs. No fictional dormitory records are seeded.
CREATE TABLE auth_user_dormitories (
    user_id TEXT NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
    dormitory_id TEXT NOT NULL CHECK (length(trim(dormitory_id)) > 0),
    PRIMARY KEY (user_id, dormitory_id)
);
CREATE TABLE auth_sessions (
    id TEXT PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    user_id TEXT REFERENCES auth_users(id) ON DELETE CASCADE,
    identity_provider TEXT NOT NULL,
    csrf_token TEXT NOT NULL,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX auth_session_expiry_idx ON auth_sessions(expires_at);

-- This is the development permission matrix, not an approved production policy.
INSERT INTO auth_roles (id, name) VALUES
    ('student', 'Студент'), ('registry_staff', 'Сотрудник реестра'),
    ('commandant', 'Комендант'), ('administration', 'Администрация'),
    ('system_admin', 'Системный администратор');
INSERT INTO auth_permissions (id, scoped) VALUES
    ('account.read', 0), ('auth.users.read', 0),
    ('registry.read', 1), ('registry.write', 1), ('places.status.write', 1),
    ('statistics.read', 1), ('audit.read', 1);
INSERT INTO auth_role_permissions (role_id, permission_id)
    SELECT id, 'account.read' FROM auth_roles;
INSERT INTO auth_role_permissions (role_id, permission_id) VALUES
    ('registry_staff', 'registry.read'), ('registry_staff', 'registry.write'),
    ('commandant', 'registry.read'), ('commandant', 'places.status.write'),
    ('administration', 'registry.read'), ('administration', 'statistics.read'),
    ('administration', 'audit.read'), ('system_admin', 'auth.users.read');
