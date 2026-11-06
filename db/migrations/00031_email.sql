-- +goose Up
-- Sign-up starts with an email (proved by a code sent to it) or Google; the
-- phone comes after (spec 21). App-owned, like the rest of credentials.

-- The person's email, lower case: a sign-in identifier like the username.
-- One person per address; someone who left gives theirs up when another
-- person proves it (auth.linkEmail).
ALTER TABLE credentials ADD COLUMN email text UNIQUE CHECK (email = lower(email));

-- Codes sent by email: the rules of phone_codes (hashed, 10 minutes, 5
-- tries, per address and per network limits).
CREATE TABLE email_codes (
    id         bigserial PRIMARY KEY,
    email      text NOT NULL,                       -- lower case
    salt       bytea NOT NULL,
    code_hash  bytea NOT NULL,                      -- sha256(salt || code)
    ip         text NOT NULL,
    tries      smallint NOT NULL DEFAULT 0 CHECK (tries BETWEEN 0 AND 5),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz
);
CREATE INDEX email_codes_email_idx ON email_codes (email, created_at DESC);
CREATE INDEX email_codes_ip_idx ON email_codes (ip, created_at DESC);

-- 'email': a proved address (an identity to sign up with, like
-- 'google_signup'). identity: on a sign-up phone ticket, the identity ticket
-- it was verified with (sign-up must use the same). revealed_phone: on an
-- identity ticket, the one phone whose owner verifying it has shown.
ALTER TABLE auth_tickets DROP CONSTRAINT auth_tickets_purpose_check;
ALTER TABLE auth_tickets
    ADD CONSTRAINT auth_tickets_purpose_check CHECK (purpose IN ('signup', 'reset', 'google', 'google_signup', 'email')),
    ADD COLUMN identity bytea,
    ADD COLUMN revealed_phone text;

-- +goose Down
DELETE FROM auth_tickets WHERE purpose = 'email';
ALTER TABLE auth_tickets DROP COLUMN revealed_phone, DROP COLUMN identity, DROP CONSTRAINT auth_tickets_purpose_check;
ALTER TABLE auth_tickets ADD CONSTRAINT auth_tickets_purpose_check CHECK (purpose IN ('signup', 'reset', 'google', 'google_signup'));
DROP TABLE email_codes;
ALTER TABLE credentials DROP COLUMN email;
