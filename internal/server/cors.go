package server

import (
	"net/http"

	"github.com/weezy2005/trial-terminator/internal/httpx"
)

// corsMiddleware lets the listed browser origins call the API with
// credentials and blocks cross-site state changes from everyone else.
//
// ARCHITECTURAL DECISION: Why an allowlist instead of "*"?
// The session lives in a cookie. With credentials, browsers refuse "*", and
// even if they didn't, every website would be able to read a signed-in
// user's tasks. Only origins we name get CORS headers.
//
// CORS only controls whether a page can READ a response. A cross-site POST
// is still sent, cookie included if SameSite allows it. So unsafe methods
// with a foreign Origin header are rejected outright. Requests without an
// Origin (curl, server-to-server) and requests carrying their own
// Authorization header pass, since cross-site request forgery depends on
// the browser attaching credentials the attacker doesn't have.
func corsMiddleware(allowedOrigins []string, next http.Handler) http.Handler {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[o] = true
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		// Responses differ by Origin, so caches must key on it.
		w.Header().Add("Vary", "Origin")

		if origin != "" && allowed[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Access-Control-Max-Age", "600")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if origin != "" && !allowed[origin] && !isSafeMethod(r.Method) && r.Header.Get("Authorization") == "" {
			httpx.Error(w, http.StatusForbidden, "cross-site request blocked")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}
