// Package app is the composition root: dependencies and HTTP routes are wired here.
package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"

	"student_dormitory/internal/modules/applications"
	"student_dormitory/internal/modules/audit"
	"student_dormitory/internal/modules/auth"
	"student_dormitory/internal/modules/inventory"
	"student_dormitory/internal/modules/reports"
	"student_dormitory/internal/platform/config"
	"student_dormitory/internal/platform/database"
	"student_dormitory/internal/platform/files"
	"student_dormitory/internal/platform/httpserver"
	"student_dormitory/internal/platform/maintenance"
	"student_dormitory/internal/platform/nca"
	"student_dormitory/internal/platform/requestid"
	"student_dormitory/web"
)

type App struct {
	handler   http.Handler
	db        *sql.DB
	guard     *maintenance.Lock
	closeOnce sync.Once
	closeErr  error
	config    config.Config
}

func New(ctx context.Context, cfg config.Config, log *slog.Logger) (*App, error) {
	if err := cfg.ValidateSecurity(); err != nil {
		return nil, err
	}
	guard, err := maintenance.Acquire(cfg.DatabasePath, cfg.FilesDir)
	if err != nil {
		return nil, err
	}
	ready := false
	defer func() {
		if !ready {
			guard.Close()
		}
	}()
	if err := files.Prepare(cfg.FilesDir); err != nil {
		return nil, err
	}
	db, err := database.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return nil, err
	}
	if cfg.DevLoginEnabled {
		output := cfg.DevUsersFile
		if output == "" {
			output = filepath.Join(filepath.Dir(cfg.DatabasePath), "dev-users.txt")
		}
		service := auth.NewService(auth.NewRepository(db), audit.NewRecorder(db), auth.Options{DevelopmentLogin: true, SessionTTL: cfg.SessionTTL})
		if err := service.BootstrapDevelopment(requestid.WithID(ctx, requestid.New()), func(body []byte) (func() error, error) {
			return files.CreatePrivate(output, body)
		}); err != nil {
			db.Close()
			return nil, err
		}
	}
	handler, err := newHandler(ctx, db, log, cfg)
	if err != nil {
		db.Close()
		return nil, err
	}
	ready = true
	return &App{handler: handler, db: db, guard: guard, config: cfg}, nil
}

func (a *App) Handler() http.Handler { return a.handler }
func (a *App) Close() error {
	a.closeOnce.Do(func() { a.closeErr = errors.Join(a.db.Close(), a.guard.Close()) })
	return a.closeErr
}

