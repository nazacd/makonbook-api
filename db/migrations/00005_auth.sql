-- +goose Up
-- App-owned login data (spec 7). The HolliHop sync never touches these tables.

-- One row per person who has set a password or username, or failed a login.
-- No row (or a password set before the person's activated_at) means the
-- default password applies: the last 4 digits of their login phone.
CREATE TABLE credentials (
    person_kind         text NOT NULL CHECK (person_kind IN ('student', 'teacher')),
    person_id           integer NOT NULL,
    username            text UNIQUE,                -- lower case
    password_hash       text,                       -- argon2id, PHC format
    password_changed_at timestamptz,
    failed_attempts     integer NOT NULL DEFAULT 0, -- since the last successful login
    locked_until        timestamptz,
    PRIMARY KEY (person_kind, person_id)
);

CREATE TABLE auth_sessions (
    id           bigserial PRIMARY KEY,
    token_hash   bytea NOT NULL UNIQUE,             -- sha256 of the bearer token
    person_kind  text NOT NULL,
    person_id    integer NOT NULL,
    device_id    text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    revoked_at   timestamptz
);
CREATE INDEX auth_sessions_person_idx ON auth_sessions (person_kind, person_id);

-- +goose Down
DROP TABLE auth_sessions;
DROP TABLE credentials;
