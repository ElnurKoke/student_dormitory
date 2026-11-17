package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"student_dormitory/internal/modules/audit"
	"student_dormitory/internal/modules/auth"
	"student_dormitory/internal/modules/inventory"
	"student_dormitory/internal/modules/reports"
	"student_dormitory/internal/platform/backup"
	"student_dormitory/internal/platform/config"
	"student_dormitory/internal/platform/database"
	"student_dormitory/internal/platform/maintenance"
	"student_dormitory/internal/platform/requestid"
	"student_dormitory/web"
)

func acceptanceConfig(dir string) config.Config {
	return config.Config{DatabasePath: filepath.Join(dir, "dormitory.db"), FilesDir: filepath.Join(dir, "files"), DevUsersFile: filepath.Join(dir, "dev-users.txt"), AppEnv: "development", SessionTTL: time.Hour}
}
func registryFingerprint(t *testing.T, a *App) string {
	t.Helper()
	nodes, err := inventory.NewRepository(a.db).ReportNodes(context.Background(), []string{"dev-dorm-1"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(nodes)
	if err != nil {
		t.Fatal(err)
	}

	hash := sha256.New()
	hash.Write(body)
	for _, query := range []string{
		`SELECT id,actor_id,occurred_at,action,object_type,object_id,result,request_id,coalesce(dormitory_id,''),changes_json FROM audit_events ORDER BY id`,
		`SELECT id,bed_id,version,action,coalesce(old_status,''),new_status,basis,actor_id,occurred_at FROM inventory_bed_history ORDER BY id`,
		`SELECT id,coalesce(dormitory_id,''),coalesce(room_id,''),storage_key,media_type,byte_size,width,height,description,position,is_primary,version,created_at FROM inventory_photos ORDER BY id`,
	} {
		rows, err := a.db.QueryContext(context.Background(), query)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]string, len(columns))
			targets := make([]any, len(columns))
			for i := range values {
				targets[i] = &values[i]
			}
			if err = rows.Scan(targets...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			if err = json.NewEncoder(hash).Encode(values); err != nil {
				rows.Close()
				t.Fatal(err)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}
func TestMVPFullLifecycleAndRecovery(t *testing.T) {
	ctx := requestid.WithID(context.Background(), requestid.New())
	dir := t.TempDir()
	cfg := acceptanceConfig(dir)
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	a, err := New(ctx, cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	for _, table := range []string{"auth_users", "inventory_dormitories", "inventory_beds", "inventory_photos"} {
		var count int
		if err = a.db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatal("clean startup seeded business/test data", table)
		}
	}
	if w := perform(a.Handler(), "GET", "/catalog", nil, nil, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "Общежития не найдены") {
		t.Fatal("empty registry is not a valid public state")
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.DevLoginEnabled = true
	a, err = New(ctx, cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := a.SeedDemo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	credentials := readDevelopmentCredentials(t, cfg.DevUsersFile)
	fingerprint := registryFingerprint(t, a)
	if _, err = a.SeedDemo(ctx); err == nil || registryFingerprint(t, a) != fingerprint {
		t.Fatal("repeat seed overwrote or duplicated data")
	}
	permissions := auth.NewService(auth.NewRepository(a.db), audit.NewRecorder(a.db), auth.Options{DevelopmentLogin: true, SessionTTL: cfg.SessionTTL})
	reporting := reports.NewService(a.db, permissions, inventory.NewRepository(a.db), audit.NewReadRepository())
	stats, err := reporting.Statistics(ctx, credentials["administration"].UserID, reports.Filter{State: "active", Page: 1})
	if err != nil || stats.DormitoriesCount != manifest.ActiveDormitories || stats.Rooms != manifest.ActiveRooms || stats.Places != manifest.ActivePlaces || stats.Free != manifest.Free || stats.Maintenance != manifest.Maintenance || stats.Unavailable != manifest.Unavailable || stats.WithoutPhotos != 1 || stats.Incomplete != 0 {
		t.Fatalf("demo counts differ from documented scenarios: %+v %v", stats, err)
	}
	var liveRoom, freeBed inventory.Location
	repo := inventory.NewRepository(a.db)
	for _, location := range manifest.Locations {
		n, err := repo.Get(ctx, location.Kind(), location.ID())
		if err != nil {
			t.Fatal(err)
		}
		if location.Kind() == inventory.Room && n.Number == "101" {
			liveRoom = location
		}
		if location.Kind() == inventory.Bed && n.Status == inventory.BedFree && !n.Archived && location.RoomID == liveRoom.RoomID {
			freeBed = location
		}
	}
	service := inventory.NewService(repo, permissions, audit.NewRecorder(a.db))
	if _, err = service.Save(ctx, credentials["registry_staff"].UserID, liveRoom, inventory.Bed, inventory.Draft{Number: "overflow", Basis: "Test capacity"}, true, 0); !errors.Is(err, inventory.ErrCapacity) {
		t.Fatal("full room accepted extra bed", err)
	}
	if registryFingerprint(t, a) != fingerprint {
		t.Fatal("rejected change altered registry/audit")
	}
	if err = database.CheckIntegrity(ctx, a.db); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "backup")
	if _, err = backup.Create(ctx, cfg.DatabasePath, cfg.FilesDir, bundle, inventory.BackupReferences); !errors.Is(err, maintenance.ErrBusy) {
		t.Fatal("backup did not exclude running app", err)
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	// Restart before backup checks that normal shutdown/startup preserves IDs,
	// versions, all metadata and audit rather than merely row counts.
	a, err = New(ctx, cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	if registryFingerprint(t, a) != fingerprint {
		t.Fatal("restart changed IDs, fields, versions or audit")
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := backup.Create(ctx, cfg.DatabasePath, cfg.FilesDir, bundle, inventory.BackupReferences)
	if err != nil || len(snapshot.Assets) != 3 {
		t.Fatal("full photo set not backed up", err)
	}
	restoreDir := filepath.Join(t.TempDir(), "restore")
	if _, err = backup.Restore(ctx, bundle, restoreDir, inventory.BackupReferences); err != nil {
		t.Fatal(err)
	}
	restoredConfig := acceptanceConfig(restoreDir)
	restoredConfig.DevLoginEnabled = true
	// Individual passwords are intentionally not part of the photo/DB bundle;
	// existing accounts must work with their original credential file.
	restoredConfig.DevUsersFile = cfg.DevUsersFile
	a, err = New(ctx, restoredConfig, log)
	if err != nil {
		t.Fatal(err)
	}
	if registryFingerprint(t, a) != fingerprint {
		t.Fatal("restore changed permanent identities or audit")
	}
	restoredPermissions := auth.NewService(auth.NewRepository(a.db), audit.NewRecorder(a.db), auth.Options{DevelopmentLogin: true, SessionTTL: cfg.SessionTTL})
	fixture := &authFixture{app: a, service: restoredPermissions, credentials: credentials, logs: &logs}
	writer, _ := login(t, fixture, "registry_staff")
	commandant, _ := login(t, fixture, "commandant")
	admin, _ := login(t, fixture, "administration")
	for _, role := range []string{"student", "system_admin"} {
		cookie, _ := login(t, fixture, role)
		for _, path := range []string{"/staff/dormitories", "/staff/registry.csv", "/staff/photos/" + manifest.PhotoIDs[0], "/staff/statistics", "/staff/history"} {
			if w := perform(a.Handler(), "GET", path, cookie, nil, ""); w.Code != 403 {
				t.Fatalf("restored role %s can access %s (%d)", role, path, w.Code)
			}
		}
	}
	for i, id := range manifest.PhotoIDs {
		want := 200
		if i == 2 {
			want = 404
		}
		if w := perform(a.Handler(), "GET", "/photos/"+id, nil, nil, ""); w.Code != want {
			t.Fatal("restored photo publication incorrect", i, w.Code)
		}
		if w := perform(a.Handler(), "GET", "/staff/photos/"+id, writer, nil, ""); w.Code != 200 {
			t.Fatal("restored private photo missing")
		}
	}
	public := perform(a.Handler(), "GET", "/catalog/dev-dorm-1", admin, nil, "")
	for _, secret := range []string{credentials["administration"].UserID, "Демо-оператор", "demo@example.invalid", "DEMO: учебное изменение"} {
		if strings.Contains(public.Body.String(), secret) {
			t.Fatal("restored public HTML contains private data", secret)
		}
	}
	// CSRF remains mandatory after restoring sessions/users. A rejected request
	// cannot mutate the registry, and an accepted manual transition updates the
	// public availability on the following read.
	bedPath := "/staff/dormitories/" + freeBed.DormitoryID + "/buildings/" + freeBed.BuildingID + "/floors/" + freeBed.FloorID + "/rooms/" + freeBed.RoomID + "/beds/" + freeBed.BedID
	form := perform(a.Handler(), "GET", bedPath+"/status", commandant, nil, "")
	values := url.Values{"version": {version(t, form.Body.String())}, "status": {"maintenance"}, "basis": {"<script>private acceptance basis</script>"}}
	if w := perform(a.Handler(), "POST", bedPath+"/status", commandant, values, ""); w.Code != 403 {
		t.Fatal("restored CSRF bypass")
	}
	values.Set("csrf_token", csrf(t, form))
	if w := perform(a.Handler(), "POST", bedPath+"/status", commandant, values, ""); w.Code != 303 {
		t.Fatal("restored status transition failed", w.Code)
	}
	catalog, err := inventory.NewPublicService(inventory.NewRepository(a.db)).Detail(ctx, "dev-dorm-1")
	if err != nil || catalog.Items[0].Free != 2 || catalog.Items[0].Total != 5 {
		t.Fatal("public availability stale after restoration", err)
	}
	detail := perform(a.Handler(), "GET", bedPath, writer, nil, "").Body.String()
	if strings.Contains(detail, "<script>private acceptance basis") || !strings.Contains(detail, "&lt;script&gt;private acceptance basis&lt;/script&gt;") {
		t.Fatal("restored history HTML not escaped")
	}
	if err = database.CheckIntegrity(ctx, a.db); err != nil {
		t.Fatal(err)
	}
	// Optional deterministic visual fixtures: generated through the real handler,
	// with test-only records and dead CSRF tokens. No credentials/DB are exported.
	if output := os.Getenv("DORM_VISUAL_OUT"); output != "" {
		if err = os.Mkdir(output, 0700); err != nil {
			t.Fatal(err)
		}
		css, err := fs.ReadFile(web.FS, "static/app.css")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(output, "app.css"), css, 0600); err != nil {
			t.Fatal(err)
		}
		cases := []struct {
			name, path string
			role       string
		}{{"catalog", "/catalog", ""}, {"catalog-detail", "/catalog/dev-dorm-1", ""}, {"statistics", "/staff/statistics", "admin"}, {"history", "/staff/history", "admin"}, {"export", "/staff/registry-export", "writer"}, {"inventory", "/staff/dormitories/dev-dorm-1", "writer"}}
		for _, item := range cases {
			for _, language := range []string{"ru", "kk"} {
				cookie := writer
				if item.role == "admin" {
					cookie = admin
				}
				if item.role == "" {
					cookie = nil
				}
				w := perform(a.Handler(), "GET", item.path+"?lang="+language, cookie, nil, "")
				if w.Code != 200 {
					t.Fatal("visual fixture HTTP error")
				}
				body := strings.ReplaceAll(w.Body.String(), `href="/static/app.css"`, `href="app.css"`)
				body = regexp.MustCompile(`name="csrf_token" value="[^"]*"`).ReplaceAllString(body, `name="csrf_token" value="visual-only"`)
				// Images are independently covered through real endpoints above; copy only
				// those authorized bytes into this private visual fixture directory.
				for _, id := range manifest.PhotoIDs {
					path := "/staff/photos/" + id
					if item.role == "" {
						path = "/photos/" + id
					}
					image := perform(a.Handler(), "GET", path, cookie, nil, "")
					if image.Code == 200 {
						if err = os.WriteFile(filepath.Join(output, id+".png"), image.Body.Bytes(), 0600); err != nil {
							t.Fatal(err)
						}
						body = strings.ReplaceAll(body, path, id+".png")
					}
				}
				if err = os.WriteFile(filepath.Join(output, item.name+"-"+language+".html"), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	for _, credential := range credentials {
		if strings.Contains(logs.String(), credential.Password) {
			t.Fatal("credential appeared in technical logs")
		}
	}
}

func TestApplicationExcludesSharedDataAndProductionDemo(t *testing.T) {
	dir := t.TempDir()
	cfg := acceptanceConfig(dir)
	a, err := New(context.Background(), cfg, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, second := range []config.Config{cfg, func() config.Config { c := cfg; c.FilesDir = filepath.Join(dir, "other-files"); return c }(), func() config.Config { c := cfg; c.DatabasePath = filepath.Join(dir, "other.db"); return c }()} {
		if other, err := New(context.Background(), second, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))); !errors.Is(err, maintenance.ErrBusy) {
			if other != nil {
				other.Close()
			}
			t.Fatal("shared-data server exclusion failed", err)
		}
	}
	prod := acceptanceConfig(t.TempDir())
	prod.AppEnv = "production"
	production, err := New(context.Background(), prod, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer production.Close()
	if _, err = production.SeedDemo(context.Background()); !errors.Is(err, auth.ErrUnavailable) {
		t.Fatal("production accepts demo seed")
	}
	var count int
	if err = production.db.QueryRowContext(context.Background(), `SELECT count(*) FROM inventory_dormitories`).Scan(&count); err != nil || count != 0 {
		t.Fatal("production startup seeded demo")
	}
}
