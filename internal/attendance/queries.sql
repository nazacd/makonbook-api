-- Attendance sessions, marks and their audit log (spec 8).

-- name: SessionByLesson :one
SELECT * FROM attendance_sessions WHERE group_id = @group_id AND date = @date;

-- Returns no row if another request created the session first.
-- name: CreateSession :one
INSERT INTO attendance_sessions (group_id, date, teacher_id, secret, starts_at, ends_at, opened_at)
VALUES (@group_id, @date, @teacher_id, @secret, @starts_at, @ends_at, @opened_at)
ON CONFLICT (group_id, date) DO NOTHING
RETURNING *;

-- The roster (spec 5): enrollments current on the date and that weekday,
-- of active students.
-- name: InsertRoster :execrows
INSERT INTO attendance_marks (session_id, student_client_id, updated_at)
SELECT DISTINCT @session_id::uuid, e.student_client_id, @at::timestamptz
FROM enrollments e
JOIN students st ON st.client_id = e.student_client_id AND st.deactivated_at IS NULL
WHERE e.group_id = @group_id
  AND (e.begin_date IS NULL OR e.begin_date <= @date::date)
  AND (e.end_date IS NULL OR e.end_date >= @date::date)
  AND (e.weekdays = 0 OR e.weekdays & (1 << (extract(isodow FROM @date::date)::int - 1)) <> 0)
ON CONFLICT DO NOTHING;

-- name: GetSession :one
SELECT s.*, g.name AS group_name
FROM attendance_sessions s
JOIN groups g ON g.id = s.group_id
WHERE s.id = @id;

-- name: SessionMarks :many
SELECT m.student_client_id, m.state, m.source, m.scanned_at, st.first_name, st.last_name
FROM attendance_marks m
JOIN students st ON st.client_id = m.student_client_id
WHERE m.session_id = @session_id
ORDER BY st.last_name, st.first_name, m.student_client_id;

-- name: GetMarkForUpdate :one
SELECT state, source, device_id FROM attendance_marks
WHERE session_id = @session_id AND student_client_id = @student_client_id
FOR UPDATE;

-- A scan only fills a pending mark of a session that is still open.
-- name: ScanMark :execrows
UPDATE attendance_marks m
SET state = @state, source = 'scan', scanned_at = @at, device_id = @device_id, updated_at = @at
FROM attendance_sessions s
WHERE s.id = m.session_id AND s.finished_at IS NULL
  AND m.session_id = @session_id AND m.student_client_id = @student_client_id AND m.state = 'pending';

-- name: TeacherSetMark :exec
UPDATE attendance_marks
SET state = @state, source = 'teacher', updated_at = @at
WHERE session_id = @session_id AND student_client_id = @student_client_id;

-- name: FinishSession :execrows
UPDATE attendance_sessions SET finished_at = @at, finished_by = @finished_by
WHERE id = @id AND finished_at IS NULL;

-- name: MarkPendingAbsent :many
UPDATE attendance_marks SET state = 'absent', source = 'auto', updated_at = @at
WHERE session_id = @session_id AND state = 'pending'
RETURNING student_client_id;

-- name: InsertAudit :exec
INSERT INTO attendance_audit (session_id, student_client_id, actor_kind, actor_id, from_state, to_state, at)
VALUES (@session_id, @student_client_id, @actor_kind, @actor_id, @from_state, @to_state, @at);

-- name: InsertAbsentAudit :exec
INSERT INTO attendance_audit (session_id, student_client_id, actor_kind, from_state, to_state, at)
SELECT @session_id::uuid, unnest(@student_client_ids::int[]), 'system', 'pending', 'absent', @at::timestamptz;

-- Open sessions whose lesson ended before cutoff: they finish on their own.
-- name: DueSessions :many
SELECT id FROM attendance_sessions WHERE finished_at IS NULL AND ends_at < @cutoff;

-- HolliHop write-back (spec 8.6) ---------------------------------------------

-- Every mark of a session that just finished.
-- name: EnqueueSession :exec
INSERT INTO hollihop_outbox (session_id, group_id, student_client_id, date, pass, created_at, next_attempt_at)
SELECT m.session_id, s.group_id, m.student_client_id, s.date, m.state = 'absent', @at::timestamptz, @at::timestamptz
FROM attendance_marks m
JOIN attendance_sessions s ON s.id = m.session_id
WHERE m.session_id = @session_id AND m.state <> 'pending';

