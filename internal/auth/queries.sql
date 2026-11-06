-- name: GetCredential :one
SELECT person_kind, person_id, username, password_hash, password_changed_at, failed_attempts, locked_until, email
FROM credentials
WHERE person_kind = @person_kind AND person_id = @person_id;

-- name: GetCredentialByUsername :one
SELECT person_kind, person_id, username, password_hash, password_changed_at, failed_attempts, locked_until, email
FROM credentials
WHERE username = @username;

-- Counts a failed login and, on every 5th consecutive failure, locks the
-- account for 15 min × 2^(lockouts-1), capped at 24 h (spec 7). Escalation is
-- what makes a 4-digit default password impractical to guess.
-- name: RecordLoginFailure :one
INSERT INTO credentials (person_kind, person_id, failed_attempts)
VALUES (@person_kind, @person_id, 1)
ON CONFLICT (person_kind, person_id) DO UPDATE
SET failed_attempts = credentials.failed_attempts + 1,
    locked_until = CASE
        WHEN (credentials.failed_attempts + 1) % 5 = 0 THEN now() + least(
            interval '15 minutes' * power(2, (credentials.failed_attempts + 1) / 5 - 1),
            interval '24 hours')
        ELSE credentials.locked_until
    END
RETURNING failed_attempts, locked_until;

-- name: ResetLoginFailures :exec
UPDATE credentials
SET failed_attempts = 0, locked_until = NULL
WHERE person_kind = @person_kind AND person_id = @person_id AND (failed_attempts <> 0 OR locked_until IS NOT NULL);

-- name: SetPassword :exec
INSERT INTO credentials (person_kind, person_id, password_hash, password_changed_at)
VALUES (@person_kind, @person_id, @password_hash, now())
ON CONFLICT (person_kind, person_id) DO UPDATE
SET password_hash = EXCLUDED.password_hash,
    password_changed_at = EXCLUDED.password_changed_at,
    failed_attempts = 0,
    locked_until = NULL;

-- name: ResetPasswordToDefault :execrows
UPDATE credentials
SET password_hash = NULL, password_changed_at = NULL, failed_attempts = 0, locked_until = NULL
WHERE person_kind = @person_kind AND person_id = @person_id;

-- name: SetUsername :exec
INSERT INTO credentials (person_kind, person_id, username)
VALUES (@person_kind, @person_id, @username)
ON CONFLICT (person_kind, person_id) DO UPDATE
SET username = EXCLUDED.username;

-- name: CreateSession :one
INSERT INTO auth_sessions (token_hash, person_kind, person_id, device_id, expires_at)
VALUES (@token_hash, @person_kind, @person_id, @device_id, @expires_at)
RETURNING id, created_at;

-- A live session together with the person's credentials row (if any), so a
-- request needs one auth query.
-- name: GetLiveSession :one
SELECT s.id, s.person_kind, s.person_id, s.created_at, s.last_seen_at,
       c.password_hash, c.password_changed_at, c.username
FROM auth_sessions s
LEFT JOIN credentials c ON c.person_kind = s.person_kind AND c.person_id = s.person_id
WHERE s.token_hash = @token_hash AND s.revoked_at IS NULL AND s.expires_at > now();

-- Sliding expiry: called at most once an hour per session.
-- name: TouchSession :exec
UPDATE auth_sessions
SET last_seen_at = now(), expires_at = @expires_at
WHERE id = @id;

-- name: RevokeSession :exec
UPDATE auth_sessions SET revoked_at = now()
WHERE id = @id AND revoked_at IS NULL;

-- name: RevokeOtherSessions :execrows
UPDATE auth_sessions SET revoked_at = now()
WHERE person_kind = @person_kind AND person_id = @person_id AND id <> @keep_id AND revoked_at IS NULL;

-- name: RevokeAllSessions :execrows
UPDATE auth_sessions SET revoked_at = now()
WHERE person_kind = @person_kind AND person_id = @person_id AND revoked_at IS NULL;

-- Enrolment (spec 21): a self-registered student re-keyed to their HolliHop
-- ClientId keeps their password, username, lockout and sessions.
-- name: MoveStudentCredentials :exec
UPDATE credentials SET person_id = @to_id
WHERE person_kind = 'student' AND person_id = @from_id;

-- name: MoveStudentSessions :exec
UPDATE auth_sessions SET person_id = @to_id
WHERE person_kind = 'student' AND person_id = @from_id;

-- name: MoveStudentGoogle :exec
UPDATE google_links SET person_id = @to_id
WHERE person_kind = 'student' AND person_id = @from_id;

-- Accounts (spec 21) ------------------------------------------------------------
-- Times come from the service's clock (@now), so tests can move it.

-- Taken for the limit checks and the insert of a phone's code, so two
-- requests at once can't both pass the limits.
-- name: LockPhone :exec
SELECT pg_advisory_xact_lock(hashtextextended('phone_codes:' || @phone::text, 0));

-- The phone's codes since a moment, oldest first (per-phone limits).
-- name: PhoneCodesSince :many
SELECT created_at FROM phone_codes
WHERE phone = @phone AND created_at > @since::timestamptz
ORDER BY created_at;

-- A network's codes since a moment, oldest first (per-network limit).
-- name: NetworkCodesSince :many
SELECT created_at FROM phone_codes
WHERE ip = @ip AND created_at > @since::timestamptz
ORDER BY created_at;

