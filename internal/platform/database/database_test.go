package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/mattn/go-sqlite3"
	"student_dormitory/migrations"
)

func TestMigrations(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nested", "test.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count, foreignKeys int
	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != len(names) {
		t.Fatalf("migration rerun: %d, %v", count, err)
	}
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("foreign keys: %d, %v", foreignKeys, err)
	}
	source := fstest.MapFS{}
	for _, name := range names {
		body, err := migrations.FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		source[name] = &fstest.MapFile{Data: body}
	}
	nextName := fmt.Sprintf("%06d_example.sql", len(names)+1)
	source[nextName] = &fstest.MapFile{Data: []byte("CREATE TABLE example (id INTEGER PRIMARY KEY); INSERT INTO no_such_table VALUES (1);")}
	if err := Migrate(ctx, db, source); err == nil {
		t.Fatal("invalid migration succeeded")
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name = 'example'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("DDL was not rolled back: %d, %v", count, err)
	}
	source[nextName].Data = []byte("CREATE TABLE example (id INTEGER PRIMARY KEY);")
	if err := Migrate(ctx, db, source); err != nil {
		t.Fatal(err)
	}
	source[nextName].Data = []byte("CREATE TABLE changed (id INTEGER PRIMARY KEY);")
	if err := Migrate(ctx, db, source); err == nil {
		t.Fatal("edited migration accepted")
	}
	// An edited bootstrap must never run before history validation.
	source["000001_schema_migrations.sql"].Data = append([]byte("DROP TABLE schema_migrations;"), source["000001_schema_migrations.sql"].Data...)
	if err := Migrate(ctx, db, source); err == nil {
		t.Fatal("edited bootstrap accepted")
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != len(names)+1 {
		t.Fatalf("bootstrap damaged migration history: %d, %v", count, err)
	}
	if err := Migrate(ctx, db, migrations.FS); err == nil {
		t.Fatal("older binary accepted newer history")
	}
}

func TestConnectionSettingsAndForeignKeys(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "connections.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if db.Stats().MaxOpenConnections != maxOpenConns {
		t.Fatal("pool limit not configured")
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE fk_parents (id INTEGER PRIMARY KEY);
		CREATE TABLE fk_children (id INTEGER PRIMARY KEY, parent_id INTEGER NOT NULL REFERENCES fk_parents(id));
		INSERT INTO fk_parents (id) VALUES (1);
		INSERT INTO fk_children (id, parent_id) VALUES (1, 1);
	`); err != nil {
		t.Fatal(err)
	}
	// Pin two connections at once, then discard them and test their replacements.
	// This proves the DSN configures each physical connection, not only startup.
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(0)
	for generation := 0; generation < 2; generation++ {
		var connections []*sql.Conn
		for i := 0; i < 2; i++ {
			conn, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			connections = append(connections, conn)
			t.Cleanup(func() { conn.Close() })
			var fk, timeout, synchronous int
			var journal string
			if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
				t.Fatalf("foreign_keys: %d, %v", fk, err)
			}
			if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil || journal != "wal" {
				t.Fatalf("journal_mode: %s, %v", journal, err)
			}
			if err := conn.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil || synchronous != 2 {
				t.Fatalf("synchronous: %d, %v", synchronous, err)
			}
			if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil || timeout != int(busyTimeout.Milliseconds()) {
				t.Fatalf("busy_timeout: %d, %v", timeout, err)
			}
			for _, statement := range []string{
				"INSERT INTO fk_children (id, parent_id) VALUES (2, 999)",
				"UPDATE fk_children SET parent_id = 999 WHERE id = 1",
				"DELETE FROM fk_parents WHERE id = 1",
			} {
				_, err := conn.ExecContext(ctx, statement)
				var sqliteErr sqlite3.Error
				if !errors.As(err, &sqliteErr) || sqliteErr.ExtendedCode != sqlite3.ErrConstraintForeignKey {
					t.Fatalf("expected FK violation, got %v", err)
				}
			}
		}
		for _, conn := range connections {
			// Explicitly discard one connection too, as database/sql does on failure.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			conn.Close()
		}
	}
}

func TestBusyDatabaseHasBoundedWait(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "busy.db")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	lock, err := first.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	start := time.Now()
	blocked, err := second.BeginTx(ctx, nil)
	if blocked != nil {
		defer blocked.Rollback()
	}
	var sqliteErr sqlite3.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code != sqlite3.ErrBusy {
		t.Fatalf("expected busy error: %v", err)
	}
	if elapsed := time.Since(start); elapsed < busyTimeout/2 || elapsed > 3*busyTimeout {
		t.Fatalf("unexpected busy wait: %s", elapsed)
	}
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	// The failed attempt must not leak its connection or transaction.
	if err := WithinTx(ctx, second, func(context.Context, *sql.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestUpgradeExistingDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "existing.db")
	previous, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer previous.Close()
	bootstrap, err := migrations.FS.ReadFile("000001_schema_migrations.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, previous, fstest.MapFS{"000001_schema_migrations.sql": {Data: bootstrap}}); err != nil {
		t.Fatal(err)
	}
	if _, err := previous.ExecContext(ctx, "CREATE TABLE previous_data (value TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := previous.ExecContext(ctx, "INSERT INTO previous_data (value) VALUES (?)", "preserve me"); err != nil {
		t.Fatal(err)
	}
	if err := previous.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value string
	if err := db.QueryRowContext(ctx, "SELECT value FROM previous_data").Scan(&value); err != nil || value != "preserve me" {
		t.Fatalf("previous data lost: %s, %v", value, err)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM audit_events").Scan(&count); err != nil || count != 0 {
		t.Fatalf("audit migration missing: %d, %v", count, err)
	}
}

func TestUpgradeInventoryRoomsToBeds(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "rooms.db"))+"?_foreign_keys=on&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	old := fstest.MapFS{}
	for _, name := range []string{"000001_schema_migrations.sql", "000002_audit_events.sql", "000003_auth.sql", "000004_inventory.sql"} {
		data, err := migrations.FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		old[name] = &fstest.MapFile{Data: data}
	}
	if err := Migrate(ctx, db, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO inventory_dormitories (id,name,address,operator,use_basis,contacts) VALUES ('d','Dorm','Address','Operator','Basis','Contacts');
        INSERT INTO inventory_buildings (id,dormitory_id,name) VALUES ('b','d','Building');
        INSERT INTO inventory_floors (id,building_id,number) VALUES ('f','b','1');
        INSERT INTO inventory_rooms (id,floor_id,number,capacity,area_hundredths,equipment,sanitary,accessible,price_minor,currency,price_period)
        VALUES ('r','f','101',2,1850,'Table','Shower',1,12345,'KZT','month');`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var number string
	var capacity, price, version int64
	if err := db.QueryRowContext(ctx, "SELECT number,capacity,price_minor,version FROM inventory_rooms WHERE id = 'r'").Scan(&number, &capacity, &price, &version); err != nil {
		t.Fatal(err)
	}
	if number != "101" || capacity != 2 || price != 12345 || version != 1 {
		t.Fatal("upgrade altered existing room")
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO inventory_beds (id,room_id,number) VALUES ('bed','r','A')"); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.QueryRowContext(ctx, "SELECT status FROM inventory_beds WHERE id = 'bed'").Scan(&status); err != nil || status != "free" {
		t.Fatal("bed schema unavailable after upgrade")
	}
}
