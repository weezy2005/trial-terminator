package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const app = "https://app.example.com"

func TestCORSMiddleware(t *testing.T) {
	reached := false
	h := corsMiddleware([]string{app}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name        string
		method      string
		origin      string
		bearer      bool
		wantStatus  int
		wantReached bool
		wantCORS    bool
	}{
		{"allowed origin gets credentialed CORS", http.MethodPost, app, false, http.StatusOK, true, true},
		{"preflight from allowed origin", http.MethodOptions, app, false, http.StatusNoContent, false, true},
		{"foreign origin can read nothing", http.MethodGet, "https://evil.example", false, http.StatusOK, true, false},
		// The CSRF case: a foreign page POSTing with the user's cookie.
		{"foreign origin cannot change state", http.MethodPost, "https://evil.example", false, http.StatusForbidden, false, false},
		{"foreign origin with its own bearer token is not CSRF", http.MethodPost, "chrome-extension://abc", true, http.StatusOK, true, false},
		{"no origin (curl, server-to-server) passes", http.MethodPost, "", false, http.StatusOK, true, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reached = false
			req := httptest.NewRequest(tc.method, "/v1/tasks", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.bearer {
				req.Header.Set("Authorization", "Bearer tt_wk_test")
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if reached != tc.wantReached {
				t.Errorf("handler reached = %v, want %v", reached, tc.wantReached)
			}
			gotCORS := rec.Header().Get("Access-Control-Allow-Origin") == tc.origin && tc.origin != ""
			if gotCORS != tc.wantCORS {
				t.Errorf("CORS granted = %v, want %v (ACAO=%q)", gotCORS, tc.wantCORS, rec.Header().Get("Access-Control-Allow-Origin"))
			}
			if tc.wantCORS && rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
				t.Error("allowed origin missing Access-Control-Allow-Credentials")
			}
		})
	}
}
