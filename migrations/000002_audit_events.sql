CREATE TABLE audit_events (
    id INTEGER PRIMARY KEY,
    actor_id TEXT NOT NULL CHECK (length(trim(actor_id)) > 0),
    occurred_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    action TEXT NOT NULL CHECK (length(trim(action)) > 0),
    object_type TEXT NOT NULL CHECK (length(trim(object_type)) > 0),
    object_id TEXT NOT NULL CHECK (length(trim(object_id)) > 0),
    result TEXT NOT NULL CHECK (length(trim(result)) > 0),
    request_id TEXT NOT NULL CHECK (length(trim(request_id)) > 0)
);

CREATE INDEX audit_events_object_idx ON audit_events (object_type, object_id, id);
CREATE INDEX audit_events_request_idx ON audit_events (request_id, id);
