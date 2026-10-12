-- Lesson days (spec 8.1). Written only by the sync.

-- name: DeleteDaysBetween :exec
DELETE FROM lesson_days WHERE date BETWEEN @date_from::date AND @date_to::date;

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
-- weekday, of active students (spec 5).
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
       )::int AS students
FROM lesson_days d
JOIN groups g ON g.id = d.group_id AND g.removed_at IS NULL
LEFT JOIN offices o ON o.id = g.office_id
JOIN schedule_items s ON s.group_id = d.group_id AND s.id = ANY (d.schedule_item_ids)
WHERE d.date = @date::date
  AND @teacher_id::int = ANY (s.teacher_ids)
GROUP BY g.id, o.name, d.date, d.minutes
ORDER BY min(s.begin_time), g.name;
