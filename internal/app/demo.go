package app

import (
	"context"
	"path/filepath"
	"student_dormitory/internal/modules/audit"
	"student_dormitory/internal/modules/auth"
	"student_dormitory/internal/modules/inventory"
	"student_dormitory/internal/platform/files"
)

// SeedDemo only assembles the real module services for the explicit demo command.
// It is never called by New or by the server startup path.
func (a *App) SeedDemo(ctx context.Context) (inventory.DemoManifest, error) {
	cfg := a.config
	if cfg.AppEnv != "development" || !cfg.DevLoginEnabled {
		return inventory.DemoManifest{}, auth.ErrUnavailable
	}
	permissions := auth.NewService(auth.NewRepository(a.db), audit.NewRecorder(a.db), auth.Options{DevelopmentLogin: true, SessionTTL: cfg.SessionTTL})
	writer, commandant, err := permissions.DevelopmentActorIDs(ctx)
	if err != nil {
		return inventory.DemoManifest{}, err
	}
	store, err := files.NewLocal(filepath.Join(cfg.FilesDir, "photos"))
	if err != nil {
		return inventory.DemoManifest{}, err
	}
	service := inventory.NewService(inventory.NewRepository(a.db), permissions, audit.NewRecorder(a.db))
	photos := inventory.NewPhotoService(inventory.NewRepository(a.db), permissions, audit.NewRecorder(a.db), store)
	return inventory.NewDemoSeeder(service, photos).Seed(ctx, writer, commandant)
}
