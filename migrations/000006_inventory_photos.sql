CREATE TABLE inventory_photos (
    id TEXT PRIMARY KEY NOT NULL,
    dormitory_id TEXT REFERENCES inventory_dormitories(id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    room_id TEXT REFERENCES inventory_rooms(id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    storage_key TEXT NOT NULL UNIQUE,
    media_type TEXT NOT NULL CHECK (media_type IN ('image/jpeg', 'image/png')),
    byte_size INTEGER NOT NULL CHECK (byte_size > 0),
    width INTEGER NOT NULL CHECK (width BETWEEN 1 AND 4096),
    height INTEGER NOT NULL CHECK (height BETWEEN 1 AND 4096 AND width * height <= 12000000),
    description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 1000),
    position INTEGER NOT NULL CHECK (position BETWEEN 0 AND 1000000),
    is_primary INTEGER NOT NULL DEFAULT 0 CHECK (is_primary IN (0,1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    CHECK ((dormitory_id IS NOT NULL) != (room_id IS NOT NULL))
);
CREATE INDEX inventory_photo_dormitory_order ON inventory_photos(dormitory_id, position, id);
CREATE INDEX inventory_photo_room_order ON inventory_photos(room_id, position, id);
CREATE UNIQUE INDEX inventory_photo_dormitory_primary ON inventory_photos(dormitory_id) WHERE is_primary = 1;
CREATE UNIQUE INDEX inventory_photo_room_primary ON inventory_photos(room_id) WHERE is_primary = 1;
CREATE TRIGGER inventory_photo_identity_immutable BEFORE UPDATE OF id, dormitory_id, room_id, storage_key ON inventory_photos
WHEN NEW.id != OLD.id OR NEW.dormitory_id IS NOT OLD.dormitory_id OR NEW.room_id IS NOT OLD.room_id OR NEW.storage_key != OLD.storage_key
BEGIN SELECT RAISE(ABORT, 'photo identity is immutable'); END;
