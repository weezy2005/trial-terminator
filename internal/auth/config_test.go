package auth

import (
	"slices"
	"strings"
	"testing"
)

func TestConfigFromEnv_Defaults(t *testing.T) {
	for _, k := range []string{"ENV", "PUBLIC_API_URL", "FRONTEND_URL", "ALLOWED_ORIGINS", "DEV_LOGIN", "GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET", "GOOGLE_ISSUER"} {
		t.Setenv(k, "")
	}
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GoogleEnabled() || cfg.DevLogin || cfg.CookieSecure() {
		t.Errorf("unexpected defaults: google=%v dev=%v secure=%v", cfg.GoogleEnabled(), cfg.DevLogin, cfg.CookieSecure())
	}
	if !slices.Equal(cfg.AllowedOrigins, []string{"http://localhost:3000"}) {
		t.Errorf("AllowedOrigins = %v, want only the frontend", cfg.AllowedOrigins)
	}
}

func TestConfigFromEnv_ParsesAllowedOrigins(t *testing.T) {
	t.Setenv("ENV", "")
	t.Setenv("ALLOWED_ORIGINS", " https://app.example.com/ ,chrome-extension://abc,, ")
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://app.example.com", "chrome-extension://abc"}
	if !slices.Equal(cfg.AllowedOrigins, want) {
		t.Errorf("AllowedOrigins = %v, want %v", cfg.AllowedOrigins, want)
	}
}

func TestConfigFromEnv_ProductionGuards(t *testing.T) {
	prod := map[string]string{
		"ENV":                  "production",
		"PUBLIC_API_URL":       "https://api.example.com",
		"GOOGLE_CLIENT_ID":     "id",
		"GOOGLE_CLIENT_SECRET": "secret",
		"DEV_LOGIN":            "",
		"GOOGLE_ISSUER":        "",
	}

	tests := []struct {
		name     string
		override map[string]string
		wantErr  string
	}{
		{"valid production config", nil, ""},
		{"dev login can never ship", map[string]string{"DEV_LOGIN": "true"}, "DEV_LOGIN"},
		{"google is required", map[string]string{"GOOGLE_CLIENT_SECRET": ""}, "GOOGLE_CLIENT_ID"},
		{"https is required", map[string]string{"PUBLIC_API_URL": "http://api.example.com"}, "https"},
		{"issuer can't be pointed elsewhere", map[string]string{"GOOGLE_ISSUER": "https://evil.example"}, "GOOGLE_ISSUER"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range prod {
				t.Setenv(k, v)
			}
			for k, v := range tc.override {
				t.Setenv(k, v)
			}
			_, err := ConfigFromEnv()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want one mentioning %q", err, tc.wantErr)
			}
		})
	}
}

func TestDevLoginIsAllowedOutsideProduction(t *testing.T) {
	t.Setenv("ENV", "")
	t.Setenv("DEV_LOGIN", "true")
	cfg, err := ConfigFromEnv()
	if err != nil || !cfg.DevLogin {
		t.Fatalf("DevLogin=%v err=%v", cfg.DevLogin, err)
	}
}
