-- A free-form scratchpad: one row, like settings.
CREATE TABLE notes (
    id         smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    body       text        NOT NULL DEFAULT '' CHECK (char_length(body) <= 10000),
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO notes (id) VALUES (1);

-- A to-do list. done_at is the single source of truth for "done", and it orders
-- finished tasks newest first.
CREATE TABLE tasks (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title      text        NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),
    done_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
