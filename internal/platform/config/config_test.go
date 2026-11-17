package config

import "testing"

func TestLoad(t *testing.T) {
	cfg, err := load(func(string) (string, bool) { return "", false })
	if err != nil || cfg.HTTPAddr != "localhost:8080" || cfg.ShutdownTimeout.Seconds() != 10 {
		t.Fatalf("defaults: %+v, %v", cfg, err)
	}
	for key, value := range map[string]string{
		"HTTP_ADDR": "invalid", "DATABASE_PATH": "", "FILES_DIR": "", "SHUTDOWN_TIMEOUT": "0s", "DEV_USERS_FILE": "",
	} {
		t.Run(key, func(t *testing.T) {
			_, err := load(func(k string) (string, bool) { return value, k == key })
			if err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}
