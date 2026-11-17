package reports

import (
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"strings"
	"testing"

	"student_dormitory/internal/modules/inventory"
)

func TestSpreadsheetCellsCannotStartFormulas(t *testing.T) {
	for _, value := range []string{"=1+1", "+cmd", "-1+1", "@SUM(1)", "   =1", "\t=1", "\r\n=1", "\ufeff=1", "\u200b+1", "\u00a0@SUM(1)", "＝1", "＋1", "－1", "＠x"} {
		if got := SpreadsheetCell(value); got != "'"+value {
			t.Errorf("unsafe cell %q", value)
		}
	}
	for _, value := range []string{"Русский, қазақша", "9007199254740993", "Көше 1", "'@already-safe", "a=b", "room-1", ""} {
		if got := SpreadsheetCell(value); got != value {
			t.Errorf("ordinary value changed %q", value)
		}
	}
}
func TestCSVUnicodeEscapingAndExactNumbers(t *testing.T) {
	row := inventory.RegistryRecord{Node: inventory.Node{ID: "room", Kind: inventory.Room, Fields: inventory.Fields{Number: "=1+1", Equipment: "Стол, шкаф; қазақша «ү»", Sanitary: "Душ \"А\"", PriceMinor: 9007199254740993, Currency: "KZT", PricePeriod: "месяц"}, Version: 2}, DormitoryName: "Алатау, Жатақхана", Address: " ", Location: inventory.Location{DormitoryID: "dorm"}}
	var out bytes.Buffer
	if err := WriteRegistryCSV(&out, []inventory.RegistryRecord{row}); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(out.Bytes(), []byte{0xef, 0xbb, 0xbf}) || !strings.Contains(out.String(), "\r\n") {
		t.Fatal("CSV encoding missing BOM or CRLF")
	}
	records, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(out.String(), "\ufeff"))).ReadAll()
	if err != nil || len(records) != 2 {
		t.Fatal(err)
	}
	if len(records[0]) != 34 || len(records[1]) != 34 {
		t.Fatal("unequal CSV columns")
	}
	fields := map[string]string{}
	for i, key := range records[0] {
		fields[key] = records[1][i]
	}
	if fields["label"] != "'=1+1" || fields["equipment"] != row.Node.Equipment || fields["sanitary"] != row.Node.Sanitary || fields["price_minor"] != "9007199254740993" || fields["dormitory_name"] != row.DormitoryName {
		t.Fatalf("CSV lost text or exact numbers: %v", fields)
	}
	if err := WriteRegistryCSV(failingWriter{}, nil); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal("write error lost")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
