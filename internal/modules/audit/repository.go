package audit

import (
	"context"
	"database/sql"
	"fmt"
)

// insertEvent deliberately accepts only *sql.Tx: a mandatory event cannot be
// written through a separate database connection after the business commit.
func insertEvent(ctx context.Context, tx *sql.Tx, event Event) error {
	var scope any
	if event.DormitoryID != "" {
		scope = event.DormitoryID
	}
	changes := event.ChangesJSON
	if changes == "" {
		changes = "{}"
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO audit_events (actor_id, action, object_type, object_id, result, request_id, dormitory_id, changes_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ActorID, event.Action, event.ObjectType, event.ObjectID, event.Result, event.RequestID, scope, changes,
	)
	if err != nil {
		return fmt.Errorf("insert audit event: %w", err)
	}
	return nil
}
