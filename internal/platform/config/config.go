package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"student_dormitory/internal/platform/nca"
)

type Config struct {
	HTTPAddr        string
	DatabasePath    string
	FilesDir        string
	ShutdownTimeout time.Duration
	AppEnv          string
	DevLoginEnabled bool
	DevUsersFile    string
	SessionTTL      time.Duration
	NCANodeURL      string
	EDSOrigin       string
}

func Load() (Config, error) {
	return load(os.LookupEnv)
}

func load(lookup func(string) (string, bool)) (Config, error) {
	value := func(key, fallback string) string {
		if v, ok := lookup(key); ok {
			return strings.TrimSpace(v)
		}
		return fallback
	}
	cfg := Config{
		HTTPAddr:     value("HTTP_ADDR", "localhost:8080"),
		DatabasePath: value("DATABASE_PATH", "var/dormitory.db"),
		FilesDir:     value("FILES_DIR", "var/files"),
		AppEnv:       value("APP_ENV", "development"),
		NCANodeURL:   value("NCANODE_URL", ""),
		EDSOrigin:    value("EDS_ORIGIN", "http://localhost:8080"),
	}
	if cfg.DatabasePath == "" || cfg.FilesDir == "" {
		return Config{}, fmt.Errorf("DATABASE_PATH and FILES_DIR must not be empty")
	}
	cfg.DevUsersFile = value("DEV_USERS_FILE", filepath.Join(filepath.Dir(cfg.DatabasePath), "dev-users.txt"))
	if cfg.DevUsersFile == "" {
		return Config{}, fmt.Errorf("DEV_USERS_FILE must not be empty")
	}
	// Accept a file path only; SQLite URI options belong to the database layer.
	if cfg.DatabasePath == ":memory:" || strings.HasPrefix(cfg.DatabasePath, "file:") {
		return Config{}, fmt.Errorf("DATABASE_PATH must be a persistent filesystem path")
	}
	_, port, err := net.SplitHostPort(cfg.HTTPAddr)
	if err != nil {
		return Config{}, fmt.Errorf("HTTP_ADDR must be host:port: %w", err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return Config{}, fmt.Errorf("HTTP_ADDR port must be between 1 and 65535")
	}
	cfg.ShutdownTimeout, err = time.ParseDuration(value("SHUTDOWN_TIMEOUT", "10s"))
	if err != nil || cfg.ShutdownTimeout <= 0 {
		return Config{}, fmt.Errorf("SHUTDOWN_TIMEOUT must be a positive duration such as 10s")
	}
	cfg.DevLoginEnabled, err = strconv.ParseBool(value("DEV_LOGIN_ENABLED", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("DEV_LOGIN_ENABLED must be a boolean")
	}
	cfg.SessionTTL, err = time.ParseDuration(value("SESSION_TTL", "8h"))
	if err != nil {
		return Config{}, fmt.Errorf("SESSION_TTL must be a duration")
	}
	return cfg, cfg.ValidateSecurity()
}

// ValidateSecurity is also called at application assembly, so bypassing Load
// does not allow development login in a production configuration.
func (c Config) ValidateSecurity() error {
	if c.AppEnv != "development" && c.AppEnv != "production" {
		return fmt.Errorf("APP_ENV must be development or production")
	}
	if c.AppEnv != "development" && c.DevLoginEnabled {
		return fmt.Errorf("development login is forbidden outside development")
	}
	if c.NCANodeURL != "" {
		if err := nca.ValidateEndpoint(c.NCANodeURL); err != nil {
			return fmt.Errorf("NCANODE_URL must be an HTTPS origin or loopback HTTP origin without credentials, path or query")
		}
		origin, err := url.Parse(c.EDSOrigin)
		if err != nil || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.ForceQuery || strings.Contains(c.EDSOrigin, "#") || origin.Fragment != "" || (origin.Scheme != "http" && origin.Scheme != "https") {
			return fmt.Errorf("EDS_ORIGIN must be the public site origin without path, query or credentials")
		}
		if c.AppEnv == "production" && origin.Scheme != "https" {
			return fmt.Errorf("production EDS_ORIGIN requires HTTPS")
		}
	}
	if c.SessionTTL < time.Second || c.SessionTTL > 24*time.Hour {
		return fmt.Errorf("SESSION_TTL must be between 1s and 24h")
	}
	return nil
}
