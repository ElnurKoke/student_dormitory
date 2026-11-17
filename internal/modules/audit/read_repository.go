package audit

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
)

const PageSize = 50

type Filter struct {
	DormitoryID, ActorID, Action, ObjectType, ObjectID, From, To string
	Page                                                         int
}
type Change struct{ Field, Before, After string }
type Entry struct {
	ID                                                                                int64
	ActorID, OccurredAt, Action, ObjectType, ObjectID, Result, RequestID, DormitoryID string
	Changes                                                                           []Change
}
type Listing struct {
	Entries            []Entry
	Total, Page, Pages int
}

// ReadRepository exposes no write or delete operation. Auth grants are checked
// by its consumer in this same transaction; events without a scope are excluded.
type ReadRepository struct{}

func NewReadRepository() *ReadRepository { return &ReadRepository{} }
func (r *ReadRepository) List(ctx context.Context, tx *sql.Tx, ids []string, f Filter) (Listing, error) {
	result := Listing{Page: f.Page}
	if len(ids) == 0 {
		return result, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	where := ` WHERE dormitory_id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + `)`
	for _, pair := range []struct{ column, value string }{{"actor_id", f.ActorID}, {"action", f.Action}, {"object_type", f.ObjectType}, {"object_id", f.ObjectID}, {"dormitory_id", f.DormitoryID}} {
		if pair.value != "" {
			where += ` AND ` + pair.column + `=?`
			args = append(args, pair.value)
		}
	}
	if f.From != "" {
		where += ` AND occurred_at>=?`
		args = append(args, f.From)
	}
	if f.To != "" {
		where += ` AND occurred_at<?`
		args = append(args, f.To)
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM audit_events`+where, args...).Scan(&result.Total); err != nil {
		return result, err
	}
	result.Pages = (result.Total + PageSize - 1) / PageSize
	args = append(args, PageSize, (f.Page-1)*PageSize)
	rows, err := tx.QueryContext(ctx, `SELECT id,actor_id,occurred_at,action,object_type,object_id,result,request_id,dormitory_id,changes_json FROM audit_events`+where+` ORDER BY occurred_at DESC,id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var e Entry
		var changes string
		if err := rows.Scan(&e.ID, &e.ActorID, &e.OccurredAt, &e.Action, &e.ObjectType, &e.ObjectID, &e.Result, &e.RequestID, &e.DormitoryID, &changes); err != nil {
			return result, err
		}
		e.Changes = visibleChanges(changes)
		result.Entries = append(result.Entries, e)
	}
	return result, rows.Err()
}

// Unknown payload keys and technical file locations never reach the HTML.
// RawMessage preserves exact int64 amounts instead of decoding through float64.
func visibleChanges(raw string) []Change {
	var doc struct{ Before, After map[string]json.RawMessage }
	if json.Unmarshal([]byte(raw), &doc) != nil {
		return nil
	}
	allowed := []string{"name", "number", "address", "operator", "use_basis", "contacts", "description", "published", "capacity", "area_hundredths", "equipment", "sanitary", "accessible", "accessibility_notes", "price_minor", "currency", "price_period", "status", "archived", "version", "position", "primary"}
	sort.Strings(allowed)
	var changes []Change
	for _, field := range allowed {
		a, b := doc.Before[field], doc.After[field]
		if string(a) == string(b) {
			continue
		}
		// A snapshot field is a scalar. Nested objects are not a display contract.
		format := func(v json.RawMessage) string {
			v = bytes.TrimSpace(v)
			if len(v) == 0 || string(v) == "null" {
				return "—"
			}
			var s string
			if json.Unmarshal(v, &s) == nil {
				return s
			}
			if v[0] == '{' || v[0] == '[' {
				return "—"
			}
			return string(v)
		}
		changes = append(changes, Change{Field: field, Before: format(a), After: format(b)})
	}
	return changes
}
