package config

import "testing"

func TestAuthenticationConfiguration(t *testing.T) {
	for _, test := range []struct {
		env       map[string]string
		wantError bool
	}{
		{map[string]string{}, false},
		{map[string]string{"APP_ENV": "development", "DEV_LOGIN_ENABLED": "true"}, false},
		{map[string]string{"APP_ENV": "production"}, false},
		{map[string]string{"APP_ENV": "production", "DEV_LOGIN_ENABLED": "true"}, true},
		{map[string]string{"APP_ENV": "prod", "DEV_LOGIN_ENABLED": "true"}, true},
		{map[string]string{"DEV_LOGIN_ENABLED": "invalid"}, true},
		{map[string]string{"SESSION_TTL": "0s"}, true},
		{map[string]string{"SESSION_TTL": "25h"}, true},
	} {
		_, err := load(func(key string) (string, bool) { v, ok := test.env[key]; return v, ok })
		if (err != nil) != test.wantError {
			t.Fatalf("configuration case %v: %v", test.env, err)
		}
	}
}

func TestEDSConfiguration(t *testing.T) {
	for _, test := range []struct {
		env       map[string]string
		wantError bool
	}{
		{map[string]string{"NCANODE_URL": "http://127.0.0.1:14579"}, false},
		{map[string]string{"APP_ENV": "production", "NCANODE_URL": "http://127.0.0.1:14579", "EDS_ORIGIN": "https://dorm.example.test"}, false},
		{map[string]string{"NCANODE_URL": "http://remote.example.test"}, true},
		{map[string]string{"NCANODE_URL": "https://user:secret@remote.example.test"}, true},
		{map[string]string{"NCANODE_URL": "http://127.0.0.1:14579", "EDS_ORIGIN": "https://dorm.example.test/login"}, true},
		{map[string]string{"NCANODE_URL": "http://127.0.0.1:14579", "EDS_ORIGIN": "https://dorm.example.test?data=sensitive"}, true},
		{map[string]string{"APP_ENV": "production", "NCANODE_URL": "http://127.0.0.1:14579", "EDS_ORIGIN": "http://dorm.example.test"}, true},
		{map[string]string{"NCANODE_URL": "http://127.0.0.1:14579", "EDS_ORIGIN": ""}, true},
	} {
		_, err := load(func(key string) (string, bool) { value, ok := test.env[key]; return value, ok })
		if (err != nil) != test.wantError {
			t.Fatal(test.env, err)
		}
	}
}
