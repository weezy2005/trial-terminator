//go:build integration

package auth_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/weezy2005/trial-terminator/internal/auth"
	"github.com/weezy2005/trial-terminator/internal/pgtest"
)

const (
	frontendURL = "http://frontend.test"
	clientID    = "test-client-id"
)

// --- Fake Google -----------------------------------------------------------

// tokenOpts shapes the ID token the fake provider issues for one login.
type tokenOpts struct {
	sub, email      string
	emailUnverified bool
	nonce           string // overrides the nonce from the auth request
	audience        string // overrides the client ID
}

type pendingCode struct {
	challenge, nonce string
	opts             tokenOpts
}

// fakeGoogle is a minimal OIDC provider: discovery, a JWKS, and a token
// endpoint that enforces PKCE and returns RS256-signed ID tokens. The real
// go-oidc verifier runs against it, so signature, issuer, audience and expiry
// checks are all exercised for real.
type fakeGoogle struct {
	srv *httptest.Server
	key *rsa.PrivateKey

	mu    sync.Mutex
	codes map[string]pendingCode
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	g := &fakeGoogle{key: key, codes: map[string]pendingCode{}}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                g.srv.URL,
			"authorization_endpoint":                g.srv.URL + "/auth",
			"token_endpoint":                        g.srv.URL + "/token",
			"jwks_uri":                              g.srv.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
			{Key: &g.key.PublicKey, KeyID: "test-key", Algorithm: "RS256", Use: "sig"},
		}})
	})
	mux.HandleFunc("/token", g.token(t))
	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

// authorize plays the user clicking "Continue" on Google's consent screen.
func (g *fakeGoogle) authorize(challenge, nonce string, opts tokenOpts) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	code := base64.RawURLEncoding.EncodeToString([]byte(time.Now().String()))
	g.codes[code] = pendingCode{challenge: challenge, nonce: nonce, opts: opts}
	return code
}

func (g *fakeGoogle) token(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		g.mu.Lock()
		pc, ok := g.codes[r.Form.Get("code")]
		delete(g.codes, r.Form.Get("code")) // codes are single use
		g.mu.Unlock()

		// PKCE: the verifier must hash to the challenge sent at /auth.
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != pc.challenge {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}

		o := pc.opts
		nonce, aud := pc.nonce, clientID
		if o.nonce != "" {
			nonce = o.nonce
		}
		if o.audience != "" {
			aud = o.audience
		}
		claims, _ := json.Marshal(map[string]any{
			"iss":            g.srv.URL,
			"aud":            aud,
			"sub":            o.sub,
			"email":          o.email,
			"email_verified": !o.emailUnverified,
			"name":           "Test User",
			"nonce":          nonce,
			"iat":            time.Now().Unix(),
			"exp":            time.Now().Add(time.Hour).Unix(),
		})
		signer, err := jose.NewSigner(
			jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: g.key, KeyID: "test-key"}},
			(&jose.SignerOptions{}).WithType("JWT"),
		)
		if err != nil {
			t.Error(err)
			return
		}
		jws, err := signer.Sign(claims)
		if err != nil {
			t.Error(err)
			return
		}
		idToken, _ := jws.CompactSerialize()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "fake-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     idToken,
		})
	}
}

// --- Harness ---------------------------------------------------------------

// harness is the API's auth routes behind a real HTTP server plus a browser:
// a client with a cookie jar that doesn't follow redirects, so each hop of
// the OAuth dance can be inspected.
type harness struct {
	api    *httptest.Server
	google *fakeGoogle
	pool   *pgxpool.Pool
	client *http.Client
}

func newHarness(t *testing.T, devLogin, googleOn bool) *harness {
	t.Helper()
	h := &harness{pool: pgtest.MigratedPool(t)}

	var handler http.Handler
	h.api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(h.api.Close)

	cfg := auth.Config{
		PublicAPIURL: h.api.URL,
		FrontendURL:  frontendURL,
		DevLogin:     devLogin,
		SessionTTL:   time.Hour,
	}
	if googleOn {
		h.google = newFakeGoogle(t)
		cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.GoogleIssuer = clientID, "secret", h.google.srv.URL
	}

	mux := http.NewServeMux()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	auth.NewHandler(cfg, auth.NewStore(h.pool), logger).Register(mux, func(_ string, next http.HandlerFunc) http.HandlerFunc { return next })
	handler = mux

	h.client = h.newBrowser(t)
	return h
}

func (h *harness) newBrowser(t *testing.T) *http.Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (h *harness) do(t *testing.T, c *http.Client, method, path string, body any) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.api.URL+path, r)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (h *harness) me(t *testing.T, c *http.Client) (*auth.User, int) {
	t.Helper()
	resp := h.do(t, c, http.MethodGet, "/v1/me", nil)
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode
	}
	var u auth.User
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		t.Fatal(err)
	}
	return &u, resp.StatusCode
}

