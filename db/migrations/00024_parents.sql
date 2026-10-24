-- +goose Up
-- Parents (spec 12). There are no parent accounts: every student has a
-- permanent 5-digit parent code, and a parent who enters it in the app sees
-- that child read-only (classes, schedule, attendance) and can report a
-- problem to the admins. A code never changes once given.
CREATE TABLE parent_codes (
    student_client_id integer PRIMARY KEY REFERENCES students ON DELETE CASCADE,
    code              text NOT NULL UNIQUE CHECK (code ~ '^[0-9]{5}$'),
    created_at        timestamptz NOT NULL DEFAULT now()
);

-- A phone signed in with a child's code. Kept apart from auth_sessions, so a
-- parent's token can never reach a student's endpoints.
CREATE TABLE parent_sessions (
    id                bigserial PRIMARY KEY,
    token_hash        bytea NOT NULL UNIQUE,          -- sha256 of the bearer token
    student_client_id integer NOT NULL REFERENCES students ON DELETE CASCADE,
    device_id         text NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    last_seen_at      timestamptz NOT NULL DEFAULT now(),
    expires_at        timestamptz NOT NULL,
    revoked_at        timestamptz
);
CREATE INDEX parent_sessions_student ON parent_sessions (student_client_id);

-- "Report a problem": a parent's message about their child, for the admins of
-- the child's branches (admin panel, Telegram).
CREATE TABLE parent_reports (
    id                bigserial PRIMARY KEY,
    student_client_id integer NOT NULL REFERENCES students ON DELETE CASCADE,
    device_id         text NOT NULL,                  -- whose reports a phone lists
    group_id          integer REFERENCES groups ON DELETE SET NULL,  -- optional: which class it's about
    message           text NOT NULL CHECK (length(message) BETWEEN 1 AND 2000),
    lang              text NOT NULL CHECK (lang IN ('uz', 'ru')),
    created_at        timestamptz NOT NULL DEFAULT now(),
    handled_at        timestamptz,
    handled_by        text,                           -- the admin's name
    reply             text CHECK (length(reply) <= 1000),  -- what the admin wrote back, shown to the parent
    CHECK ((handled_at IS NULL) = (handled_by IS NULL))
);
CREATE INDEX parent_reports_open ON parent_reports (created_at DESC) WHERE handled_at IS NULL;
CREATE INDEX parent_reports_student ON parent_reports (student_client_id, created_at DESC);

-- +goose Down
DROP TABLE parent_reports;
DROP TABLE parent_sessions;
DROP TABLE parent_codes;
