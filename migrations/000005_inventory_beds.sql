CREATE TABLE inventory_beds (
    id TEXT PRIMARY KEY NOT NULL,
    room_id TEXT NOT NULL REFERENCES inventory_rooms(id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    number TEXT NOT NULL CHECK (length(number) BETWEEN 1 AND 64 AND number = trim(number)),
    status TEXT NOT NULL DEFAULT 'free' CHECK (status IN ('free', 'reserved', 'occupied', 'maintenance', 'unavailable')),
    archived INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0, 1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK (typeof(version) = 'integer' AND version > 0),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (room_id, number)
);
CREATE TABLE inventory_bed_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    bed_id TEXT NOT NULL REFERENCES inventory_beds(id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    version INTEGER NOT NULL CHECK (version > 0),
    action TEXT NOT NULL CHECK (action IN ('created', 'updated', 'archived', 'restored', 'status_changed')),
    old_status TEXT CHECK (old_status IN ('free', 'reserved', 'occupied', 'maintenance', 'unavailable')),
    new_status TEXT NOT NULL CHECK (new_status IN ('free', 'reserved', 'occupied', 'maintenance', 'unavailable')),
    basis TEXT NOT NULL CHECK (length(trim(basis)) BETWEEN 1 AND 1000),
    actor_id TEXT NOT NULL REFERENCES auth_users(id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    occurred_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (bed_id, version),
    CHECK ((action = 'created' AND old_status IS NULL AND new_status = 'free' AND version = 1)
        OR (action != 'created' AND old_status IS NOT NULL))
);
CREATE TRIGGER inventory_bed_identity_immutable BEFORE UPDATE OF id, room_id ON inventory_beds
WHEN NEW.id != OLD.id OR NEW.room_id != OLD.room_id
BEGIN SELECT RAISE(ABORT, 'inventory bed identity is immutable'); END;

-- Count every non-archived bed, including maintenance and unavailable beds.
-- These guards also protect capacity when independent connections race.
CREATE TRIGGER inventory_bed_capacity_insert BEFORE INSERT ON inventory_beds
WHEN NEW.archived = 0 AND (SELECT count(*) FROM inventory_beds WHERE room_id = NEW.room_id AND archived = 0)
    >= (SELECT capacity FROM inventory_rooms WHERE id = NEW.room_id)
BEGIN SELECT RAISE(ABORT, 'inventory bed capacity exceeded'); END;
CREATE TRIGGER inventory_bed_capacity_restore BEFORE UPDATE OF archived ON inventory_beds
WHEN OLD.archived = 1 AND NEW.archived = 0 AND (SELECT count(*) FROM inventory_beds WHERE room_id = NEW.room_id AND archived = 0)
    >= (SELECT capacity FROM inventory_rooms WHERE id = NEW.room_id)
BEGIN SELECT RAISE(ABORT, 'inventory bed capacity exceeded'); END;
CREATE TRIGGER inventory_room_capacity_beds BEFORE UPDATE OF capacity ON inventory_rooms
WHEN NEW.capacity < (SELECT count(*) FROM inventory_beds WHERE room_id = NEW.id AND archived = 0)
BEGIN SELECT RAISE(ABORT, 'inventory room capacity below active beds'); END;
