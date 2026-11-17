package inventory

import (
	"context"
	"database/sql"
)

func (r *Repository) ActiveBedCount(ctx context.Context, roomID string) (int64, error) {
	var count int64
	err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM inventory_beds WHERE room_id = ? AND archived = 0`, roomID).Scan(&count)
	return count, err
}

func (r *Repository) SetBedStatus(ctx context.Context, old Node, next BedStatus) error {
	result, err := r.db.ExecContext(ctx, `UPDATE inventory_beds SET status = ?, version = version + 1,
        updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ? AND version = ? AND status = ? AND archived = 0`, next, old.ID, old.Version, old.Status)
	if err != nil {
		return err
	}
	return changed(result)
}

func (r *Repository) InsertBedChange(ctx context.Context, actor, action, basis string, before *Node, after Node) (int64, error) {
	var old any
	if before != nil {
		old = before.Status
	}
	result, err := r.db.ExecContext(ctx, `INSERT INTO inventory_bed_history
        (bed_id, version, action, old_status, new_status, basis, actor_id, occurred_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		after.ID, after.Version, action, old, after.Status, basis, actor, after.UpdatedAt)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (r *Repository) BedHistory(ctx context.Context, bedID string) ([]BedChange, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, version, action, old_status, new_status, basis, actor_id, occurred_at
        FROM inventory_bed_history WHERE bed_id = ? ORDER BY version DESC`, bedID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var history []BedChange
	for rows.Next() {
		var h BedChange
		var old sql.NullString
		if err := rows.Scan(&h.ID, &h.Version, &h.Action, &old, &h.NewStatus, &h.Basis, &h.ActorID, &h.OccurredAt); err != nil {
			return nil, err
		}
		h.OldStatus = BedStatus(old.String)
		history = append(history, h)
	}
	return history, rows.Err()
}
