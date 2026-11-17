package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"student_dormitory/internal/app"
	"student_dormitory/internal/platform/backup"
	"student_dormitory/internal/platform/config"
	"student_dormitory/internal/platform/maintenance"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		switch {
		case errors.Is(err, maintenance.ErrBusy):
			fmt.Fprintln(os.Stderr, "Данные заняты. Остановите сервер и другие команды перед резервированием.")
		case errors.Is(err, maintenance.ErrIncomplete):
			fmt.Fprintln(os.Stderr, "Незавершённая операция. Используйте полную резервную копию и новую папку.")
		default:
			fmt.Fprintln(os.Stderr, "Операция не выполнена. Проверьте аргументы, целостность копии, пути и свободное место. Существующие данные не заменяются.")
		}
		os.Exit(1)
	}
	fmt.Fprintln(os.Stdout, "Операция завершена; целостность SQLite, ссылки и контрольные суммы проверены.")
}
func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: backup create -out DIR | verify -from DIR | restore -from DIR -out NEW_DIR")
	}
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	output := flags.String("out", "", "new private output directory; must not exist")
	source := flags.String("from", "", "complete backup directory")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if len(flags.Args()) > 0 {
		return fmt.Errorf("unexpected arguments")
	}
	switch args[0] {
	case "create":
		if *output == "" || *source != "" {
			return fmt.Errorf("create requires -out")
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		_, err = backup.Create(ctx, cfg.DatabasePath, cfg.FilesDir, *output, app.BackupReferences)
		return err
	case "verify":
		if *source == "" || *output != "" {
			return fmt.Errorf("verify requires -from")
		}
		_, err := backup.Verify(ctx, *source, app.BackupReferences)
		return err
	case "restore":
		if *source == "" || *output == "" {
			return fmt.Errorf("restore requires -from and -out")
		}
		_, err := backup.Restore(ctx, *source, *output, app.BackupReferences)
		return err
	}
	return fmt.Errorf("unknown backup action")
}
