-- name: GetCredential :one
SELECT person_kind, person_id, username, password_hash, password_changed_at, failed_attempts, locked_until
FROM credentials
WHERE person_kind = @person_kind AND person_id = @person_id;

-- name: GetCredentialByUsername :one
SELECT person_kind, person_id, username, password_hash, password_changed_at, failed_attempts, locked_until
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