-- name: InsertPhoneCode :exec
INSERT INTO phone_codes (phone, purpose, salt, code_hash, ip, created_at, expires_at)
VALUES (@phone, @purpose, @salt, @code_hash, @ip, @created_at, @expires_at);

-- Only the latest code for a phone and purpose counts.
-- name: LatestPhoneCode :one
SELECT id, salt, code_hash, tries, created_at, expires_at, used_at
FROM phone_codes
WHERE phone = @phone AND purpose = @purpose
ORDER BY id DESC
LIMIT 1;

-- Counts a try before the code is compared; no row once all 5 are used.
-- name: CountPhoneCodeTry :one
UPDATE phone_codes SET tries = tries + 1
WHERE id = @id AND tries < 5 AND used_at IS NULL
RETURNING tries;

-- name: UsePhoneCode :execrows
UPDATE phone_codes SET used_at = @now::timestamptz
WHERE id = @id AND used_at IS NULL;

-- name: InsertTicket :exec
INSERT INTO auth_tickets (token_hash, purpose, phone, google_sub, email, first_name, last_name, identity, created_at, expires_at)
VALUES (@token_hash, @purpose, @phone, @google_sub, @email, @first_name, @last_name, @identity, @created_at, @expires_at);

-- A ticket that can still be used, without using it (to check a request
-- before changing anything).
-- name: PeekTicket :one
SELECT phone, google_sub, email, first_name, last_name, identity, revealed_phone
FROM auth_tickets
WHERE token_hash = @token_hash AND purpose = @purpose AND used_at IS NULL AND expires_at > @now::timestamptz;

-- name: UseTicket :one
UPDATE auth_tickets SET used_at = @now::timestamptz
WHERE token_hash = @token_hash AND purpose = @purpose AND used_at IS NULL AND expires_at > @now::timestamptz
RETURNING phone, google_sub, email, first_name, last_name, identity, revealed_phone;

-- name: DeleteOldPhoneCodes :execrows
DELETE FROM phone_codes WHERE created_at < @before::timestamptz;

-- name: DeleteOldTickets :execrows
DELETE FROM auth_tickets WHERE expires_at < @before::timestamptz;

-- name: GoogleLinkBySub :one
SELECT person_kind, person_id, email FROM google_links WHERE sub = @sub;

-- name: HasGoogleLink :one
SELECT EXISTS (SELECT 1 FROM google_links WHERE person_kind = @person_kind AND person_id = @person_id);

-- name: DeleteGoogleLink :exec
DELETE FROM google_links WHERE sub = @sub;

-- A person links one Google account: a new one replaces the old.
-- name: UnlinkGooglePerson :exec
DELETE FROM google_links WHERE person_kind = @person_kind AND person_id = @person_id;

-- name: InsertGoogleLink :exec
INSERT INTO google_links (sub, person_kind, person_id, email)
VALUES (@sub, @person_kind, @person_id, @email);

-- name: SetGoogleEmail :exec
UPDATE google_links SET email = @email WHERE sub = @sub AND email <> @email;

-- An identity ticket shows the owner of one phone at most (spec 21.3): it
-- records that phone, and refuses another.
-- name: RevealPhone :execrows
UPDATE auth_tickets SET revealed_phone = @phone::text
WHERE token_hash = @token_hash AND purpose = @purpose AND used_at IS NULL
  AND (revealed_phone IS NULL OR revealed_phone = @phone::text);

-- Emails ------------------------------------------------------------------------

-- name: LockEmail :exec
SELECT pg_advisory_xact_lock(hashtextextended('email_codes:' || @email::text, 0));

-- name: EmailCodesSince :many
SELECT created_at FROM email_codes
WHERE email = @email AND created_at > @since::timestamptz
ORDER BY created_at;

-- name: NetworkEmailCodesSince :many
SELECT created_at FROM email_codes
WHERE ip = @ip AND created_at > @since::timestamptz
ORDER BY created_at;

-- name: InsertEmailCode :exec
INSERT INTO email_codes (email, salt, code_hash, ip, created_at, expires_at)
VALUES (@email, @salt, @code_hash, @ip, @created_at, @expires_at);

-- name: LatestEmailCode :one
SELECT id, salt, code_hash, tries, created_at, expires_at, used_at
FROM email_codes WHERE email = @email
ORDER BY id DESC LIMIT 1;

-- name: CountEmailCodeTry :one
UPDATE email_codes SET tries = tries + 1
WHERE id = @id AND tries < 5 AND used_at IS NULL
RETURNING tries;

-- name: UseEmailCode :execrows
UPDATE email_codes SET used_at = @now::timestamptz
WHERE id = @id AND used_at IS NULL;

-- name: DeleteOldEmailCodes :execrows
DELETE FROM email_codes WHERE created_at < @before::timestamptz;

-- Who has an email (active or not: the caller checks).
-- name: EmailOwner :one
SELECT person_kind, person_id FROM credentials WHERE email = @email;

-- name: ClearEmail :exec
UPDATE credentials SET email = NULL WHERE email = @email;

-- A person's email (one each: a new one replaces theirs).
-- name: SetEmail :exec
INSERT INTO credentials (person_kind, person_id, email)
VALUES (@person_kind, @person_id, @email)
ON CONFLICT (person_kind, person_id) DO UPDATE SET email = EXCLUDED.email;
