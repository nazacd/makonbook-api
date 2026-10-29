-- Lesson days (spec 8.1). Written only by the sync, and by internal/demo for
-- the store reviewers' demo groups (negative ids), which the sync and the
-- admins' lists leave out.

-- name: DeleteDaysBetween :exec
DELETE FROM lesson_days WHERE date BETWEEN @date_from::date AND @date_to::date AND group_id > 0;

-- A day of a group HolliHop listed but the sync didn't store (shouldn't
-- happen; both come from GetEdUnits) is skipped rather than failing the run.
-- name: InsertDay :batchexec
INSERT INTO lesson_days (group_id, date, minutes, schedule_item_ids)
SELECT @group_id, @date, @minutes, @schedule_item_ids::int[]
WHERE EXISTS (SELECT 1 FROM groups WHERE id = @group_id)
ON CONFLICT (group_id, date) DO UPDATE
SET minutes = excluded.minutes, schedule_item_ids = excluded.schedule_item_ids;

-- A teacher's lessons on a date: lesson days whose schedule items name the
-- teacher (a substitute who isn't on the schedule item has none, spec D12).
-- students is the roster size: enrollments current on that date, on that
-- weekday, of active students (spec 5). session_* describe the lesson's
-- attendance session, if one was opened (read-only here; the attendance
-- feature owns that table).
-- name: TeacherLessons :many
SELECT g.id AS group_id, g.name AS group_name, g.discipline, g.learning_type,
       coalesce(o.name, '')::text AS office,
       d.minutes,
       min(s.begin_time)::time AS begin_time,
       max(s.end_time)::time AS end_time,
       coalesce(string_agg(DISTINCT s.classroom, ', ') FILTER (WHERE s.classroom <> ''), '')::text AS classroom,
       (SELECT count(*)
          FROM enrollments e
          JOIN students st ON st.client_id = e.student_client_id AND st.deactivated_at IS NULL
         WHERE e.group_id = g.id
           AND (e.begin_date IS NULL OR e.begin_date <= d.date)
           AND (e.end_date IS NULL OR e.end_date >= d.date)
           AND (e.weekdays = 0 OR e.weekdays & (1 << (extract(isodow FROM d.date)::int - 1)) <> 0)
       )::int AS students,
       a.id AS session_id,
       (a.finished_at IS NOT NULL)::bool AS session_finished
FROM lesson_days d
JOIN groups g ON g.id = d.group_id AND g.removed_at IS NULL
LEFT JOIN offices o ON o.id = g.office_id
JOIN schedule_items s ON s.group_id = d.group_id AND s.id = ANY (d.schedule_item_ids)
LEFT JOIN attendance_sessions a ON a.group_id = d.group_id AND a.date = d.date
WHERE d.date = @date::date
  AND @teacher_id::int = ANY (s.teacher_ids)
GROUP BY g.id, o.name, d.date, d.minutes, a.id
ORDER BY min(s.begin_time), g.name;

-- Every lesson on a date in the admin's branches (spec 13), with its
-- teachers and, once attendance was taken, the marks' counts (read-only).
-- Only lessons someone can take attendance for: at least one teacher on the
-- day's schedule is in MakonBook and not fired. A lesson with no teacher in
-- HolliHop is almost always a finished group whose schedule was never
-- closed there.
-- name: OfficeLessons :many
SELECT g.id AS group_id, g.name AS group_name, g.discipline, g.learning_type,
       g.office_id, coalesce(o.name, '')::text AS office,
       d.minutes,
       min(s.begin_time)::time AS begin_time,
       max(s.end_time)::time AS end_time,
       coalesce(string_agg(DISTINCT s.classroom, ', ') FILTER (WHERE s.classroom <> ''), '')::text AS classroom,
       (SELECT coalesce(array_agg(DISTINCT t.first_name || ' ' || t.last_name), '{}')
          FROM teachers t
         WHERE NOT t.fired
           AND t.id IN (SELECT unnest(si.teacher_ids) FROM schedule_items si
                         WHERE si.group_id = g.id AND si.id = ANY (d.schedule_item_ids))
       )::text[] AS teachers,
       (SELECT count(*)
          FROM enrollments e
          JOIN students st ON st.client_id = e.student_client_id AND st.deactivated_at IS NULL
         WHERE e.group_id = g.id
           AND (e.begin_date IS NULL OR e.begin_date <= d.date)
           AND (e.end_date IS NULL OR e.end_date >= d.date)
           AND (e.weekdays = 0 OR e.weekdays & (1 << (extract(isodow FROM d.date)::int - 1)) <> 0)
       )::int AS students,
       a.id AS session_id,
       (a.finished_at IS NOT NULL)::bool AS session_finished,
       -- A newly opened group: its schedule began within new_group_days and
       -- nobody has attended a lesson yet (HolliHop's marks, read-only).
       -- (A schedule without a start date is an old one: not new.)
       coalesce(NOT EXISTS (SELECT 1 FROM attendance_records ar WHERE ar.group_id = g.id AND ar.date < d.date)
                AND (SELECT min(si.begin_date) FROM schedule_items si WHERE si.group_id = g.id) >= d.date - @new_group_days::int,
                false)::bool AS new_group,
       (SELECT count(*) FROM attendance_marks m WHERE m.session_id = a.id AND m.state = 'present')::int AS present,
       (SELECT count(*) FROM attendance_marks m WHERE m.session_id = a.id AND m.state = 'late')::int AS late,
       (SELECT count(*) FROM attendance_marks m WHERE m.session_id = a.id AND m.state = 'absent')::int AS absent,
       (SELECT count(*) FROM attendance_marks m WHERE m.session_id = a.id AND m.state = 'pending')::int AS pending
FROM lesson_days d
JOIN groups g ON g.id = d.group_id AND g.removed_at IS NULL
LEFT JOIN offices o ON o.id = g.office_id
JOIN schedule_items s ON s.group_id = d.group_id AND s.id = ANY (d.schedule_item_ids)
LEFT JOIN attendance_sessions a ON a.group_id = d.group_id AND a.date = d.date
WHERE d.date = @date::date AND g.id > 0
  AND (@all_offices::bool OR g.office_id = ANY (@office_ids::int[]))
GROUP BY g.id, o.name, d.date, d.minutes, d.schedule_item_ids, a.id
HAVING EXISTS (SELECT 1 FROM schedule_items si JOIN teachers t ON t.id = ANY (si.teacher_ids)
               WHERE si.group_id = g.id AND si.id = ANY (d.schedule_item_ids) AND NOT t.fired)
ORDER BY coalesce(o.name, ''), min(s.begin_time), g.name;

-- Groups admins should fix in HolliHop (spec 13): each group with lessons
-- from today on, with what its next lesson looks like. The caller decides
-- the problem: no teacher who can take attendance, or nobody on the list of
-- a group that isn't new (in practice: finished, schedule never closed).
-- name: GroupIssues :many
WITH next AS (
    SELECT DISTINCT ON (d.group_id) d.group_id, d.date, d.schedule_item_ids
    FROM lesson_days d
    WHERE d.date >= @today::date AND d.group_id > 0
    ORDER BY d.group_id, d.date
)
SELECT g.id AS group_id, g.name AS group_name, g.learning_type,
       g.office_id, coalesce(o.name, '')::text AS office,
       n.date AS next_lesson,
       (SELECT min(si.begin_date) FROM schedule_items si WHERE si.group_id = g.id)::date AS schedule_since,
       (SELECT max(ar.date) FROM attendance_records ar WHERE ar.group_id = g.id AND ar.date < @today::date)::date AS last_attended,
       (SELECT coalesce(array_agg(DISTINCT t.first_name || ' ' || t.last_name), '{}')
          FROM schedule_items si JOIN teachers t ON t.id = ANY (si.teacher_ids)
         WHERE si.group_id = g.id AND si.id = ANY (n.schedule_item_ids) AND NOT t.fired
       )::text[] AS teachers,
       (SELECT count(*) FROM schedule_items si, unnest(si.teacher_ids) tid
         WHERE si.group_id = g.id AND si.id = ANY (n.schedule_item_ids))::int AS scheduled_teachers,
       (SELECT count(*)
          FROM enrollments e
          JOIN students st ON st.client_id = e.student_client_id AND st.deactivated_at IS NULL
         WHERE e.group_id = g.id
           AND (e.begin_date IS NULL OR e.begin_date <= n.date)
           AND (e.end_date IS NULL OR e.end_date >= n.date)
           AND (e.weekdays = 0 OR e.weekdays & (1 << (extract(isodow FROM n.date)::int - 1)) <> 0)
       )::int AS students
FROM next n
JOIN groups g ON g.id = n.group_id AND g.removed_at IS NULL
LEFT JOIN offices o ON o.id = g.office_id
WHERE (@all_offices::bool OR g.office_id = ANY (@office_ids::int[]))
ORDER BY coalesce(o.name, ''), g.name;
