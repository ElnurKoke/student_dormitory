package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"student_dormitory/internal/modules/audit"
	"student_dormitory/internal/modules/auth"
	"student_dormitory/internal/platform/config"
	"student_dormitory/internal/platform/database"
	"student_dormitory/internal/platform/maintenance"
	"student_dormitory/internal/platform/requestid"
)

func main() {
	output := flag.String("out", "var/dev-users.json", "new private credential file; must not exist")
	scopes := flag.String("dormitories", "dev-dorm-1", "comma-separated assigned dormitory scope IDs")
	flag.Parse()
	if err := run(*output, strings.Split(*scopes, ",")); err != nil {
		// SQL/credential errors must not be formatted here: output may be captured.
		fmt.Fprintln(os.Stderr, "Не удалось создать тестовых пользователей. Нужны development, DEV_LOGIN_ENABLED=true, новая БД без этих логинов и новый доступный файл вывода.")
		os.Exit(1)
	}
	fmt.Fprintln(os.Stdout, "Тестовые пользователи созданы. Индивидуальные пароли сохранены в указанном закрытом файле.")
}

func run(output string, scopes []string) (err error) {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.AppEnv != "development" || !cfg.DevLoginEnabled {
		return auth.ErrUnavailable
	}
	guard, err := maintenance.Acquire(cfg.DatabasePath, cfg.FilesDir)
	if err != nil {
		return err
	}
	defer guard.Close()
	ctx := requestid.WithID(context.Background(), requestid.New())
	db, err := database.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		file.Close()
		if err != nil {
			os.Remove(output)
		}
	}()
	service := auth.NewService(auth.NewRepository(db), audit.NewRecorder(db), auth.Options{DevelopmentLogin: true, SessionTTL: cfg.SessionTTL})
	return service.ProvisionDevelopment(ctx, scopes, func(credentials []auth.DevelopmentCredential) error {
		if err := json.NewEncoder(file).Encode(credentials); err != nil {
			return err
		}
		if err := file.Sync(); err != nil {
			return err
		}
		return file.Close()
	})
}
