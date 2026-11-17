CREATE TABLE inventory_dormitories (
    id TEXT PRIMARY KEY NOT NULL,
    name TEXT NOT NULL CHECK (length(trim(name)) > 0),
    address TEXT NOT NULL CHECK (length(trim(address)) > 0),
    operator TEXT NOT NULL CHECK (length(trim(operator)) > 0),
    use_basis TEXT NOT NULL CHECK (length(trim(use_basis)) > 0),
    contacts TEXT NOT NULL CHECK (length(trim(contacts)) > 0),
    description TEXT NOT NULL DEFAULT '',
    published INTEGER NOT NULL DEFAULT 0 CHECK (published IN (0, 1)),
    archived INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0, 1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE TABLE inventory_buildings (
    id TEXT PRIMARY KEY NOT NULL,
    dormitory_id TEXT NOT NULL REFERENCES inventory_dormitories(id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    name TEXT NOT NULL CHECK (length(trim(name)) > 0),
    archived INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0, 1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX inventory_building_parent_idx ON inventory_buildings(dormitory_id);
CREATE TABLE inventory_floors (
    id TEXT PRIMARY KEY NOT NULL,
    building_id TEXT NOT NULL REFERENCES inventory_buildings(id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    number TEXT NOT NULL CHECK (length(trim(number)) > 0),
    archived INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0, 1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX inventory_floor_parent_idx ON inventory_floors(building_id);
CREATE TABLE inventory_rooms (
    id TEXT PRIMARY KEY NOT NULL,
    floor_id TEXT NOT NULL REFERENCES inventory_floors(id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    number TEXT NOT NULL CHECK (length(number) > 0 AND number = trim(number)),
    capacity INTEGER NOT NULL CHECK (typeof(capacity) = 'integer' AND capacity > 0),
    area_hundredths INTEGER NOT NULL CHECK (typeof(area_hundredths) = 'integer' AND area_hundredths > 0),
    equipment TEXT NOT NULL,
    sanitary TEXT NOT NULL,
    accessible INTEGER NOT NULL CHECK (accessible IN (0, 1)),
    accessibility_notes TEXT NOT NULL DEFAULT '',
    price_minor INTEGER NOT NULL CHECK (typeof(price_minor) = 'integer' AND price_minor >= 0),
    currency TEXT NOT NULL CHECK (length(currency) = 3 AND currency NOT GLOB '*[^A-Z]*'),
    price_period TEXT NOT NULL CHECK (length(trim(price_period)) > 0),
    archived INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0, 1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (floor_id, number)
);
-- Audit remains linked to the permanent scope even if objects are archived.
ALTER TABLE audit_events ADD COLUMN dormitory_id TEXT REFERENCES inventory_dormitories(id) ON DELETE RESTRICT ON UPDATE RESTRICT;
ALTER TABLE audit_events ADD COLUMN changes_json TEXT NOT NULL DEFAULT '{}';
CREATE INDEX audit_events_scope_idx ON audit_events(dormitory_id, id);

CREATE TRIGGER inventory_dormitory_id_immutable BEFORE UPDATE OF id ON inventory_dormitories
WHEN NEW.id != OLD.id BEGIN SELECT RAISE(ABORT, 'inventory ID is immutable'); END;
CREATE TRIGGER inventory_building_id_immutable BEFORE UPDATE OF id ON inventory_buildings
WHEN NEW.id != OLD.id BEGIN SELECT RAISE(ABORT, 'inventory ID is immutable'); END;
CREATE TRIGGER inventory_floor_id_immutable BEFORE UPDATE OF id ON inventory_floors
WHEN NEW.id != OLD.id BEGIN SELECT RAISE(ABORT, 'inventory ID is immutable'); END;
CREATE TRIGGER inventory_room_id_immutable BEFORE UPDATE OF id ON inventory_rooms
WHEN NEW.id != OLD.id BEGIN SELECT RAISE(ABORT, 'inventory ID is immutable'); END;
