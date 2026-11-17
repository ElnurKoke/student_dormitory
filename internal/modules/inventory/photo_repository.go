package inventory

import (
	"context"
	"database/sql"
	"errors"

	"student_dormitory/internal/platform/database"
)

// Exclusive serializes reconciliation/file creation against metadata changes,
// including across processes using the same SQLite DB (BEGIN IMMEDIATE).
func (r *Repository) Exclusive(ctx context.Context, fn func(*Repository, *sql.Tx) error) error {
	return database.WithinTx(ctx, r.root, func(ctx context.Context, tx *sql.Tx) error { return fn(r.WithTx(tx), tx) })
}

const photoColumns = `id, coalesce(dormitory_id,''), coalesce(room_id,''), storage_key, media_type, byte_size, width, height, description, position, is_primary, version, created_at`

func scanPhoto(scanner interface{ Scan(...any) error }) (Photo, error) {
	var p Photo
	err := scanner.Scan(&p.ID, &p.DormitoryID, &p.RoomID, &p.StorageKey, &p.MediaType, &p.ByteSize, &p.Width, &p.Height, &p.Description, &p.Position, &p.Primary, &p.Version, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}
func (r *Repository) Photo(ctx context.Context, id string) (Photo, error) {
	return scanPhoto(r.db.QueryRowContext(ctx, "SELECT "+photoColumns+" FROM inventory_photos WHERE id=?", id))
}
func photoOwner(p Location) (string, string, error) {
	switch p.Kind() {
	case Dormitory:
		return "dormitory_id", p.DormitoryID, nil
	case Room:
		return "room_id", p.RoomID, nil
	default:
		return "", "", ErrNotFound
	}
}
func (r *Repository) Photos(ctx context.Context, p Location) ([]Photo, error) {
	column, id, err := photoOwner(p)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, "SELECT "+photoColumns+" FROM inventory_photos WHERE "+column+"=? ORDER BY position, id", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var photos []Photo
	for rows.Next() {
		photo, err := scanPhoto(rows)
		if err != nil {
			return nil, err
		}
		photos = append(photos, photo)
	}
	return photos, rows.Err()
}
func (r *Repository) PhotoLocation(ctx context.Context, photo Photo) (Location, error) {
	if photo.DormitoryID != "" {
		return Location{DormitoryID: photo.DormitoryID}, nil
	}
	var p Location
	err := r.db.QueryRowContext(ctx, `SELECT b.dormitory_id, b.id, f.id, r.id FROM inventory_rooms r JOIN inventory_floors f ON f.id=r.floor_id JOIN inventory_buildings b ON b.id=f.building_id WHERE r.id=?`, photo.RoomID).Scan(&p.DormitoryID, &p.BuildingID, &p.FloorID, &p.RoomID)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}
func (r *Repository) InsertPhoto(ctx context.Context, p Photo) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO inventory_photos (id,dormitory_id,room_id,storage_key,media_type,byte_size,width,height,description,position,is_primary) VALUES (?,nullif(?,''),nullif(?,''),?,?,?,?,?,?,?,?)`, p.ID, p.DormitoryID, p.RoomID, p.StorageKey, p.MediaType, p.ByteSize, p.Width, p.Height, p.Description, p.Position, p.Primary)
	return err
}
func (r *Repository) DemotePhotos(ctx context.Context, p Location) error {
	column, id, err := photoOwner(p)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, "UPDATE inventory_photos SET is_primary=0, version=version+1 WHERE "+column+"=? AND is_primary=1", id)
	return err
}
func (r *Repository) UpdatePhoto(ctx context.Context, p Photo, version int64) error {
	result, err := r.db.ExecContext(ctx, `UPDATE inventory_photos SET description=?, position=?, is_primary=?, version=version+1 WHERE id=? AND version=?`, p.Description, p.Position, p.Primary, p.ID, version)
	if err != nil {
		return err
	}
	return changed(result)
}
func (r *Repository) RemovePhoto(ctx context.Context, id string, version int64) error {
	result, err := r.db.ExecContext(ctx, "DELETE FROM inventory_photos WHERE id=? AND version=?", id, version)
	if err != nil {
		return err
	}
	return changed(result)
}
func (r *Repository) PhotoKeys(ctx context.Context) (map[string]bool, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT storage_key FROM inventory_photos")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys[key] = true
	}
	return keys, rows.Err()
}
