package reports

import (
	"encoding/csv"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"student_dormitory/internal/modules/inventory"
)

// SpreadsheetCell forces potentially executable cells to text, including a
// formula hidden behind whitespace, BOM, zero-width or other control prefixes.
// CSV quoting alone does not prevent formula evaluation in spreadsheet apps.
func SpreadsheetCell(value string) string {
	probe := strings.TrimLeftFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) })
	if strings.ContainsAny(value, "\t\r\n") || strings.HasPrefix(probe, "=") || strings.HasPrefix(probe, "+") || strings.HasPrefix(probe, "-") || strings.HasPrefix(probe, "@") || strings.HasPrefix(probe, "＝") || strings.HasPrefix(probe, "＋") || strings.HasPrefix(probe, "－") || strings.HasPrefix(probe, "＠") {
		return "'" + value
	}
	return value
}

// WriteRegistryCSV exports one row per actual registry object, never one row per
// joined photo/history record. Stable ASCII headers and codes are locale-neutral.
func WriteRegistryCSV(output io.Writer, records []inventory.RegistryRecord) error {
	if _, err := io.WriteString(output, "\xef\xbb\xbf"); err != nil {
		return err
	}
	w := csv.NewWriter(output)
	w.UseCRLF = true
	header := []string{"kind", "id", "parent_id", "dormitory_id", "dormitory_name", "address", "building_id", "building_name", "floor_id", "floor_number", "room_id", "room_number", "label", "own_archived", "effective_archived", "published", "operator", "use_basis", "contacts", "description", "capacity", "area_hundredths", "equipment", "sanitary", "accessible", "accessibility_notes", "price_minor", "currency", "price_period", "bed_status", "photo_count", "incomplete_fields", "version", "updated_at"}
	if err := w.Write(header); err != nil {
		return err
	}
	for _, row := range records {
		n := row.Node
		number := func(v int64) string { return strconv.FormatInt(v, 10) }
		boolText := strconv.FormatBool
		fields := []string{}
		for key := range row.Errors {
			fields = append(fields, key)
		}
		sort.Strings(fields)
		values := []string{string(n.Kind), n.ID, n.ParentID, n.DormitoryID, row.DormitoryName, row.Address, row.Location.BuildingID, row.BuildingName, row.Location.FloorID, row.FloorNumber, row.Location.RoomID, row.RoomNumber, n.Label(), boolText(n.Archived), boolText(row.EffectiveArchived), "", n.Operator, n.UseBasis, n.Contacts, n.Description, "", "", n.Equipment, n.Sanitary, "", n.AccessibilityNotes, "", n.Currency, n.PricePeriod, string(n.Status), "", strings.Join(fields, ";"), number(n.Version), n.UpdatedAt}
		if n.Kind == inventory.Dormitory {
			values[15] = boolText(n.Published)
		}
		if n.Kind == inventory.Room {
			values[20] = number(n.Capacity)
			values[21] = number(n.AreaHundredths)
			values[24] = boolText(n.Accessible)
			values[26] = number(n.PriceMinor)
		}
		if row.PhotoApplicable() {
			values[30] = number(row.PhotoCount)
		}
		for i, value := range values {
			values[i] = SpreadsheetCell(value)
		}
		if err := w.Write(values); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}
