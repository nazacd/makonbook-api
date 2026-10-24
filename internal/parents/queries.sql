-- Parents (spec 12).

-- Codes ------------------------------------------------------------------------

-- name: StudentsWithoutCode :many
SELECT s.client_id FROM students s
WHERE NOT EXISTS (SELECT 1 FROM parent_codes c WHERE c.student_client_id = s.client_id)
ORDER BY s.client_id;

-- A code taken by another student (or the student already has one): nothing.
-- name: InsertCode :execrows
INSERT INTO parent_codes (student_client_id, code) VALUES (@student_client_id, @code)
ON CONFLICT DO NOTHING;

-- name: CodeOf :one
SELECT code FROM parent_codes WHERE student_client_id = @student_client_id;

-- name: StudentByCode :one
SELECT s.client_id, s.first_name, s.last_name, (s.deactivated_at IS NULL)::bool AS active
FROM parent_codes c JOIN students s ON s.client_id = c.student_client_id
WHERE c.code = @code;

-- Sessions ---------------------------------------------------------------------

-- name: InsertSession :exec
INSERT INTO parent_sessions (token_hash, student_client_id, device_id, expires_at)
VALUES (@token_hash, @student_client_id, @device_id, @expires_at);

-- A live session of an active student.
-- name: LiveSession :one
SELECT p.id, p.student_client_id, p.device_id, p.last_seen_at
FROM parent_sessions p JOIN students s ON s.client_id = p.student_client_id
WHERE p.token_hash = @token_hash AND p.revoked_at IS NULL AND p.expires_at > now() AND s.deactivated_at IS NULL;

-- name: TouchSession :exec
UPDATE parent_sessions SET last_seen_at = now(), expires_at = @expires_at WHERE id = @id;

-- name: RevokeSession :exec
UPDATE parent_sessions SET revoked_at = now() WHERE id = @id AND revoked_at IS NULL;

-- name: Child :one
SELECT client_id, first_name, last_name FROM students WHERE client_id = @client_id;

-- Reports ----------------------------------------------------------------------

-- name: InsertReport :one
INSERT INTO parent_reports (student_client_id, device_id, group_id, message, lang)
VALUES (@student_client_id, @device_id, @group_id, @message, @lang)
RETURNING id, created_at;

-- Whether the child is in the group (now or later), for a report about it.
-- name: InGroup :one
SELECT EXISTS (
    SELECT 1 FROM enrollments e JOIN groups g ON g.id = e.group_id
    WHERE e.student_client_id = @client_id AND e.group_id = @group_id AND g.removed_at IS NULL
      AND (e.end_date IS NULL OR e.end_date >= @today::date));

-- A phone's reports about the child, newest first.
-- name: DeviceReports :many
SELECT r.id, r.group_id, coalesce(g.name, '')::text AS group_name, r.message, r.created_at, r.handled_at, r.reply
FROM parent_reports r LEFT JOIN groups g ON g.id = r.group_id
WHERE r.student_client_id = @client_id AND r.device_id = @device_id
ORDER BY r.created_at DESC
LIMIT 50;

-- What the admins' Telegram message says about a report.
-- name: ReportContext :one
SELECT s.first_name, s.last_name, coalesce(g.name, '')::text AS group_name,
       coalesce((SELECT string_agg(DISTINCT o.name, ', ') FROM offices o
                 WHERE o.id = ANY (s.office_ids) OR o.id = g.office_id), '')::text AS offices
FROM students s LEFT JOIN groups g ON g.id = sqlc.narg('group_id')::int
WHERE s.client_id = @client_id;

-- Admin panel: reports about students of the admin's branches (by the
-- student's HolliHop offices or their groups' branches).
-- name: AdminReports :many
SELECT r.id, r.student_client_id, s.first_name, s.last_name, r.group_id, coalesce(g.name, '')::text AS group_name,
       r.message, r.lang, r.created_at, r.handled_at, r.handled_by, r.reply
FROM parent_reports r
JOIN students s ON s.client_id = r.student_client_id
LEFT JOIN groups g ON g.id = r.group_id
WHERE (@handled::bool = (r.handled_at IS NOT NULL))
  AND (@all_offices::bool
       OR s.office_ids && @office_ids::int[]
       OR EXISTS (SELECT 1 FROM enrollments e JOIN groups eg ON eg.id = e.group_id
                  WHERE e.student_client_id = s.client_id AND eg.office_id = ANY (@office_ids::int[])))
ORDER BY r.created_at DESC
LIMIT 200;

-- name: MarkHandled :execrows
UPDATE parent_reports r SET handled_at = now(), handled_by = @handled_by, reply = sqlc.narg('reply')
FROM students s
WHERE r.id = @id AND s.client_id = r.student_client_id AND r.handled_at IS NULL
  AND (@all_offices::bool
       OR s.office_ids && @office_ids::int[]
       OR EXISTS (SELECT 1 FROM enrollments e JOIN groups eg ON eg.id = e.group_id
                  WHERE e.student_client_id = s.client_id AND eg.office_id = ANY (@office_ids::int[])));

-- Admin panel: active students of the admin's branches by name, with their
-- parent codes (for a parent who asks the office).
-- name: SearchCodes :many
SELECT s.client_id, s.first_name, s.last_name, c.code,
       coalesce((SELECT string_agg(g.name, ', ' ORDER BY g.name) FROM enrollments e JOIN groups g ON g.id = e.group_id
                 WHERE e.student_client_id = s.client_id AND g.removed_at IS NULL
                   AND (e.end_date IS NULL OR e.end_date >= @today::date)), '')::text AS groups
FROM students s JOIN parent_codes c ON c.student_client_id = s.client_id
WHERE s.deactivated_at IS NULL
  AND (s.first_name || ' ' || s.last_name || ' ' || s.last_name || ' ' || s.first_name) ILIKE '%' || @q::text || '%'
  AND (@all_offices::bool
       OR s.office_ids && @office_ids::int[]
       OR EXISTS (SELECT 1 FROM enrollments e JOIN groups eg ON eg.id = e.group_id
                  WHERE e.student_client_id = s.client_id AND eg.office_id = ANY (@office_ids::int[])))
ORDER BY s.last_name, s.first_name
LIMIT 30;
