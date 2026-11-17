package audit

import (
	"context"
	"database/sql"
	"fmt"

	"student_dormitory/internal/platform/database"
	"student_dormitory/internal/platform/requestid"
)

type Recorder struct {
	db *sql.DB
}

func NewRecorder(db *sql.DB) *Recorder {
	return &Recorder{db: db}
}

// RecordChange saves a business change and its mandatory audit together. The
// callback uses repositories bound to tx and returns the event only on success,
// allowing newly generated object IDs to be recorded. An invalid or failed audit
// rolls back the business change too. This method does not log event data.
// The callback must not commit/roll back tx or write through another connection.
func (r *Recorder) RecordChange(ctx context.Context, change func(context.Context, *sql.Tx) (Event, error)) error {
	if change == nil {
		return fmt.Errorf("audited change callback is required")
	}
	return database.WithinTx(ctx, r.db, func(ctx context.Context, tx *sql.Tx) error {
		event, err := change(ctx, tx)
		if err != nil {
			return err
		}
		if event.RequestID == "" {
			event.RequestID = requestid.FromContext(ctx)
		}
		if err := event.validate(); err != nil {
			return err
		}
		return insertEvent(ctx, tx, event)
	})
}
