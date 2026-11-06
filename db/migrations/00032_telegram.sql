-- +goose Up
-- Telegram (spec 21): a third identity to sign up and sign in with (the
-- Login Widget), and a free way to prove a phone (the sign-in bot asks for
-- the person's own contact). App-owned; the sync never touches these.

-- The Telegram account a person signs in with: one each way. No foreign key
-- (students or teachers); auth.MoveStudent moves it with a student who enrols.
CREATE TABLE telegram_links (
    telegram_id bigint PRIMARY KEY,                 -- Telegram's user id
    person_kind text NOT NULL CHECK (person_kind IN ('student', 'teacher')),
    person_id   integer NOT NULL,
    username    text,                               -- without @, as last seen
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (person_kind, person_id)
);

-- 'telegram_signup': a Telegram account waiting for a phone (an identity,
-- like 'google_signup'), with its id and username.
ALTER TABLE auth_tickets DROP CONSTRAINT auth_tickets_purpose_check;
ALTER TABLE auth_tickets
    ADD CONSTRAINT auth_tickets_purpose_check
        CHECK (purpose IN ('signup', 'reset', 'google', 'google_signup', 'email', 'telegram_signup')),
    ADD COLUMN telegram_id bigint,
    ADD COLUMN telegram_username text,
    ADD CONSTRAINT auth_tickets_telegram_check CHECK ((telegram_id IS NOT NULL) = (purpose = 'telegram_signup'));

-- A phone check through the bot: the web gets a nonce and a t.me link; the
-- person opens the bot (/start <nonce>, which ties the check to their
-- Telegram account) and shares their own contact; the web then trades the
-- nonce for a phone ticket, once.
CREATE TABLE telegram_phone_checks (
    nonce_hash       bytea PRIMARY KEY,             -- sha256 of the nonce
    purpose          text NOT NULL CHECK (purpose IN ('signup', 'reset')),
    identity         bytea,                         -- signup: the identity ticket's hash
    identity_purpose text,
    ip               text NOT NULL,
    telegram_user_id bigint,                        -- who opened the link
    phone            text,                          -- E.164, once shared
    created_at       timestamptz NOT NULL DEFAULT now(),
    expires_at       timestamptz NOT NULL,
    verified_at      timestamptz,
    used_at          timestamptz,
    CHECK ((identity IS NULL) = (purpose = 'reset')),
    CHECK ((phone IS NULL) = (verified_at IS NULL))
);
CREATE INDEX telegram_phone_checks_user_idx ON telegram_phone_checks (telegram_user_id, created_at DESC);
CREATE INDEX telegram_phone_checks_ip_idx ON telegram_phone_checks (ip, created_at DESC);

-- +goose Down
DROP TABLE telegram_phone_checks;
DELETE FROM auth_tickets WHERE purpose = 'telegram_signup';
ALTER TABLE auth_tickets DROP CONSTRAINT auth_tickets_telegram_check, DROP COLUMN telegram_username,
    DROP COLUMN telegram_id, DROP CONSTRAINT auth_tickets_purpose_check;
ALTER TABLE auth_tickets ADD CONSTRAINT auth_tickets_purpose_check
    CHECK (purpose IN ('signup', 'reset', 'google', 'google_signup', 'email'));
DROP TABLE telegram_links;