// startGoogle runs /google/start and returns the parsed redirect to Google.
func (h *harness) startGoogle(t *testing.T) url.Values {
	t.Helper()
	resp := h.do(t, h.client, http.MethodGet, "/v1/auth/google/start", nil)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("start: status %d, want 302", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := loc.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Fatalf("start: missing PKCE S256 challenge in %s", loc)
	}
	if q.Get("state") == "" || q.Get("nonce") == "" {
		t.Fatalf("start: missing state or nonce in %s", loc)
	}
	if got, want := q.Get("redirect_uri"), h.api.URL+"/v1/auth/google/callback"; got != want {
		t.Fatalf("redirect_uri = %s, want %s", got, want)
	}
	return q
}

// googleLogin runs the whole flow and returns the callback response.
func (h *harness) googleLogin(t *testing.T, opts tokenOpts) *http.Response {
	t.Helper()
	q := h.startGoogle(t)
	code := h.google.authorize(q.Get("code_challenge"), q.Get("nonce"), opts)
	return h.do(t, h.client, http.MethodGet, "/v1/auth/google/callback?code="+code+"&state="+url.QueryEscape(q.Get("state")), nil)
}

func loginError(t *testing.T, resp *http.Response) string {
	t.Helper()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("callback: status %d, want 302", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if loc.Scheme+"://"+loc.Host != frontendURL {
		t.Fatalf("redirected to %s, want the frontend", loc)
	}
	return loc.Query().Get("login_error")
}

// --- Google sign-in --------------------------------------------------------

func TestGoogleSignIn_CreatesSession(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, true)

	resp := h.googleLogin(t, tokenOpts{sub: "g-1", email: "ada@example.com"})
	if e := loginError(t, resp); e != "" {
		t.Fatalf("login_error = %q", e)
	}
	u, status := h.me(t, h.client)
	if status != http.StatusOK || u.Email != "ada@example.com" || u.Name != "Test User" {
		t.Fatalf("/v1/me = %d %+v", status, u)
	}
}

func TestGoogleSignIn_Rejections(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		opts  tokenOpts
		state string // overrides the state echoed to the callback
		want  string
	}{
		{"forged state (login CSRF)", tokenOpts{sub: "g", email: "a@example.com"}, "attacker-state", "state_mismatch"},
		{"replayed or foreign nonce", tokenOpts{sub: "g", email: "a@example.com", nonce: "other"}, "", "invalid_token"},
		{"token minted for another app", tokenOpts{sub: "g", email: "a@example.com", audience: "other-app"}, "", "invalid_token"},
		{"unverified email", tokenOpts{sub: "g", email: "a@example.com", emailUnverified: true}, "", "email_unverified"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, false, true)
			q := h.startGoogle(t)
			code := h.google.authorize(q.Get("code_challenge"), q.Get("nonce"), tc.opts)
			state := q.Get("state")
			if tc.state != "" {
				state = tc.state
			}
			resp := h.do(t, h.client, http.MethodGet, "/v1/auth/google/callback?code="+code+"&state="+url.QueryEscape(state), nil)

			if got := loginError(t, resp); got != tc.want {
				t.Errorf("login_error = %q, want %q", got, tc.want)
			}
			if _, status := h.me(t, h.client); status != http.StatusUnauthorized {
				t.Errorf("/v1/me = %d after a rejected login, want 401", status)
			}
		})
	}
}

func TestGoogleSignIn_CallbackWithoutStartIsRejected(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, true)
	resp := h.do(t, h.client, http.MethodGet, "/v1/auth/google/callback?code=x&state=y", nil)
	if got := loginError(t, resp); got != "expired" {
		t.Errorf("login_error = %q, want expired", got)
	}
}

// The oauth cookie is single use: replaying a callback URL (say, from browser
// history) must not sign anyone in again.
func TestGoogleSignIn_CallbackCannotBeReplayed(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, true)
	q := h.startGoogle(t)
	code := h.google.authorize(q.Get("code_challenge"), q.Get("nonce"), tokenOpts{sub: "g", email: "a@example.com"})
	callback := "/v1/auth/google/callback?code=" + code + "&state=" + url.QueryEscape(q.Get("state"))

	if e := loginError(t, h.do(t, h.client, http.MethodGet, callback, nil)); e != "" {
		t.Fatalf("first callback: login_error = %q", e)
	}
	other := h.newBrowser(t)
	if e := loginError(t, h.do(t, other, http.MethodGet, callback, nil)); e != "expired" {
		t.Errorf("replayed callback: login_error = %q, want expired", e)
	}
}

