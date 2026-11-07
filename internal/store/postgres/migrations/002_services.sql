CREATE TABLE services (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 60),
    url        text        NOT NULL CHECK (char_length(url) BETWEEN 1 AND 2048),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE service_checks (
    service_id  bigint      NOT NULL REFERENCES services (id) ON DELETE CASCADE,
    checked_at  timestamptz NOT NULL,
    ok          boolean     NOT NULL,
    status_code integer,
    latency_ms  integer     NOT NULL CHECK (latency_ms >= 0),
    error       text,
    PRIMARY KEY (service_id, checked_at)
);

-- The primary key serves "latest check per service" and "checks of a service since T";
-- this index serves retention deletes across all services.
CREATE INDEX service_checks_checked_at_idx ON service_checks (checked_at);
