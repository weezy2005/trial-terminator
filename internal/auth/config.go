// Package auth owns user identity: Google sign-in, dev login, sessions, and
// the middleware that attaches the signed-in user to a request.
//
// See docs/design/001-auth-and-worker-api.md, Decision 1.
package auth

import (
	"errors"
	"os"
	"strings"
	"time"
)

const (
	defaultSessionTTL = 14 * 24 * time.Hour
	googleIssuer      = "https://accounts.google.com"
)

// Config is everything auth needs from the environment, validated once at
// startup so a misconfiguration stops the process instead of surfacing as
// a confusing failure on someone's first login.
type Config struct {
	Env string // "production" enables the strict checks below

	// PublicAPIURL is the API's externally visible base URL. Google
	// redirects back to PublicAPIURL + /v1/auth/google/callback, and an
	// https URL turns on Secure cookies.
	PublicAPIURL string

	// FrontendURL is where the browser lands after signing in or out.
	FrontendURL string

	// AllowedOrigins may call the API from a browser with credentials.
	// It drives both CORS and the cross-site request check.
	AllowedOrigins []string

	// DevLogin enables POST /v1/auth/dev-login, which signs in as any email
	// without Google. For local development and e2e tests only.
	DevLogin bool

	GoogleClientID     string
	GoogleClientSecret string
	GoogleIssuer       string // overridable so tests can stand up a fake provider

	SessionTTL time.Duration
}

// GoogleEnabled reports whether Google sign-in is configured. Without it the
// Google routes answer 503, which keeps local setup to a single docker
// compose up with DEV_LOGIN.
func (c Config) GoogleEnabled() bool {
	return c.GoogleClientID != "" && c.GoogleClientSecret != ""
}

// CookieSecure reports whether cookies should carry the Secure flag. Secure
// cookies are never sent over plain http, so local development on
// http://localhost would lose its session; this follows the scheme the API
// is actually served on.
func (c Config) CookieSecure() bool {
	return strings.HasPrefix(c.PublicAPIURL, "https://")
}

// ConfigFromEnv reads and validates auth configuration.
func ConfigFromEnv() (Config, error) {
	cfg := Config{
		Env:                os.Getenv("ENV"),
		PublicAPIURL:       strings.TrimRight(getEnv("PUBLIC_API_URL", "http://localhost:8081"), "/"),
		FrontendURL:        strings.TrimRight(getEnv("FRONTEND_URL", "http://localhost:3000"), "/"),
		DevLogin:           os.Getenv("DEV_LOGIN") == "true",
		GoogleClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		GoogleIssuer:       getEnv("GOOGLE_ISSUER", googleIssuer),
		SessionTTL:         defaultSessionTTL,
	}

	cfg.AllowedOrigins = []string{cfg.FrontendURL}
	if raw := os.Getenv("ALLOWED_ORIGINS"); raw != "" {
		cfg.AllowedOrigins = nil
		for _, o := range strings.Split(raw, ",") {
			if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
				cfg.AllowedOrigins = append(cfg.AllowedOrigins, o)
			}
		}
	}

	return cfg, cfg.validate()
}

func (c Config) validate() error {
	if c.Env != "production" {
		return nil
	}
	var errs []error
	// DEV_LOGIN signs in as anyone. It must be impossible to ship by accident.
	if c.DevLogin {
		errs = append(errs, errors.New("DEV_LOGIN must not be enabled when ENV=production"))
	}
	if !c.GoogleEnabled() {
		errs = append(errs, errors.New("GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET are required when ENV=production"))
	}
	if !c.CookieSecure() {
		errs = append(errs, errors.New("PUBLIC_API_URL must be https when ENV=production"))
	}
	if c.GoogleIssuer != googleIssuer {
		errs = append(errs, errors.New("GOOGLE_ISSUER must not be overridden when ENV=production"))
	}
	return errors.Join(errs...)
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
