-- Only the SHA-256 of each session token is stored, never the token itself.
CREATE TABLE sessions (
    token_hash bytea       PRIMARY KEY CHECK (octet_length(token_hash) = 32),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);

CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);
