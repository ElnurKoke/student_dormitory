// Package audit records minimal business events without document or profile data.
package audit

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Event contains identifiers and short action/result codes, never credentials,
// document bodies, profiles or arbitrary request data. occurred_at is set by SQLite
// in UTC when the event is inserted. ActorID must come from a trusted identity.
type Event struct {
	ActorID     string
	Action      string
	ObjectType  string
	ObjectID    string
	Result      string
	RequestID   string
	DormitoryID string
	// ChangesJSON is a server-built, allowlisted business diff, never a raw form,
	// credential, document or personal profile. It is not a technical log field.
	ChangesJSON string
}

func (e Event) validate() error {
	if e.ChangesJSON != "" && !json.Valid([]byte(e.ChangesJSON)) {
		return fmt.Errorf("audit changes must be valid JSON")
	}
	for _, field := range []struct{ name, value string }{
		{"actor_id", e.ActorID}, {"action", e.Action},
		{"object_type", e.ObjectType}, {"object_id", e.ObjectID},
		{"result", e.Result}, {"request_id", e.RequestID},
	} {
		if strings.TrimSpace(field.value) == "" {
			// Do not include invalid values in errors; they may contain secrets.
			return fmt.Errorf("audit %s is required", field.name)
		}
	}
	return nil
}
