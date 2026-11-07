-- bcrypt hash of the current login password. At startup the app compares it with
-- STARTPAGE_PASSWORD; when they differ (the password was changed) it deletes every
-- session, so cookies issued under the old password stop working.
CREATE TABLE auth (
    id            smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    password_hash bytea       NOT NULL,
    updated_at    timestamptz NOT NULL DEFAULT now()
);