func newHandler(ctx context.Context, db *sql.DB, log *slog.Logger, cfg config.Config) (http.Handler, error) {
	renderer, err := httpserver.NewRenderer(web.FS, log)
	if err != nil {
		return nil, err
	}
	static, err := fs.Sub(web.FS, "static")
	if err != nil {
		return nil, fmt.Errorf("static files: %w", err)
	}
	repo := auth.NewRepository(db)
	service := auth.NewService(repo, audit.NewRecorder(db), auth.Options{DevelopmentLogin: cfg.DevLoginEnabled, EDSLogin: cfg.NCANodeURL != "", SessionTTL: cfg.SessionTTL})
	var provider *auth.DevelopmentProvider
	if cfg.DevLoginEnabled {
		provider = auth.NewDevelopmentProvider(repo)
	}
	applicationService := applications.NewService(applications.NewRepository(db), service, audit.NewRecorder(db), service)
	var eds *auth.EDSService
	if cfg.NCANodeURL != "" {
		verifier, err := nca.NewVerifier(cfg.NCANodeURL)
		if err != nil {
			return nil, fmt.Errorf("configure NCA verification")
		}
		eds = auth.NewEDSService(service, verifier, applicationService, cfg.EDSOrigin)
	}
	authentication := auth.NewHandler(service, provider, renderer, cfg.AppEnv == "production", eds)
	fund := inventory.NewHandler(inventory.NewService(inventory.NewRepository(db), service, audit.NewRecorder(db)), authentication, renderer)
	photoStore, err := files.NewLocal(filepath.Join(cfg.FilesDir, "photos"))
	if err != nil {
		return nil, err
	}
	photoService := inventory.NewPhotoService(inventory.NewRepository(db), service, audit.NewRecorder(db), photoStore)
	if err := photoService.Reconcile(ctx); err != nil {
		return nil, fmt.Errorf("reconcile photo storage: %w", err)
	}
	photos := inventory.NewPhotoHandler(photoService, fund)
	catalog := inventory.NewPublicHandler(inventory.NewPublicService(inventory.NewRepository(db)), renderer)
	reporting := reports.NewHandler(reports.NewService(db, service, inventory.NewRepository(db), audit.NewReadRepository()), authentication, renderer)
	documentStore, err := files.NewDocumentLocal(filepath.Join(cfg.FilesDir, "documents"))
	if err != nil {
		return nil, err
	}
	documentService := applications.NewDocumentService(applicationService, documentStore)
	if err := documentService.Reconcile(ctx); err != nil {
		return nil, fmt.Errorf("reconcile application documents: %w", err)
	}
	reviewService := applications.NewReviewService(applicationService, service)
	studentApplications := applications.NewWorkflowHandler(applicationService, reviewService, documentService, authentication, renderer, service)
	mux := http.NewServeMux()
	// Method checks precede CSRF, and all registered changing routes pass through CSRF.
	handle := func(pattern, methods string, handler http.Handler, multipartLimit ...int64) {
		protected := authentication.CSRF(handler)
		if len(multipartLimit) > 0 {
			protected = authentication.MultipartCSRF(handler, multipartLimit[0])
		}
		mux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			for _, method := range strings.Split(methods, ", ") {
				if r.Method == method {
					protected.ServeHTTP(w, r)
					return
				}
			}
			w.Header().Set("Allow", methods)
			renderer.Error(w, r, http.StatusMethodNotAllowed)
		}))
	}
	handle("/{$}", "GET, HEAD", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		renderer.Render(w, r, http.StatusOK, "home", httpserver.Page{Title: "Главная", Data: cfg.DevLoginEnabled})
	}))
	handle("/catalog", "GET, HEAD", http.HandlerFunc(catalog.List))
	handle("/catalog/{dormitoryID}", "GET, HEAD", http.HandlerFunc(catalog.Detail))
	handle("/healthz", "GET, HEAD", httpserver.Health(db, log))
	handle("/static/app.css", "GET, HEAD", http.StripPrefix("/static/", http.FileServerFS(static)))
	handle("/static/ncalayer-login.js", "GET, HEAD", http.StripPrefix("/static/", http.FileServerFS(static)))
	if cfg.DevLoginEnabled || eds != nil {
		handle("/auth/login", "GET, HEAD, POST", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				authentication.Login(w, r)
			} else {
				authentication.LoginForm(w, r)
			}
		}))
	}
	if eds != nil {
		handle("/auth/eds/challenge", "POST", http.HandlerFunc(authentication.EDSChallenge))
		handle("/auth/eds/login", "POST", http.HandlerFunc(authentication.EDSLogin))
	}
	handle("/auth/logout", "POST", http.HandlerFunc(authentication.Logout))
	handle("/account", "GET, HEAD", authentication.Require("account.read", nil, http.HandlerFunc(authentication.Account)))
	handle("/student/profile", "GET, HEAD, POST", http.HandlerFunc(studentApplications.Profile))
	handle("/applications", "GET, HEAD", http.HandlerFunc(studentApplications.List))
	handle("/applications/new", "POST", http.HandlerFunc(studentApplications.Create))
	handle("/applications/{applicationID}", "GET, HEAD", studentApplications.Detail(false))
	handle("/applications/{applicationID}/edit", "GET, HEAD, POST", http.HandlerFunc(studentApplications.Edit))
	handle("/applications/{applicationID}/submit", "POST", http.HandlerFunc(studentApplications.Submit))
	handle("/applications/{applicationID}/withdraw", "POST", http.HandlerFunc(studentApplications.Withdraw))
	handle("/applications/{applicationID}/documents", "POST", http.HandlerFunc(studentApplications.Upload), applications.MaxDocumentBytes+(64<<10))
	handle("/applications/{applicationID}/documents/{versionID}", "GET, HEAD", studentApplications.Download(false))
	handle("/staff/applications", "GET, HEAD", studentApplications.StaffList(false))
	handle("/staff/application-assignments", "GET, HEAD", studentApplications.StaffList(true))
	handle("/staff/application-assignments/{applicationID}", "POST", http.HandlerFunc(studentApplications.Assign))
	handle("/staff/applications/{applicationID}", "GET, HEAD", studentApplications.Detail(true))
	handle("/staff/applications/{applicationID}/review", "POST", http.HandlerFunc(studentApplications.Review))
	handle("/staff/applications/{applicationID}/documents/{versionID}", "GET, HEAD", studentApplications.Download(true))
	handle("/account/notifications", "GET, HEAD", http.HandlerFunc(studentApplications.Notifications))
	handle("/account/notifications/{notificationID}/read", "POST", http.HandlerFunc(studentApplications.ReadNotification))
	handle("/account/role-notifications/{notificationID}/read", "POST", authentication.Require("account.read", nil, http.HandlerFunc(authentication.ReadRoleNotification)))
	handle("/admin/users", "GET, HEAD", authentication.Require("auth.users.read", nil, http.HandlerFunc(authentication.Users)))
	handle("/admin/users/{userID}/role", "POST", authentication.Require("auth.users.roles.write", nil, http.HandlerFunc(authentication.ChangeRole)))
	handle("/photos/{photoID}", "GET, HEAD", photos.Image(true))
	handle("/staff/photos/{photoID}", "GET, HEAD", photos.Image(false))
	handle("/staff/statistics", "GET, HEAD", http.HandlerFunc(reporting.Statistics))
	handle("/staff/registry-export", "GET, HEAD", reporting.Registry(false))
	handle("/staff/registry.csv", "GET, HEAD", reporting.Registry(true))
	handle("/staff/history", "GET, HEAD", http.HandlerFunc(reporting.History))
	handle("/staff/dormitories", "GET, HEAD", http.HandlerFunc(fund.List))
	for _, route := range []struct {
		kind               inventory.Kind
		collection, detail string
	}{
		{inventory.Dormitory, "/staff/dormitories", "/staff/dormitories/{dormitoryID}"},
		{inventory.Building, "/staff/dormitories/{dormitoryID}/buildings", "/staff/dormitories/{dormitoryID}/buildings/{buildingID}"},
		{inventory.Floor, "/staff/dormitories/{dormitoryID}/buildings/{buildingID}/floors", "/staff/dormitories/{dormitoryID}/buildings/{buildingID}/floors/{floorID}"},
		{inventory.Room, "/staff/dormitories/{dormitoryID}/buildings/{buildingID}/floors/{floorID}/rooms", "/staff/dormitories/{dormitoryID}/buildings/{buildingID}/floors/{floorID}/rooms/{roomID}"},
		{inventory.Bed, "/staff/dormitories/{dormitoryID}/buildings/{buildingID}/floors/{floorID}/rooms/{roomID}/beds", "/staff/dormitories/{dormitoryID}/buildings/{buildingID}/floors/{floorID}/rooms/{roomID}/beds/{bedID}"},
	} {
		if route.kind == inventory.Dormitory || route.kind == inventory.Room {
			handle(route.detail+"/photos", "POST", http.HandlerFunc(photos.Upload), inventory.MaxPhotoBytes+(64<<10))
			handle(route.detail+"/photos/{photoID}/edit", "POST", photos.Change(false))
			handle(route.detail+"/photos/{photoID}/delete", "POST", photos.Change(true))
		}
		if route.kind == inventory.Bed {
			handle(route.detail+"/status", "GET, HEAD, POST", http.HandlerFunc(fund.BedStatus))
		}
		handle(route.collection+"/new", "GET, HEAD, POST", fund.Form(route.kind, true))
		handle(route.detail, "GET, HEAD", http.HandlerFunc(fund.Show))
		handle(route.detail+"/edit", "GET, HEAD, POST", fund.Form(route.kind, false))
		handle(route.detail+"/archive", "POST", fund.Archive(true))
		handle(route.detail+"/restore", "POST", fund.Archive(false))
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { renderer.Error(w, r, http.StatusNotFound) })
	return httpserver.Localization(authentication.Navigation(httpserver.Middleware(mux, log, renderer)), cfg.AppEnv == "production"), nil
}
