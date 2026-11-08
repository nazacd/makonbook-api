-- +goose Up
-- Accounts, reworked (spec 21, D31): people sign in or sign up with an email
-- code, Google or Telegram (OpenID Connect, which can share a verified
-- phone); a phone is added afterwards (SMS code or Telegram), and proving a
-- number someone else has merges the two accounts. The tables of the
-- earlier flows (telegram_phone_checks, the identity columns of
-- auth_tickets) stay, unused.

-- Profile photos, copied from Google or Telegram (never hotlinked): served
-- at /v1/photos/{id}.
CREATE TABLE photos (
    id           text PRIMARY KEY,                -- random, in the URL
    content_type text NOT NULL CHECK (content_type IN ('image/jpeg', 'image/png', 'image/webp')),
    data         bytea NOT NULL CHECK (length(data) <= 524288),
    created_at   timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE credentials ADD COLUMN photo_id text REFERENCES photos ON DELETE SET NULL;

-- A person may have several Google accounts (merged accounts keep theirs).
ALTER TABLE google_links DROP CONSTRAINT google_links_person_kind_person_id_key;
CREATE INDEX google_links_person_idx ON google_links (person_kind, person_id);

-- Telegram's OpenID Connect flow: what /v1/auth/telegram/start keeps for the
-- callback, under the SHA-256 of the state, for 10 minutes.
CREATE TABLE oauth_states (
    state_hash  bytea PRIMARY KEY,
    purpose     text NOT NULL CHECK (purpose IN ('signin', 'verify')),
    verifier    text NOT NULL,                    -- PKCE
    person_kind text,                             -- verify: who started it
    person_id   integer,
    return_path text NOT NULL DEFAULT '/',
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL,
    used_at     timestamptz,
    CHECK ((purpose = 'verify') = (person_id IS NOT NULL))
);

-- Tickets back from Telegram ('telegram_signin', 'telegram_verify'): the
-- Telegram account, the phone it shared (if any), its photo, and for verify
-- the person it belongs to. Google tickets keep the photo too.
ALTER TABLE auth_tickets DROP CONSTRAINT auth_tickets_purpose_check;
ALTER TABLE auth_tickets DROP CONSTRAINT auth_tickets_telegram_check;
ALTER TABLE auth_tickets
    ADD CONSTRAINT auth_tickets_purpose_check CHECK (purpose IN (
        'signup', 'reset', 'google', 'google_signup', 'email', 'telegram_signup', 'telegram_signin', 'telegram_verify')),
    ADD CONSTRAINT auth_tickets_telegram_check
        CHECK ((telegram_id IS NOT NULL) = (purpose IN ('telegram_signup', 'telegram_signin', 'telegram_verify'))),
    ADD COLUMN shared_phone text,                 -- E.164, as Telegram shared it
    ADD COLUMN photo_url text,
    ADD COLUMN person_kind text,
    ADD COLUMN person_id integer;

-- A signed-in person proving a number to add it.
ALTER TABLE phone_codes DROP CONSTRAINT phone_codes_purpose_check;
ALTER TABLE phone_codes ADD CONSTRAINT phone_codes_purpose_check CHECK (purpose IN ('signup', 'reset', 'verify'));

-- +goose Down
DELETE FROM phone_codes WHERE purpose = 'verify';
ALTER TABLE phone_codes DROP CONSTRAINT phone_codes_purpose_check;
ALTER TABLE phone_codes ADD CONSTRAINT phone_codes_purpose_check CHECK (purpose IN ('signup', 'reset'));
DELETE FROM auth_tickets WHERE purpose IN ('telegram_signin', 'telegram_verify');
ALTER TABLE auth_tickets DROP COLUMN person_id, DROP COLUMN person_kind, DROP COLUMN photo_url, DROP COLUMN shared_phone,
    DROP CONSTRAINT auth_tickets_telegram_check, DROP CONSTRAINT auth_tickets_purpose_check;
ALTER TABLE auth_tickets
    ADD CONSTRAINT auth_tickets_purpose_check
        CHECK (purpose IN ('signup', 'reset', 'google', 'google_signup', 'email', 'telegram_signup')),
    ADD CONSTRAINT auth_tickets_telegram_check CHECK ((telegram_id IS NOT NULL) = (purpose = 'telegram_signup'));
DROP TABLE oauth_states;
DROP INDEX google_links_person_idx;
DELETE FROM google_links g WHERE EXISTS (
    SELECT 1 FROM google_links o WHERE o.person_kind = g.person_kind AND o.person_id = g.person_id AND o.created_at < g.created_at);
ALTER TABLE google_links ADD CONSTRAINT google_links_person_kind_person_id_key UNIQUE (person_kind, person_id);
ALTER TABLE credentials DROP COLUMN photo_id;
DROP TABLE photos;
