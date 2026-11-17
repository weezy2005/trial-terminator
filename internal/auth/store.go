package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrNoSession means the token is unknown or expired.
	ErrNoSession = errors.New("auth: no valid session")

	// ErrAccountConflict means a Google account's email already belongs to a
	// different Google account here, e.g. a Workspace address that was
	// reassigned. Merging them automatically would hand one person the
	// other's tasks, so sign-in is refused instead.
	ErrAccountConflict = errors.New("auth: email belongs to a different Google account")
)

// User is a signed-in person. It's what handlers get from UserFrom.
type User struct {
	ID    uuid.UUID `json:"id"`
	Email string    `json:"email"`
	Name  string    `json:"name,omitempty"`
}

// Store is the Postgres-backed persistence for users and sessions.
type Store struct {
	db *pgxpool.Pool
}

func NewStore(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

// NewToken returns a random 256-bit token for a client to hold and the hash
// to store server-side. Only the hash ever reaches the database.
func NewToken() (token string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("auth: generate token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

// HashToken is the lookup key for a client-held token.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

const userColumns = `id, email, COALESCE(name, '')`

func scanUser(row pgx.Row) (*User, error) {
	u := &User{}
	if err := row.Scan(&u.ID, &u.Email, &u.Name); err != nil {
		return nil, err
	}
	return u, nil
}

// UpsertGoogleUser returns the user for a verified Google identity, creating
// or linking it as needed. The caller must have checked email_verified.
//
// Lookup order:
//  1. By sub, Google's permanent account ID. Email and name are refreshed
//     because both can change on Google's side.
//  2. By email, for an account created through DEV_LOGIN (no sub yet).
//     Google has verified the address, so linking is safe.
//  3. Otherwise a new user.
//
// Two first-time callbacks for the same account can race to step 3; the
// loser hits the unique constraint on google_sub and simply retries, which
// then finds the winner's row in step 1.
func (s *Store) UpsertGoogleUser(ctx context.Context, sub, email, name string) (*User, error) {
	for attempt := 0; ; attempt++ {
		var user *User
		err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			var err error
			user, err = scanUser(tx.QueryRow(ctx, `
				UPDATE users SET email = $2, name = NULLIF($3, '')
				WHERE google_sub = $1
				RETURNING `+userColumns, sub, email, name))
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}

			user, err = scanUser(tx.QueryRow(ctx, `
				UPDATE users SET google_sub = $1, name = NULLIF($3, '')
				WHERE email = $2 AND google_sub IS NULL
				RETURNING `+userColumns, sub, email, name))
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}

			user, err = scanUser(tx.QueryRow(ctx, `
				INSERT INTO users (google_sub, email, name) VALUES ($1, $2, NULLIF($3, ''))
				RETURNING `+userColumns, sub, email, name))
			return err
		})

		var pgErr *pgconn.PgError
		switch {
		case err == nil:
			return user, nil
		case errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "users_email_key":
			return nil, ErrAccountConflict
		case errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "users_google_sub_key" && attempt == 0:
			continue
		default:
			return nil, fmt.Errorf("auth: upsert google user: %w", err)
		}
	}
}

// UpsertDevUser returns the user with this email, creating it if needed.
// DEV_LOGIN only: it trusts the email as given.
func (s *Store) UpsertDevUser(ctx context.Context, email string) (*User, error) {
	// DO UPDATE with a no-op assignment (rather than DO NOTHING) so RETURNING
	// yields the existing row too.
	user, err := scanUser(s.db.QueryRow(ctx, `
		INSERT INTO users (email) VALUES ($1)
		ON CONFLICT (email) DO UPDATE SET email = EXCLUDED.email
		RETURNING `+userColumns, email))
	if err != nil {
		return nil, fmt.Errorf("auth: upsert dev user: %w", err)
	}
	return user, nil
}

// CreateSession starts a session for userID and returns the token for the
// cookie. It also clears that user's expired sessions, which bounds the
// table without a separate cleanup job.
func (s *Store) CreateSession(ctx context.Context, userID uuid.UUID, ttl time.Duration) (string, error) {
	token, hash, err := NewToken()
	if err != nil {
		return "", err
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1 AND expires_at <= NOW()`, userID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO sessions (token_hash, user_id, expires_at)
			VALUES ($1, $2, NOW() + $3::interval)`,
			hash, userID, ttl.String())
		return err
	})
	if err != nil {
		return "", fmt.Errorf("auth: create session: %w", err)
	}
	return token, nil
}

// UserForSession resolves a session token to its user, or ErrNoSession.
func (s *Store) UserForSession(ctx context.Context, token string) (*User, error) {
	user, err := scanUser(s.db.QueryRow(ctx, `
		SELECT u.id, u.email, COALESCE(u.name, '')
		FROM   sessions s JOIN users u ON u.id = s.user_id
		WHERE  s.token_hash = $1 AND s.expires_at > NOW()`,
		HashToken(token)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, fmt.Errorf("auth: look up session: %w", err)
	}
	return user, nil
}

// DeleteSession ends a session. Deleting an unknown token is not an error,
// so logout is idempotent.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, HashToken(token)); err != nil {
		return fmt.Errorf("auth: delete session: %w", err)
	}
	return nil
}
