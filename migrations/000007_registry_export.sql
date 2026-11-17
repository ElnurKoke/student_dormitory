-- Export is a separate scoped grant; registry.read alone is insufficient.
-- Extend the development matrix without granting business access to system_admin.
INSERT INTO auth_permissions (id, scoped) VALUES ('registry.export', 1);
INSERT INTO auth_role_permissions (role_id, permission_id) VALUES
    ('registry_staff', 'registry.export'), ('administration', 'registry.export');
CREATE INDEX audit_events_scope_time_idx ON audit_events(dormitory_id, occurred_at, id);
-- The application only appends audit events. These guards also reject accidental
-- update/delete SQL through the application connection; DB owners can drop them.
CREATE TRIGGER audit_events_no_update BEFORE UPDATE ON audit_events
BEGIN SELECT RAISE(ABORT, 'audit events are append-only'); END;
CREATE TRIGGER audit_events_no_delete BEFORE DELETE ON audit_events
BEGIN SELECT RAISE(ABORT, 'audit events are append-only'); END;
