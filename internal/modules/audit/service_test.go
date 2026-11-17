package audit

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"student_dormitory/internal/platform/database"
	"student_dormitory/internal/platform/requestid"
)

func testDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// Test-only business fixture, not a new product entity or migration.
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE test_records (
		id TEXT PRIMARY KEY,
		parent_id TEXT REFERENCES test_records(id) DEFERRABLE INITIALLY DEFERRED
	)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestChangeAndAuditCommitTogether(t *testing.T) {
	db := testDatabase(t)
	ctx := requestid.WithID(context.Background(), requestid.New())
	// SQL-looking data must remain data in both the change and its audit.
	objectID := "record'); DROP TABLE audit_events; --"
	event := Event{ActorID: "staff:1", Action: "record.created", ObjectType: "test_record", ObjectID: objectID, Result: "success"}
	err := NewRecorder(db).RecordChange(ctx, func(ctx context.Context, tx *sql.Tx) (Event, error) {
		_, err := tx.ExecContext(ctx, "INSERT INTO test_records (id) VALUES (?)", objectID)
		return event, err
	})
	if err != nil {
		t.Fatal(err)
	}
	var stored Event
	var occurredAt string
	err = db.QueryRowContext(ctx, `SELECT actor_id, action, object_type, object_id, result, request_id, occurred_at FROM audit_events`).Scan(
		&stored.ActorID, &stored.Action, &stored.ObjectType, &stored.ObjectID, &stored.Result, &stored.RequestID, &occurredAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	event.RequestID = requestid.FromContext(ctx)
	if stored != event {
		t.Fatalf("event mismatch: %+v", stored)
	}
	timestamp, err := time.Parse(time.RFC3339Nano, occurredAt)
	if err != nil || timestamp.Location() != time.UTC || time.Since(timestamp) > time.Minute {
		t.Fatalf("bad audit timestamp: %s, %v", occurredAt, err)
	}
	var id string
	if err := db.QueryRowContext(ctx, "SELECT id FROM test_records").Scan(&id); err != nil || id != objectID {
		t.Fatalf("business record: %s, %v", id, err)
	}
}

func TestChangeAndAuditRollbackTogether(t *testing.T) {
	for _, failure := range []string{"business", "audit_validation", "audit_insert", "commit", "cancel", "panic"} {
		t.Run(failure, func(t *testing.T) {
			db := testDatabase(t)
			ctx, cancel := context.WithCancel(requestid.WithID(context.Background(), requestid.New()))
			defer cancel()
			if failure == "audit_insert" {
				if _, err := db.ExecContext(ctx, `CREATE TRIGGER reject_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`); err != nil {
					t.Fatal(err)
				}
			}
			businessErr := errors.New("business change failed")
			var err error
			panicked := false
			func() {
				defer func() {
					if p := recover(); p != nil {
						if p != "rollback me" {
							panic(p)
						}
						panicked = true
					}
				}()
				err = NewRecorder(db).RecordChange(ctx, func(ctx context.Context, tx *sql.Tx) (Event, error) {
					if _, err := tx.ExecContext(ctx, "INSERT INTO test_records (id) VALUES (?)", "record:1"); err != nil {
						return Event{}, err
					}
					event := Event{ActorID: "staff:1", Action: "record.created", ObjectType: "test_record", ObjectID: "record:1", Result: "success"}
					switch failure {
					case "business":
						return Event{}, businessErr
					case "audit_validation":
						event.ActorID = ""
					case "commit":
						_, err := tx.ExecContext(ctx, "UPDATE test_records SET parent_id = ? WHERE id = ?", "missing", "record:1")
						if err != nil {
							return Event{}, err
						}
					case "cancel":
						cancel()
					case "panic":
						panic("rollback me")
					}
					return event, nil
				})
			}()
			if failure == "panic" {
				if !panicked {
					t.Fatal("panic was swallowed")
				}
			} else if err == nil {
				t.Fatal("failed operation returned success")
			}
			if failure == "business" && !errors.Is(err, businessErr) {
				t.Fatalf("original error lost: %v", err)
			}
			if failure == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			// Use a live context after testing cancellation and verify BOTH sides.
			for _, table := range []string{"test_records", "audit_events"} {
				var count int
				if err := db.QueryRowContext(context.Background(), "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("%s survived rollback: %d, %v", table, count, err)
				}
			}
		})
	}
}

func TestMissingRequestIDRollsBack(t *testing.T) {
	db := testDatabase(t)
	err := NewRecorder(db).RecordChange(context.Background(), func(ctx context.Context, tx *sql.Tx) (Event, error) {
		_, err := tx.ExecContext(ctx, "INSERT INTO test_records (id) VALUES (?)", "record:1")
		return Event{ActorID: "staff:1", Action: "record.created", ObjectType: "test_record", ObjectID: "record:1", Result: "success"}, err
	})
	if err == nil {
		t.Fatal("missing operation ID accepted")
	}
	var count int
	if err := db.QueryRowContext(context.Background(), "SELECT count(*) FROM test_records").Scan(&count); err != nil || count != 0 {
		t.Fatalf("unaudited change committed: %d, %v", count, err)
	}
}
