package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/weezy2005/trial-terminator/internal/httpx"
	"golang.org/x/oauth2"
)

const (
	sessionCookie = "tt_session"

	// oauthCookie carries state, nonce and the PKCE verifier between
	// /google/start and /google/callback. Scoped to that path and to ten
	// minutes, the longest a person plausibly spends on Google's screen.
	oauthCookie     = "tt_oauth"
	oauthCookiePath = "/v1/auth/google"
	oauthCookieTTL  = 600
)

// Handler serves the /v1/auth routes and provides RequireUser.
type Handler struct {
	cfg    Config
	store  *Store
	google *googleClient
	logger *slog.Logger
}

func NewHandler(cfg Config, store *Store, logger *slog.Logger) *Handler {
	return &Handler{cfg: cfg, store: store, google: &googleClient{cfg: cfg}, logger: logger}
}

// Register adds the auth routes to mux. wrap is applied to every handler so
// the server can add instrumentation without this package knowing about it.
func (h *Handler) Register(mux *http.ServeMux, wrap func(pattern string, next http.HandlerFunc) http.HandlerFunc) {
	route := func(pattern string, next http.HandlerFunc) {
		mux.HandleFunc(pattern, wrap(pattern, next))
	}
	route("GET /v1/auth/google/start", h.GoogleStart)
	route("GET /v1/auth/google/callback", h.GoogleCallback)
	route("POST /v1/auth/logout", h.Logout)
	route("GET /v1/me", h.RequireUser(h.Me))
	// Not registered at all when disabled, so production has no route to probe.
	if h.cfg.DevLogin {
		route("POST /v1/auth/dev-login", h.DevLogin)
	}
}

// GoogleStart redirects the browser to Google's consent screen.
func (h *Handler) GoogleStart(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.GoogleEnabled() {
		httpx.Error(w, http.StatusServiceUnavailable, "Google sign-in is not configured")
		return
	}
	oc, _, err := h.google.get(r.Context())
	if err != nil {
		h.logger.Error("google discovery failed", "error", err)
		httpx.Error(w, http.StatusServiceUnavailable, "Google sign-in is temporarily unavailable")
		return
	}

	// state: binds the callback to this browser (login CSRF protection).
	// nonce: binds the ID token to this attempt (replay protection).
	// verifier: PKCE, so an intercepted authorization code is useless
	// without this cookie.
	state, nonce := randomString(), randomString()
	verifier := oauth2.GenerateVerifier()

	http.SetCookie(w, &http.Cookie{
		Name:     oauthCookie,
		Value:    strings.Join([]string{state, nonce, verifier}, "."),
		Path:     oauthCookiePath,
		MaxAge:   oauthCookieTTL,
		HttpOnly: true,
		Secure:   h.cfg.CookieSecure(),
		// Lax, not Strict: the callback arrives as a top-level navigation
		// from accounts.google.com, which Strict would strip the cookie from.
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, oc.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

// GoogleCallback finishes sign-in: it checks state, exchanges the code with
// the PKCE verifier, verifies the ID token and its nonce, and starts a session.
//
// Every failure redirects to the frontend with a short login_error code
// rather than rendering details, which are logged instead.
func (h *Handler) GoogleCallback(w http.ResponseWriter, r *http.Request) {
	fail := func(code string, err error) {
		if err != nil {
			h.logger.Warn("google sign-in failed", "reason", code, "error", err)
		}
		h.redirectToFrontend(w, r, url.Values{"login_error": {code}})
	}

	c, err := r.Cookie(oauthCookie)
	// Single use: clear it whatever happens next.
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Path: oauthCookiePath, MaxAge: -1, HttpOnly: true, Secure: h.cfg.CookieSecure()})
	if err != nil {
		fail("expired", nil)
		return
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 3 {
		fail("expired", nil)
		return
	}
	state, nonce, verifier := parts[0], parts[1], parts[2]

	q := r.URL.Query()
	if q.Get("error") != "" {
		fail("cancelled", nil) // the user declined on Google's screen
		return
	}
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
		fail("state_mismatch", errors.New("state parameter does not match cookie"))
		return
	}

	oc, idVerifier, err := h.google.get(r.Context())
	if err != nil {
		fail("unavailable", err)
		return
	}
	tok, err := oc.Exchange(r.Context(), q.Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		fail("exchange_failed", err)
		return
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok {
		fail("exchange_failed", errors.New("token response has no id_token"))
		return
	}
	idToken, err := idVerifier.Verify(r.Context(), rawID)
	if err != nil {
		fail("invalid_token", err)
		return
	}
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(nonce)) != 1 {
		fail("invalid_token", errors.New("id_token nonce does not match cookie"))
		return
	}

	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
	}
	if err := idToken.Claims(&claims); err != nil {
		fail("invalid_token", err)
		return
	}
	// An unverified email could be anyone's, and UpsertGoogleUser links
	// accounts by email, so it's a hard requirement.
	if !claims.EmailVerified || claims.Email == "" {
		fail("email_unverified", nil)
		return
	}

	user, err := h.store.UpsertGoogleUser(r.Context(), idToken.Subject, claims.Email, claims.Name)
	if errors.Is(err, ErrAccountConflict) {
		fail("account_conflict", err)
		return
	}
	if err != nil {
		fail("server_error", err)
		return
	}
	if err := h.startSession(r.Context(), w, user); err != nil {
		fail("server_error", err)
		return
	}
	h.redirectToFrontend(w, r, nil)
}

// DevLogin signs in as any email without Google. Only registered when
// DEV_LOGIN=true, which ConfigFromEnv forbids in production.
func (h *Handler) DevLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if at := strings.IndexByte(email, '@'); at < 1 || at == len(email)-1 {
		httpx.Error(w, http.StatusUnprocessableEntity, "a valid email is required")
		return
	}

	user, err := h.store.UpsertDevUser(r.Context(), email)
	if err != nil {
		h.logger.Error("dev login failed", "error", err)
		httpx.Error(w, http.StatusInternalServerError, "sign-in failed")
		return
	}
	if err := h.startSession(r.Context(), w, user); err != nil {
		h.logger.Error("dev login failed", "error", err)
		httpx.Error(w, http.StatusInternalServerError, "sign-in failed")
		return
	}
	httpx.JSON(w, http.StatusOK, user)
}

// Logout ends the current session. Idempotent: it succeeds with or without one.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := h.store.DeleteSession(r.Context(), c.Value); err != nil {
			h.logger.Error("logout failed", "error", err)
			httpx.Error(w, http.StatusInternalServerError, "logout failed")
			return
		}
	}
	h.setSessionCookie(w, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

// Me returns the signed-in user. Mounted behind RequireUser.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	httpx.JSON(w, http.StatusOK, user)
}

func (h *Handler) startSession(ctx context.Context, w http.ResponseWriter, user *User) error {
	token, err := h.store.CreateSession(ctx, user.ID, h.cfg.SessionTTL)
	if err != nil {
		return err
	}
	h.setSessionCookie(w, token, int(h.cfg.SessionTTL.Seconds()))
	return nil
}

func (h *Handler) setSessionCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true, // page scripts can't read it, so XSS can't steal it
		Secure:   h.cfg.CookieSecure(),
		// Lax: not sent on cross-site POSTs, the first line of CSRF defence.
		// The Origin check in the server middleware is the second.
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *Handler) redirectToFrontend(w http.ResponseWriter, r *http.Request, q url.Values) {
	target := h.cfg.FrontendURL + "/"
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func randomString() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand only fails if the OS entropy source is broken, in
		// which case no security property here can hold anyway.
		panic("auth: crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
