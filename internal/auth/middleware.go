package auth

import (
	"context"
	"errors"
	"net/http"

	"github.com/weezy2005/trial-terminator/internal/httpx"
)

type userKey struct{}

// UserFrom returns the user RequireUser attached to the request context.
func UserFrom(ctx context.Context) (*User, bool) {
	u, ok := ctx.Value(userKey{}).(*User)
	return u, ok
}

// WithUser returns ctx carrying user. Exposed so handlers behind
// RequireUser can be unit tested without a database.
func WithUser(ctx context.Context, user *User) context.Context {
	return context.WithValue(ctx, userKey{}, user)
}

// RequireUser rejects requests without a valid session with 401 and
// otherwise makes the user available through UserFrom.
func (h *Handler) RequireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || c.Value == "" {
			httpx.Error(w, http.StatusUnauthorized, "sign in required")
			return
		}
		user, err := h.store.UserForSession(r.Context(), c.Value)
		if errors.Is(err, ErrNoSession) {
			httpx.Error(w, http.StatusUnauthorized, "sign in required")
			return
		}
		if err != nil {
			h.logger.Error("session lookup failed", "error", err)
			httpx.Error(w, http.StatusInternalServerError, "internal error")
			return
		}
		next(w, r.WithContext(WithUser(r.Context(), user)))
	}
}
