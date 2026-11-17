-- NCA identities use provider='nca', subject=IIN in auth_external_identities.
-- The existing (provider, subject) primary key prevents duplicate accounts.
CREATE UNIQUE INDEX auth_nca_user_identity_idx ON auth_external_identities(user_id) WHERE provider = 'nca';
CREATE TABLE auth_certificate_profiles (
    user_id TEXT PRIMARY KEY REFERENCES auth_users(id),
    name TEXT NOT NULL,
    email TEXT NOT NULL DEFAULT '',
    verified_at TEXT NOT NULL
);
CREATE TABLE auth_eds_challenges (
    session_id TEXT PRIMARY KEY REFERENCES auth_sessions(id) ON DELETE CASCADE,
    payload TEXT NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE INDEX auth_eds_challenge_expiry_idx ON auth_eds_challenges(expires_at);
