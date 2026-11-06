-- +goose Up
-- Accounts (spec 21): sign-up and password reset with a code sent by SMS, and
-- Google sign-in. App-owned, like credentials; the sync never touches them.

-- Codes sent by SMS: 6 digits, stored hashed, 10 minutes, 5 tries. Only a
-- phone's latest code for a purpose counts. A reset for a number nobody signs
-- in with sends nothing but still stores a row (code_hash NULL), so the
-- answers and the limits are the same either way.
CREATE TABLE phone_codes (
    id         bigserial PRIMARY KEY,
    phone      text NOT NULL,                       -- E.164
    purpose    text NOT NULL CHECK (purpose IN ('signup', 'reset')),
    salt       bytea NOT NULL,
    code_hash  bytea,                               -- sha256(salt || code); NULL: nothing was sent
    ip         text NOT NULL,                       -- the network that asked (limits)
    tries      smallint NOT NULL DEFAULT 0 CHECK (tries BETWEEN 0 AND 5),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz                          -- the right code was entered
);
CREATE INDEX phone_codes_phone_idx ON phone_codes (phone, created_at DESC);
CREATE INDEX phone_codes_ip_idx ON phone_codes (ip, created_at DESC);

-- Single-use proofs given to the web: a phone verified for sign-up or reset
-- (15 minutes), a Google account just back from Google ('google', 10
-- minutes) and one waiting for a phone to sign up with ('google_signup',
-- 30 minutes). Stored as the SHA-256 of the random ticket, like sessions.
CREATE TABLE auth_tickets (
    token_hash bytea PRIMARY KEY,
    purpose    text NOT NULL CHECK (purpose IN ('signup', 'reset', 'google', 'google_signup')),
    phone      text,                                -- E.164, for 'signup' and 'reset'
    google_sub text,                                -- the Google account, for the others
    email      text,
    first_name text,                                -- as Google has them
    last_name  text,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    CHECK ((phone IS NOT NULL) = (purpose IN ('signup', 'reset'))),
    CHECK ((google_sub IS NOT NULL) = (purpose IN ('google', 'google_signup')))
);
CREATE INDEX auth_tickets_expires_idx ON auth_tickets (expires_at);

-- The Google account a person signs in with: at most one each way. No
-- foreign key (students or teachers); when a self-registered student enrols,
-- auth.MoveStudent moves it with them.
CREATE TABLE google_links (
    sub         text PRIMARY KEY,                   -- Google's id for the account
    person_kind text NOT NULL CHECK (person_kind IN ('student', 'teacher')),
    person_id   integer NOT NULL,
    email       text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (person_kind, person_id)
);

-- +goose Down
DROP TABLE google_links;
DROP TABLE auth_tickets;
DROP TABLE phone_codes;
