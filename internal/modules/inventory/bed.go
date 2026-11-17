package inventory

// Reserved and occupied are stored domain states, never targets of a manual operation.
type BedStatus string

const (
	BedFree        BedStatus = "free"
	BedReserved    BedStatus = "reserved"
	BedOccupied    BedStatus = "occupied"
	BedMaintenance BedStatus = "maintenance"
	BedUnavailable BedStatus = "unavailable"
)

func (s BedStatus) Label() string {
	switch s {
	case BedFree:
		return "Свободно"
	case BedReserved:
		return "Забронировано"
	case BedOccupied:
		return "Занято"
	case BedMaintenance:
		return "На ремонте"
	case BedUnavailable:
		return "Временно недоступно"
	}
	return "—"
}

func (s BedStatus) ManualTargets() []BedStatus {
	switch s {
	case BedFree:
		return []BedStatus{BedMaintenance, BedUnavailable}
	case BedMaintenance, BedUnavailable:
		return []BedStatus{BedFree}
	}
	return nil
}

func (s BedStatus) AllowsManual(next BedStatus) bool {
	for _, target := range s.ManualTargets() {
		if target == next {
			return true
		}
	}
	return false
}

type BedChange struct {
	ID, Version                        int64
	Action, Basis, ActorID, OccurredAt string
	OldStatus, NewStatus               BedStatus
}

func (h BedChange) ActionLabel() string {
	switch h.Action {
	case "created":
		return "Создание"
	case "updated":
		return "Изменение обозначения"
	case "archived":
		return "Архивирование"
	case "restored":
		return "Восстановление"
	case "status_changed":
		return "Изменение статуса"
	}
	return "Изменение"
}
