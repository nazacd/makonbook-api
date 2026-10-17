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

-- The panel's grant: only ever a branch admin and/or editor, and never over a
-- super admin (then no row comes back). Super admins are managed on the server.
-- name: UpsertBranchAccess :one
INSERT INTO admin_access (member_type, member_id, name, super, office_ids, editor)
VALUES (@member_type, @member_id, @name, false, @office_ids::int[], @editor)
ON CONFLICT (member_type, member_id) DO UPDATE
SET office_ids = excluded.office_ids, editor = excluded.editor,
    name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE admin_access.name END
WHERE NOT admin_access.super
RETURNING *;

-- name: DeleteBranchAccess :execrows
DELETE FROM admin_access WHERE member_type = @member_type AND member_id = @member_id AND NOT super;

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

-- name: GetTeacher :one
SELECT fired, (first_name || ' ' || last_name)::text AS name FROM teachers WHERE id = @id;

-- Locks the super admins, so two changes can't both remove "the other" one.
-- name: LockSupers :many
SELECT member_type, member_id FROM admin_access WHERE super FOR UPDATE;

-- name: Offices :many
SELECT id, name FROM offices ORDER BY name;
