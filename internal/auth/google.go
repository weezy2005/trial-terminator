package auth

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// googleClient lazily discovers Google's OIDC configuration.
//
// ARCHITECTURAL DECISION: Why discover lazily instead of at startup?
// Discovery is an HTTP call to Google. Doing it at startup would make the
// API's ability to boot depend on Google being reachable, so a blip on
// their side would crash-loop every replica, including for users who are
// already signed in. Lazily, a failure only affects that one sign-in attempt
// (503), and the next attempt retries.
type googleClient struct {
	cfg Config

	mu       sync.Mutex
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
}

func (g *googleClient) get(ctx context.Context) (*oauth2.Config, *oidc.IDTokenVerifier, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.oauth != nil {
		return g.oauth, g.verifier, nil
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	provider, err := oidc.NewProvider(ctx, g.cfg.GoogleIssuer)
	if err != nil {
		return nil, nil, fmt.Errorf("auth: discover %s: %w", g.cfg.GoogleIssuer, err)
	}

	g.oauth = &oauth2.Config{
		ClientID:     g.cfg.GoogleClientID,
		ClientSecret: g.cfg.GoogleClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  g.cfg.PublicAPIURL + "/v1/auth/google/callback",
		Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
	}
	// The verifier checks the ID token's signature against Google's
	// published keys, its issuer, its expiry, and that its audience is our
	// client ID, so a token minted for some other app is rejected.
	g.verifier = provider.Verifier(&oidc.Config{ClientID: g.cfg.GoogleClientID})
	return g.oauth, g.verifier, nil
}