func TestGoogleSignIn_AccountLinkingAndConflicts(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true, true)

	// A dev-login account is linked when Google verifies the same email.
	dev := h.newBrowser(t)
	h.do(t, dev, http.MethodPost, "/v1/auth/dev-login", map[string]string{"email": "grace@example.com"})
	devUser, _ := h.me(t, dev)

	if e := loginError(t, h.googleLogin(t, tokenOpts{sub: "g-grace", email: "grace@example.com"})); e != "" {
		t.Fatalf("login_error = %q", e)
	}
	googleUser, _ := h.me(t, h.client)
	if googleUser.ID != devUser.ID {
		t.Errorf("Google sign-in created user %s instead of linking %s", googleUser.ID, devUser.ID)
	}

	// The same Google account with a new email is still the same user.
	if e := loginError(t, h.googleLogin(t, tokenOpts{sub: "g-grace", email: "grace@new.example.com"})); e != "" {
		t.Fatalf("login_error = %q", e)
	}
	renamed, _ := h.me(t, h.client)
	if renamed.ID != devUser.ID || renamed.Email != "grace@new.example.com" {
		t.Errorf("after email change got %+v, want same ID with new email", renamed)
	}

	// A different Google account claiming that email is refused, not merged.
	if e := loginError(t, h.googleLogin(t, tokenOpts{sub: "g-intruder", email: "grace@new.example.com"})); e != "account_conflict" {
		t.Errorf("login_error = %q, want account_conflict", e)
	}
}

func TestGoogleStart_NotConfigured(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true, false)
	if resp := h.do(t, h.client, http.MethodGet, "/v1/auth/google/start", nil); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}

// --- Dev login and sessions ------------------------------------------------

func TestDevLogin_SessionLifecycle(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true, false)

	if _, status := h.me(t, h.client); status != http.StatusUnauthorized {
		t.Fatalf("/v1/me before login = %d, want 401", status)
	}
	resp := h.do(t, h.client, http.MethodPost, "/v1/auth/dev-login", map[string]string{"email": "  Linus@Example.com "})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dev-login = %d", resp.StatusCode)
	}
	u, status := h.me(t, h.client)
	if status != http.StatusOK || u.Email != "linus@example.com" {
		t.Fatalf("/v1/me = %d %+v", status, u)
	}

	if resp := h.do(t, h.client, http.MethodPost, "/v1/auth/logout", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %d", resp.StatusCode)
	}
	if _, status := h.me(t, h.client); status != http.StatusUnauthorized {
		t.Errorf("/v1/me after logout = %d, want 401", status)
	}
	if resp := h.do(t, h.client, http.MethodPost, "/v1/auth/logout", nil); resp.StatusCode != http.StatusNoContent {
		t.Errorf("second logout = %d, want 204 (idempotent)", resp.StatusCode)
	}
}

func TestDevLogin_RejectsBadEmail(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true, false)
	for _, email := range []string{"", "nobody", "@example.com", "a@"} {
		if resp := h.do(t, h.client, http.MethodPost, "/v1/auth/dev-login", map[string]string{"email": email}); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("email %q: status %d, want 422", email, resp.StatusCode)
		}
	}
}

func TestDevLogin_AbsentWhenDisabled(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, false)
	resp := h.do(t, h.client, http.MethodPost, "/v1/auth/dev-login", map[string]string{"email": "a@example.com"})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (route must not exist)", resp.StatusCode)
	}
}

func TestSession_ExpiredSessionIsRejected(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true, false)
	h.do(t, h.client, http.MethodPost, "/v1/auth/dev-login", map[string]string{"email": "a@example.com"})

	if _, err := h.pool.Exec(context.Background(), "UPDATE sessions SET expires_at = NOW() - interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	if _, status := h.me(t, h.client); status != http.StatusUnauthorized {
		t.Errorf("/v1/me with expired session = %d, want 401", status)
	}
}

func TestSession_OnlyTheHashIsStored(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true, false)
	resp := h.do(t, h.client, http.MethodPost, "/v1/auth/dev-login", map[string]string{"email": "a@example.com"})

	var token string
	for _, c := range resp.Cookies() {
		if c.Name == "tt_session" {
			token = c.Value
			if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
				t.Errorf("session cookie flags: HttpOnly=%v SameSite=%v", c.HttpOnly, c.SameSite)
			}
		}
	}
	if token == "" {
		t.Fatal("no session cookie set")
	}

	var stored []byte
	if err := h.pool.QueryRow(context.Background(), "SELECT token_hash FROM sessions").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if string(stored) == token || !bytes.Equal(stored, auth.HashToken(token)) {
		t.Error("sessions.token_hash is not sha256(cookie value)")
	}
}
