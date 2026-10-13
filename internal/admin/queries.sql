-- Admin access list and sessions (spec 13).

-- name: GetAccess :one
SELECT * FROM admin_access WHERE member_type = @member_type AND member_id = @member_id;

-- name: ListAccess :many
SELECT * FROM admin_access ORDER BY super DESC, name, member_id;

-- name: UpsertAccess :one
INSERT INTO admin_access (member_type, member_id, name, super, office_ids)
VALUES (@member_type, @member_id, @name, @super, @office_ids::int[])
ON CONFLICT (member_type, member_id) DO UPDATE
SET super = excluded.super, office_ids = excluded.office_ids,
    name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE admin_access.name END
RETURNING *;

-- name: DeleteAccess :execrows
DELETE FROM admin_access WHERE member_type = @member_type AND member_id = @member_id;

-- name: RecordLogin :exec
UPDATE admin_access SET name = @name, last_login_at = @at
WHERE member_type = @member_type AND member_id = @member_id;

-- name: CreateSession :exec
INSERT INTO admin_sessions (token_hash, member_type, member_id, created_at, expires_at)
VALUES (@token_hash, @member_type, @member_id, @created_at, @expires_at);

-- A live session of someone still on the access list.
-- name: SessionAccess :one
SELECT a.*
FROM admin_sessions s
JOIN admin_access a ON a.member_type = s.member_type AND a.member_id = s.member_id
WHERE s.token_hash = @token_hash AND s.revoked_at IS NULL AND s.expires_at > @now;

-- name: RevokeSession :exec
UPDATE admin_sessions SET revoked_at = @at WHERE token_hash = @token_hash AND revoked_at IS NULL;

-- name: TeacherFired :one
SELECT fired FROM teachers WHERE id = @id;

-- name: Offices :many
SELECT id, name FROM offices ORDER BY name;
