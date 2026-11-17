package inventory

import (
	"context"
	"database/sql"
	"strings"
)

const publicRoomFrom = ` FROM inventory_rooms r JOIN inventory_floors f ON f.id=r.floor_id JOIN inventory_buildings b ON b.id=f.building_id WHERE b.dormitory_id=d.id AND b.archived=0 AND f.archived=0 AND r.archived=0`

// PublicList reads headers, prices, counts and photo metadata in one SQLite
// snapshot. SQL never selects private fields, even transiently.
func (r *Repository) PublicList(ctx context.Context, filter PublicFilter, id string) (PublicListing, error) {
	out := PublicListing{Page: filter.Page}
	tx, err := r.root.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	where := ` WHERE d.archived=0 AND d.published=1`
	args := []any{}
	if id != "" {
		where += ` AND d.id=?`
		args = append(args, id)
	}
	roomWhere := publicRoomFrom
	needsRoom := filter.Free || filter.Accessible || filter.Currency != "" || filter.Period != "" || filter.Min != nil || filter.Max != nil
	if filter.Accessible {
		roomWhere += ` AND r.accessible=1`
	}
	if filter.Free {
		roomWhere += ` AND EXISTS (SELECT 1 FROM inventory_beds p WHERE p.room_id=r.id AND p.archived=0 AND p.status='free')`
	}
	if filter.Currency != "" {
		roomWhere += ` AND r.currency=?`
		args = append(args, filter.Currency)
	}
	if filter.Period != "" {
		roomWhere += ` AND r.price_period=?`
		args = append(args, filter.Period)
	}
	if filter.Min != nil {
		roomWhere += ` AND r.price_minor>=?`
		args = append(args, *filter.Min)
	}
	if filter.Max != nil {
		roomWhere += ` AND r.price_minor<=?`
		args = append(args, *filter.Max)
	}
	if needsRoom {
		where += ` AND EXISTS (SELECT 1` + roomWhere + `)`
	}
	rows, err := tx.QueryContext(ctx, `SELECT d.id,d.name,d.address,d.description FROM inventory_dormitories d`+where+` ORDER BY d.name,d.id`, args...)
	if err != nil {
		return out, err
	}
	var candidates []PublicDormitory
	for rows.Next() {
		var d PublicDormitory
		if err = rows.Scan(&d.ID, &d.Name, &d.Address, &d.Description); err != nil {
			rows.Close()
			return out, err
		}
		// Go's Unicode case folding handles Russian and Kazakh; SQLite's built-in
		// lower/NOCASE only folds ASCII. Filter before counting and pagination.
		if strings.Contains(strings.ToLower(d.Name), strings.ToLower(filter.Name)) && strings.Contains(strings.ToLower(d.Address), strings.ToLower(filter.Address)) {
			candidates = append(candidates, d)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if id != "" && len(candidates) == 0 {
		return out, ErrNotFound
	}
	out.Total = len(candidates)
	out.Pages = (out.Total + PublicPageSize - 1) / PublicPageSize
	start := (filter.Page - 1) * PublicPageSize
	if start < len(candidates) {
		out.Items = candidates[start:min(start+PublicPageSize, len(candidates))]
	}
	for i := range out.Items {
		if err = r.publicDetails(ctx, tx, &out.Items[i], id != ""); err != nil {
			return out, err
		}
	}
	if err = tx.QueryRowContext(ctx, `SELECT strftime('%Y-%m-%dT%H:%M:%fZ','now')`).Scan(&out.AsOf); err != nil {
		return out, err
	}
	return out, tx.Commit()
}
func (r *Repository) publicDetails(ctx context.Context, tx *sql.Tx, d *PublicDormitory, detail bool) error {
	rows, err := tx.QueryContext(ctx, `SELECT r.id,r.number,b.name,f.number,r.area_hundredths,r.equipment,r.sanitary,r.accessible,r.accessibility_notes,r.price_minor,r.currency,r.price_period,
 (SELECT count(*) FROM inventory_beds p WHERE p.room_id=r.id AND p.archived=0 AND p.status='free'),
 (SELECT count(*) FROM inventory_beds p WHERE p.room_id=r.id AND p.archived=0)
 FROM inventory_dormitories d JOIN inventory_buildings b ON b.dormitory_id=d.id AND b.archived=0
 JOIN inventory_floors f ON f.building_id=b.id AND f.archived=0 JOIN inventory_rooms r ON r.floor_id=f.id AND r.archived=0
 WHERE d.id=? AND d.archived=0 AND d.published=1 ORDER BY b.name,b.id,f.number,f.id,r.number,r.id`, d.ID)
	if err != nil {
		return err
	}
	var roomIDs []string
	for rows.Next() {
		var room PublicRoom
		var roomID string
		if err = rows.Scan(&roomID, &room.Number, &room.Building, &room.Floor, &room.AreaHundredths, &room.Equipment, &room.Sanitary, &room.Accessible, &room.AccessibilityNotes, &room.Price.Minor, &room.Price.Currency, &room.Price.Period, &room.Free, &room.Total); err != nil {
			rows.Close()
			return err
		}
		d.Free += room.Free
		d.Total += room.Total
		d.Accessible = d.Accessible || room.Accessible
		found := false
		for _, p := range d.Prices {
			if p == room.Price {
				found = true
				break
			}
		}
		if !found {
			d.Prices = append(d.Prices, room.Price)
		}
		if detail {
			d.Rooms = append(d.Rooms, room)
			roomIDs = append(roomIDs, roomID)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	d.Photos, err = publicPhotos(ctx, tx, "dormitory_id", d.ID)
	if err != nil {
		return err
	}
	for i, roomID := range roomIDs {
		d.Rooms[i].Photos, err = publicPhotos(ctx, tx, "room_id", roomID)
		if err != nil {
			return err
		}
	}
	return nil
}
func publicPhotos(ctx context.Context, tx *sql.Tx, column, id string) ([]PublicPhoto, error) {
	// column is an internal constant, never request input.
	rows, err := tx.QueryContext(ctx, `SELECT id,description,width,height FROM inventory_photos WHERE `+column+`=? ORDER BY is_primary DESC,position,id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var photos []PublicPhoto
	for rows.Next() {
		var p PublicPhoto
		if err = rows.Scan(&p.ID, &p.Description, &p.Width, &p.Height); err != nil {
			return nil, err
		}
		photos = append(photos, p)
	}
	return photos, rows.Err()
}
