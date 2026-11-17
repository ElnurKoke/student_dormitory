package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"student_dormitory/internal/app"
	"student_dormitory/internal/platform/config"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) (result error) {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 15*time.Second)
	application, err := app.New(startup, cfg, log)
	cancel()
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, application.Close()) }()
	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.HTTPAddr, err)
	}
	server := &http.Server{
		Handler:           application.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	log.Info("server started", "address", listener.Addr().String())
	select {
	case err := <-served:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		stop() // A second interrupt can force termination.
		log.Info("shutting down server")
	}
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancelShutdown()
	if err := server.Shutdown(shutdown); err != nil {
		return errors.Join(fmt.Errorf("graceful shutdown: %w", err), server.Close(), <-served)
	}
	if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Info("server stopped gracefully")
	return nil
}
