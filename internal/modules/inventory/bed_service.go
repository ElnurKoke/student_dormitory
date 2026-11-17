package inventory

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"student_dormitory/internal/modules/audit"
)

func validateBasis(basis string) error {
	if n := utf8.RuneCountInString(strings.TrimSpace(basis)); n == 0 || n > 1000 {
		return ValidationError{"basis": "Укажите основание изменения: от 1 до 1000 символов."}
	}
	return nil
}

func checkBedCapacity(ctx context.Context, repo *Repository, room Node) error {
	count, err := repo.ActiveBedCount(ctx, room.ID)
	if err != nil {
		return err
	}
	if count >= room.Capacity {
		return ErrCapacity
	}
	return nil
}

// All bed changes share a version, including designation, archive and status.
// Basis stays in the closed business history; audit contains only its row ID.
func recordInventoryChange(ctx context.Context, repo *Repository, actor, action, basis string, before *Node, after Node) (audit.Event, error) {
	e, err := event(actor, action, before, after)
	if err != nil || after.Kind != Bed {
		return e, err
	}
	historyID, err := repo.InsertBedChange(ctx, actor, action, strings.TrimSpace(basis), before, after)
	if err != nil {
		return audit.Event{}, err
	}
	var old any
	if before != nil {
		old = snapshot(*before)
	}
	data, err := json.Marshal(map[string]any{"before": old, "after": snapshot(after), "history_id": historyID})
	e.ChangesJSON = string(data)
	return e, err
}

func (s *Service) ChangeBedStatus(ctx context.Context, actor string, p Location, version int64, next BedStatus, basis string) (Node, error) {
	for _, permission := range []string{"registry.read", "places.status.write"} {
		if err := s.permissions.Authorize(ctx, actor, permission, p.DormitoryID); err != nil {
			return Node{}, err
		}
	}
	if p.Kind() != Bed {
		return Node{}, ErrNotFound
	}
	if err := validateBasis(basis); err != nil {
		return Node{}, err
	}
	var saved Node
	err := s.audit.RecordChange(ctx, func(ctx context.Context, tx *sql.Tx) (audit.Event, error) {
		for _, permission := range []string{"registry.read", "places.status.write"} {
			if err := s.permissions.AuthorizeTx(ctx, tx, actor, permission, p.DormitoryID); err != nil {
				return audit.Event{}, err
			}
		}
		repo := s.repo.WithTx(tx)
		chain, err := loadChain(ctx, repo, p)
		if err != nil {
			return audit.Event{}, err
		}
		if err := active(chain); err != nil {
			return audit.Event{}, err
		}
		old := chain[len(chain)-1]
		if old.Version != version {
			return audit.Event{}, ErrConflict
		}
		if !old.Status.AllowsManual(next) {
			return audit.Event{}, ValidationError{"status": "Такой ручной переход статуса запрещён. Бронирование и заселение выполняются отдельными процессами."}
		}
		if err := repo.SetBedStatus(ctx, old, next); err != nil {
			return audit.Event{}, err
		}
		saved, err = repo.Get(ctx, Bed, old.ID)
		if err != nil {
			return audit.Event{}, err
		}
		return recordInventoryChange(ctx, repo, actor, "status_changed", basis, &old, saved)
	})
	return saved, err
}
