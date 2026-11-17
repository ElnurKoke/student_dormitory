-- Local proposal for the development student workflow, not an approved regulation.
INSERT INTO auth_permissions(id, scoped) VALUES ('applications.own', 0);
INSERT INTO auth_role_permissions(role_id, permission_id) VALUES ('student', 'applications.own');
CREATE TABLE application_profiles (
 user_id TEXT PRIMARY KEY REFERENCES auth_users(id),
 fields_json TEXT NOT NULL CHECK(json_valid(fields_json)),
 version INTEGER NOT NULL CHECK(version > 0)
);
CREATE TABLE application_consents (
 version TEXT PRIMARY KEY,
 body TEXT NOT NULL
);
INSERT INTO application_consents VALUES ('test-v1', 'ТЕСТОВЫЙ ТЕКСТ — ТРЕБУЕТ СОГЛАСОВАНИЯ. Я соглашаюсь на обработку введённых мной данных профиля и заявления для тестового рассмотрения обращения о предоставлении общежития. Текст не является утверждённым юридическим согласием.');
CREATE TRIGGER application_consent_no_update BEFORE UPDATE ON application_consents BEGIN SELECT RAISE(ABORT, 'immutable consent'); END;
CREATE TRIGGER application_consent_no_delete BEFORE DELETE ON application_consents BEGIN SELECT RAISE(ABORT, 'immutable consent'); END;
CREATE TABLE applications (
 id TEXT PRIMARY KEY,
 user_id TEXT NOT NULL REFERENCES auth_users(id),
 status TEXT NOT NULL CHECK(status IN ('draft','submitted','under_review','needs_information','verified','withdrawn')),
 fields_json TEXT NOT NULL CHECK(json_valid(fields_json)),
 version INTEGER NOT NULL CHECK(version > 0),
 submit_key TEXT NOT NULL UNIQUE,
 submitted_version INTEGER,
 submitted_at TEXT NOT NULL DEFAULT '',
 consent_version TEXT REFERENCES application_consents(version),
 consent_accepted_at TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 CHECK ((status = 'draft' AND submitted_at = '' AND consent_version IS NULL AND submitted_version IS NULL) OR
 (status <> 'draft' AND submitted_at <> '' AND consent_version IS NOT NULL AND consent_accepted_at <> '' AND submitted_version IS NOT NULL))
);
CREATE INDEX applications_owner_idx ON applications(user_id, created_at);
CREATE TABLE application_registrations (
 number INTEGER PRIMARY KEY AUTOINCREMENT,
 application_id TEXT NOT NULL UNIQUE REFERENCES applications(id)
);
CREATE TABLE application_history (
 id INTEGER PRIMARY KEY,
 application_id TEXT NOT NULL REFERENCES applications(id),
 actor_id TEXT NOT NULL REFERENCES auth_users(id),
 action TEXT NOT NULL,
 status TEXT NOT NULL,
 occurred_at TEXT NOT NULL,
 version INTEGER NOT NULL,
 UNIQUE(application_id, version)
);
CREATE TRIGGER application_history_no_update BEFORE UPDATE ON application_history BEGIN SELECT RAISE(ABORT, 'immutable history'); END;
CREATE TRIGGER application_history_no_delete BEFORE DELETE ON application_history BEGIN SELECT RAISE(ABORT, 'immutable history'); END;
