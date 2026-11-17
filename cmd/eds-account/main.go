package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"student_dormitory/internal/modules/audit"
	"student_dormitory/internal/modules/auth"
	"student_dormitory/internal/platform/config"
	"student_dormitory/internal/platform/database"
	"student_dormitory/internal/platform/maintenance"
	"student_dormitory/internal/platform/requestid"
)

func main() {
	iinFile := flag.String("iin-file", "", "private file containing only the IIN; never put IIN in command arguments")
	userID := flag.String("user-id", "", "existing permanent local user ID to link")
	bootstrap := flag.Bool("bootstrap-admin", false, "create the first system administrator; requires no active administrator")
	flag.Parse()
	if err := run(*iinFile, *userID, *bootstrap); err != nil {
		fmt.Fprintln(os.Stderr, "Не удалось привязать ЭЦП. Остановите сервер; проверьте закрытый файл ИИН, ID пользователя и отсутствие конфликтующей привязки. Создание первого администратора разрешено только при отсутствии активных системных администраторов.")
		os.Exit(1)
	}
	fmt.Fprintln(os.Stdout, "Привязка ЭЦП сохранена. Вход потребует действительную подпись сертификатом с этим ИИН.")
}

func run(iinFile, userID string, bootstrap bool) error {
	file, err := os.Open(iinFile)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return auth.ErrCredentials
	}
	data, err := io.ReadAll(io.LimitReader(file, 64))
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
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
	s := auth.NewService(auth.NewRepository(db), audit.NewRecorder(db), auth.Options{SessionTTL: cfg.SessionTTL})
	return s.ProvisionNCAIdentity(ctx, strings.TrimSpace(string(data)), userID, bootstrap)
}
