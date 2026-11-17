package inventory

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPublicProjectionCountsFiltersAndVisibility(t *testing.T) {
	f := setup(t)
	p, _ := hierarchy(t, f)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.db.ExecContext(f.ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE inventory_rooms SET capacity=10,price_minor=9007199254740993 WHERE id=?`, p.RoomID)
	exec(`UPDATE inventory_dormitories SET name='ӘЛ-ФАРАБИ',address='АЛМАТЫ',operator='PRIVATE-OPERATOR',contacts='PRIVATE-CONTACT',use_basis='PRIVATE-BASIS'`)
	for i, status := range []string{"free", "reserved", "occupied", "maintenance", "unavailable", "free"} {
		exec(`INSERT INTO inventory_beds(id,room_id,number,status,archived) VALUES(?,?,?,?,?)`, fmt.Sprint(i), p.RoomID, fmt.Sprint(i), status, i == 5)
	}
	exec(`INSERT INTO inventory_photos(id,room_id,storage_key,media_type,byte_size,width,height,description,position) VALUES('photo',?,'PRIVATE-STORAGE','image/png',10,1,1,'Public caption',0)`, p.RoomID)
	s := NewPublicService(NewRepository(f.db))
	result, err := s.Detail(f.ctx, p.DormitoryID)
	if err != nil {
		t.Fatal(err)
	}
	d := result.Items[0]
	if d.Total != 5 || d.Free != 1 || len(d.Rooms) != 1 || d.Rooms[0].Total != 5 || d.Rooms[0].Free != 1 || !d.Accessible || d.Prices[0].Minor != 9007199254740993 || len(d.Rooms[0].Photos) != 1 {
		t.Fatalf("wrong public projection: %+v", d)
	}
	if _, err = time.Parse(time.RFC3339Nano, result.AsOf); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(result)
	for _, secret := range []string{"PRIVATE-OPERATOR", "PRIVATE-CONTACT", "PRIVATE-BASIS", "PRIVATE-STORAGE", "writer-a", "ActorID", "History", "StorageKey"} {
		if strings.Contains(string(payload), secret) {
			t.Fatalf("public model leaked %s", secret)
		}
	}
	amount := int64(9007199254740993)
	for _, filter := range []PublicFilter{
		{Name: "әл-фараби", Address: "алматы", Page: 1},
		{Min: &amount, Max: &amount, Currency: "kzt", Period: "месяц", Free: true, Accessible: true, Page: 1},
	} {
		got, err := s.List(f.ctx, filter)
		if err != nil || got.Total != 1 {
			t.Fatalf("filter failed: %+v %v", got, err)
		}
	}
	for _, filter := range []PublicFilter{{Name: "not found", Page: 1}, {Currency: "USD", Page: 1}, {Period: "сутки", Page: 1}} {
		got, err := s.List(f.ctx, filter)
		if err != nil || got.Total != 0 {
			t.Fatal("nonmatching filter included dormitory", err)
		}
	}
	for _, filter := range []PublicFilter{{Min: &amount, Page: 1}, {Max: &amount, Currency: "KZT", Page: 1}, {Page: 0}, {Page: 1000001}, {Currency: "$$$", Page: 1}} {
		if _, err := s.List(f.ctx, filter); !errors.Is(err, ErrPublicFilter) {
			t.Fatal("invalid filter accepted", err)
		}
	}
	// Changing the real registry status is visible on the next read, without a cache.
	exec(`UPDATE inventory_beds SET status='maintenance' WHERE id='0'`)
	result, err = s.Detail(f.ctx, p.DormitoryID)
	if err != nil || result.Items[0].Free != 0 || result.Items[0].Total != 5 {
		t.Fatal("stale counts", err)
	}
	got, err := s.List(f.ctx, PublicFilter{Free: true, Page: 1})
	if err != nil || got.Total != 0 {
		t.Fatal("free filter ignored live status")
	}
	// Every actual ancestor must be active, even for an inconsistent imported chain.
	for _, entry := range []struct{ table, id string }{{"inventory_rooms", p.RoomID}, {"inventory_floors", p.FloorID}, {"inventory_buildings", p.BuildingID}} {
		exec(`UPDATE `+entry.table+` SET archived=1 WHERE id=?`, entry.id)
		got, err = s.Detail(f.ctx, p.DormitoryID)
		if err != nil || got.Items[0].Total != 0 || len(got.Items[0].Rooms) != 0 || len(got.Items[0].Prices) != 0 {
			t.Fatalf("archived chain included: %s", entry.table)
		}
		exec(`UPDATE `+entry.table+` SET archived=0 WHERE id=?`, entry.id)
	}
	for _, column := range []string{"published", "archived"} {
		value := 0
		if column == "archived" {
			value = 1
		}
		exec(`UPDATE inventory_dormitories SET `+column+`=?`, value)
		if _, err = s.Detail(f.ctx, p.DormitoryID); !errors.Is(err, ErrNotFound) {
			t.Fatal("hidden dormitory detail exposed")
		}
		list, err := s.List(f.ctx, PublicFilter{Page: 1})
		if err != nil || list.Total != 0 {
			t.Fatal("hidden dormitory listed")
		}
		exec(`UPDATE inventory_dormitories SET published=1,archived=0`)
	}
}

func TestPublicPaginationAndSameRoomFilters(t *testing.T) {
	f := setup(t)
	p, _ := hierarchy(t, f)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.db.ExecContext(f.ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Accessible expensive room and inaccessible cheap room do not jointly satisfy a filter.
	exec(`UPDATE inventory_rooms SET accessible=0 WHERE id=?`, p.RoomID)
	exec(`INSERT INTO inventory_rooms(id,floor_id,number,capacity,area_hundredths,equipment,sanitary,accessible,price_minor,currency,price_period) VALUES('other',?,'102',2,1000,'desk','bath',1,99999,'KZT','месяц')`, p.FloorID)
	s := NewPublicService(NewRepository(f.db))
	max := int64(12345)
	got, err := s.List(f.ctx, PublicFilter{Accessible: true, Max: &max, Currency: "KZT", Period: "месяц", Page: 1})
	if err != nil || got.Total != 0 {
		t.Fatal("filters matched different rooms")
	}
	for i := 0; i < 25; i++ {
		exec(`INSERT INTO inventory_dormitories(id,name,address,operator,use_basis,contacts,published) VALUES(?, 'Same name','Address','private','private','private',1)`, fmt.Sprintf("page-%02d", i))
	}
	seen := map[string]bool{}
	for page := 1; page <= 3; page++ {
		got, err = s.List(f.ctx, PublicFilter{Name: "same NAME", Page: page})
		if err != nil || got.Total != 25 || got.Pages != 3 {
			t.Fatalf("page metadata %+v %v", got, err)
		}
		for _, d := range got.Items {
			if seen[d.ID] {
				t.Fatal("duplicate between pages")
			}
			seen[d.ID] = true
			if d.Total != 0 || d.Free != 0 {
				t.Fatal("invented capacity")
			}
		}
	}
	if len(seen) != 25 {
		t.Fatal("pagination lost dormitories")
	}
	got, err = s.List(f.ctx, PublicFilter{Name: "same name", Page: 100})
	if err != nil || len(got.Items) != 0 || got.Total != 25 {
		t.Fatal("out of range page incorrect")
	}
}