-- A correction after the session finished replaces anything not yet sent.
-- name: SupersedePending :exec
UPDATE hollihop_outbox SET status = 'superseded', done_at = @at
WHERE session_id = @session_id AND student_client_id = @student_client_id AND status = 'pending';

-- name: EnqueueMark :exec
INSERT INTO hollihop_outbox (session_id, group_id, student_client_id, date, pass, created_at, next_attempt_at)
SELECT s.id, s.group_id, @student_client_id, s.date, @pass, @at::timestamptz, @at::timestamptz
FROM attendance_sessions s WHERE s.id = @session_id;

-- Read under a share lock so a correction and a concurrent finish serialise:
-- either the correction lands before the finish enqueues the session, or it
-- sees the session finished and enqueues itself.
-- name: IsFinished :one
SELECT (finished_at IS NOT NULL)::bool AS finished FROM attendance_sessions WHERE id = @id FOR SHARE;

-- Takes due rows for one batch: they are leased (next_attempt_at pushed to
-- lease_until) so a crashed or concurrent worker doesn't send them twice.
-- name: ClaimDue :many
UPDATE hollihop_outbox o SET next_attempt_at = @lease_until::timestamptz, attempts = o.attempts + 1
WHERE o.id IN (
    SELECT i.id FROM hollihop_outbox i
    WHERE i.status = 'pending' AND i.next_attempt_at <= @due_before::timestamptz
    ORDER BY i.id
    LIMIT @batch_size
    FOR UPDATE SKIP LOCKED)
RETURNING o.id, o.group_id, o.student_client_id, o.date, o.pass, o.attempts;

-- name: CompleteRow :exec
UPDATE hollihop_outbox SET status = @status, done_at = @at, last_error = sqlc.narg('last_error')
WHERE id = @id AND status = 'pending';

-- name: RetryRow :exec
UPDATE hollihop_outbox SET next_attempt_at = @retry_at::timestamptz, last_error = @last_error
WHERE id = @id AND status = 'pending';

-- HolliHop's attendance, read-only (spec 8.7) --------------------------------

-- name: DeleteRecordsBetween :exec
DELETE FROM attendance_records WHERE date BETWEEN @date_from::date AND @date_to::date;

-- Rows for students MakonBook doesn't store (never active) are skipped.
-- name: InsertRecord :batchexec
INSERT INTO attendance_records (group_id, student_client_id, date, pass)
SELECT @group_id::int, @client_id::int, @day::date, @pass::bool
WHERE EXISTS (SELECT 1 FROM students st WHERE st.client_id = @client_id::int)
ON CONFLICT (student_client_id, group_id, date) DO UPDATE SET pass = excluded.pass;

-- A student's table: HolliHop's marks before today, plus the app's own marks
-- (late, and today's lesson before HolliHop has it). HolliHop wins where both
-- exist, except that the app adds "late" to a lesson HolliHop counts attended.
-- name: StudentAttendance :many
WITH app AS (
    SELECT s.group_id, s.date, m.state
    FROM attendance_marks m
    JOIN attendance_sessions s ON s.id = m.session_id
    WHERE m.student_client_id = @client_id::int AND m.state <> 'pending'
      AND s.date BETWEEN @date_from::date AND @today::date
)
SELECT coalesce(r.group_id, app.group_id)::int AS group_id,
       coalesce(r.date, app.date)::date AS date,
       (CASE
            WHEN r.pass IS NULL THEN app.state
            WHEN r.pass THEN 'absent'
            WHEN app.state = 'late' THEN 'late'
            ELSE 'present'
        END)::text AS state
FROM (SELECT ar.group_id, ar.date, ar.pass FROM attendance_records ar
      WHERE ar.student_client_id = @client_id::int AND ar.date >= @date_from::date AND ar.date < @today::date) r
FULL JOIN app ON app.group_id = r.group_id AND app.date = r.date
ORDER BY 2 DESC, 1;

-- name: GroupNames :many
SELECT id, name, discipline FROM groups WHERE id = ANY (@ids::int[]);
