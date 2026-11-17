package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"student_dormitory/internal/app"
	"student_dormitory/internal/modules/auth"
	"student_dormitory/internal/platform/config"
	"student_dormitory/internal/platform/files"
	"student_dormitory/internal/platform/maintenance"
	"student_dormitory/internal/platform/requestid"
)

func main() {
	output := flag.String("out", "var/demo", "new isolated demonstration directory; must not exist")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *output); err != nil {
		fmt.Fprintln(os.Stderr, "Демо не создано. Нужны APP_ENV=development, DEV_LOGIN_ENABLED=true и новая папка. При сбое сохраните диагностику и используйте другую папку; существующие данные не перезаписываются.")
		os.Exit(1)
	}
	fmt.Fprintln(os.Stdout, "Демонстрационный фонд создан. ID и ожидаемые количества — demo.json; индивидуальные пароли — закрытый dev-users.txt в указанной папке.")
}
func run(ctx context.Context, output string) (err error) {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.AppEnv != "development" || !cfg.DevLoginEnabled {
		return auth.ErrUnavailable
	}
	if output == "" {
		return fmt.Errorf("output directory is required")
	}
	absolute, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
		return err
	}
	if err = os.Mkdir(absolute, 0700); err != nil {
		return err
	}
	cfg.DatabasePath = filepath.Join(absolute, "dormitory.db")
	cfg.FilesDir = filepath.Join(absolute, "files")
	cfg.DevUsersFile = filepath.Join(absolute, "dev-users.txt")
	application, err := app.New(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, application.Close()) }()
	marker := filepath.Join(absolute, maintenance.IncompleteMarker)
	if _, err = files.CreatePrivate(marker, []byte("incomplete demo dataset\n")); err != nil {
		return err
	}
	root, err := os.Open(absolute)
	if err != nil {
		return err
	}
	if err = errors.Join(root.Sync(), root.Close()); err != nil {
		return err
	}
	manifest, err := application.SeedDemo(requestid.WithID(ctx, requestid.New()))
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if _, err = files.CreatePrivate(filepath.Join(absolute, "demo.json"), body); err != nil {
		return err
	}
	if err = os.Remove(marker); err != nil {
		return err
	}
	directory, err := os.Open(absolute)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
