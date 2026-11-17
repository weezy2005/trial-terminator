-- Migration 003: users and sessions
--
-- See docs/design/001-auth-and-worker-api.md, Decision 1.

-- google_sub is Google's stable account ID (the "sub" claim). Emails can
-- change; sub never does, so it's the key for Google sign-in. It's NULL for
-- accounts created by DEV_LOGIN, which only exists outside production.
CREATE TABLE users (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    google_sub  TEXT        UNIQUE,
    email       TEXT        NOT NULL UNIQUE,
    name        TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ARCHITECTURAL DECISION: Why store a hash of the session token?
-- The browser holds the raw token. If this table leaks, hashes can't be
-- replayed as cookies. SHA-256 rather than bcrypt because the token is 256
-- random bits: there's nothing to brute-force, so a slow hash would only
-- slow down every authenticated request.
CREATE TABLE sessions (
    token_hash  BYTEA       PRIMARY KEY,
    user_id     UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at  TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Expired-session cleanup runs per user at login time.
CREATE INDEX idx_sessions_user_expires ON sessions (user_id, expires_at);
