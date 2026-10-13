-- Writes below are used only by the HolliHop sync (people.Apply). Upserts
-- skip rows whose values didn't change, so updated_at means "last changed".

-- name: UpsertOffices :batchexec
INSERT INTO offices (id, name, utc_offset)
VALUES (@id, @name, @utc_offset)
ON CONFLICT (id) DO UPDATE
SET name = EXCLUDED.name, utc_offset = EXCLUDED.utc_offset, updated_at = now()
WHERE (offices.name, offices.utc_offset) IS DISTINCT FROM (EXCLUDED.name, EXCLUDED.utc_offset);

-- name: UpsertGroups :batchexec
INSERT INTO groups (id, name, discipline, level, learning_type, office_id)
VALUES (@id, @name, @discipline, @level, @learning_type, @office_id)
ON CONFLICT (id) DO UPDATE
SET name = EXCLUDED.name,
    discipline = EXCLUDED.discipline,
    level = EXCLUDED.level,
    learning_type = EXCLUDED.learning_type,
    office_id = EXCLUDED.office_id,
    removed_at = NULL,
    updated_at = now()
WHERE (groups.name, groups.discipline, groups.level, groups.learning_type, groups.office_id, groups.removed_at)
    IS DISTINCT FROM
      (EXCLUDED.name, EXCLUDED.discipline, EXCLUDED.level, EXCLUDED.learning_type, EXCLUDED.office_id, NULL::timestamptz);

-- name: MarkGroupsRemoved :execrows
UPDATE groups
SET removed_at = now(), updated_at = now()
WHERE removed_at IS NULL AND NOT (id = ANY (@present_ids::int[]));

-- name: DeleteScheduleItems :exec
DELETE FROM schedule_items;

-- name: InsertScheduleItems :copyfrom
INSERT INTO schedule_items (id, group_id, begin_date, end_date, begin_time, end_time, weekdays, classroom, teacher_ids)
VALUES (@id, @group_id, @begin_date, @end_date, @begin_time, @end_time, @weekdays, @classroom, @teacher_ids);

-- name: ListTeacherIDs :many
SELECT id FROM teachers;

-- name: CountActiveTeachers :one
SELECT count(*) FROM teachers WHERE deactivated_at IS NULL;

-- name: UpsertTeachers :batchexec
INSERT INTO teachers (id, first_name, last_name, phone_raw, fired)
VALUES (@id, @first_name, @last_name, @phone_raw, @fired)
ON CONFLICT (id) DO UPDATE
SET first_name = EXCLUDED.first_name,
    last_name = EXCLUDED.last_name,
    phone_raw = EXCLUDED.phone_raw,
    fired = EXCLUDED.fired,
    updated_at = now()
WHERE (teachers.first_name, teachers.last_name, teachers.phone_raw, teachers.fired)
    IS DISTINCT FROM (EXCLUDED.first_name, EXCLUDED.last_name, EXCLUDED.phone_raw, EXCLUDED.fired);

-- name: DeactivateTeachersExcept :many
UPDATE teachers
SET deactivated_at = now(), login_phone = NULL, updated_at = now()
WHERE deactivated_at IS NULL AND NOT (id = ANY (@active_ids::int[]))
RETURNING id;

-- name: ReactivateTeachers :many
UPDATE teachers
SET deactivated_at = NULL, activated_at = now(), updated_at = now()
WHERE deactivated_at IS NOT NULL AND id = ANY (@active_ids::int[])
RETURNING id;

-- name: ListStudentClientIDs :many
SELECT client_id FROM students;

-- name: CountActiveStudents :one
SELECT count(*) FROM students WHERE deactivated_at IS NULL;

-- name: UpsertStudents :batchexec
INSERT INTO students (client_id, hh_student_id, first_name, last_name, phone_raw, hh_status_id, office_ids)
VALUES (@client_id, @hh_student_id, @first_name, @last_name, @phone_raw, @hh_status_id, @office_ids)
ON CONFLICT (client_id) DO UPDATE
SET hh_student_id = EXCLUDED.hh_student_id,
    first_name = EXCLUDED.first_name,
    last_name = EXCLUDED.last_name,
    phone_raw = EXCLUDED.phone_raw,
    hh_status_id = EXCLUDED.hh_status_id,
    office_ids = EXCLUDED.office_ids,
    updated_at = now()
WHERE (students.hh_student_id, students.first_name, students.last_name, students.phone_raw, students.hh_status_id, students.office_ids)
    IS DISTINCT FROM
      (EXCLUDED.hh_student_id, EXCLUDED.first_name, EXCLUDED.last_name, EXCLUDED.phone_raw, EXCLUDED.hh_status_id, EXCLUDED.office_ids);

-- Full pulls only: a student HolliHop no longer returns at all is treated as
-- having left.
-- name: MarkStudentsMissing :execrows
UPDATE students
SET hh_status_id = NULL, updated_at = now()
WHERE hh_status_id IS NOT NULL AND NOT (client_id = ANY (@present_ids::int[]));

-- name: DeactivateStudents :many
UPDATE students
SET deactivated_at = now(), login_phone = NULL, updated_at = now()
WHERE deactivated_at IS NULL AND hh_status_id IS DISTINCT FROM @studying_status::int
RETURNING client_id;

-- name: ReactivateStudents :many
UPDATE students
SET deactivated_at = NULL, activated_at = now(), updated_at = now()
WHERE deactivated_at IS NOT NULL AND hh_status_id = @studying_status::int
RETURNING client_id;

-- name: DeleteEnrollments :exec
DELETE FROM enrollments;

-- name: InsertEnrollments :copyfrom
INSERT INTO enrollments (group_id, student_client_id, begin_date, end_date, weekdays)
VALUES (@group_id, @student_client_id, @begin_date, @end_date, @weekdays);

-- name: ListActivePhones :many
SELECT 'student'::text AS person_kind, client_id AS person_id, phone_raw, first_name, last_name
FROM students WHERE deactivated_at IS NULL
UNION ALL
SELECT 'teacher'::text, id, phone_raw, first_name, last_name
FROM teachers WHERE deactivated_at IS NULL;

-- Login phones are updated in two passes — clear every value that is not
-- wanted, then set the wanted ones — so the UNIQUE index never sees a number
-- held by two rows mid-update.

-- name: ClearStudentLoginPhones :exec
UPDATE students s
SET login_phone = NULL
WHERE s.login_phone IS NOT NULL
  AND NOT EXISTS (
      SELECT 1 FROM (SELECT unnest(@ids::int[]) AS id, unnest(@phones::text[]) AS phone) AS w
      WHERE w.id = s.client_id AND w.phone = s.login_phone);

-- name: SetStudentLoginPhones :exec
UPDATE students s
SET login_phone = w.phone
FROM (SELECT unnest(@ids::int[]) AS id, unnest(@phones::text[]) AS phone) AS w
WHERE s.client_id = w.id AND s.login_phone IS DISTINCT FROM w.phone;

-- name: ClearTeacherLoginPhones :exec
UPDATE teachers t
SET login_phone = NULL
WHERE t.login_phone IS NOT NULL
  AND NOT EXISTS (
      SELECT 1 FROM (SELECT unnest(@ids::int[]) AS id, unnest(@phones::text[]) AS phone) AS w
      WHERE w.id = t.id AND w.phone = t.login_phone);

-- name: SetTeacherLoginPhones :exec
UPDATE teachers t
SET login_phone = w.phone
FROM (SELECT unnest(@ids::int[]) AS id, unnest(@phones::text[]) AS phone) AS w
WHERE t.id = w.id AND t.login_phone IS DISTINCT FROM w.phone;

-- name: UpsertIssues :batchexec
INSERT INTO data_quality_issues (person_kind, person_id, kind, detail)
VALUES (@person_kind, @person_id, @kind, @detail)
ON CONFLICT (person_kind, person_id, kind) WHERE resolved_at IS NULL
DO UPDATE SET detail = EXCLUDED.detail, last_seen_at = now();

-- name: ResolveIssuesExcept :execrows
UPDATE data_quality_issues i
SET resolved_at = now()
WHERE i.resolved_at IS NULL
  AND NOT EXISTS (
      SELECT 1 FROM (SELECT unnest(@person_kinds::text[]) AS person_kind, unnest(@person_ids::int[]) AS person_id, unnest(@kinds::text[]) AS kind) AS c
      WHERE c.person_kind = i.person_kind AND c.person_id = i.person_id AND c.kind = i.kind);

-- Reads for other features (see directory.go). -------------------------------

-- name: FindByLoginPhone :many
SELECT 'student'::text AS kind, client_id AS id, first_name, last_name, login_phone, deactivated_at, activated_at
FROM students WHERE login_phone = @phone::text
UNION ALL
SELECT 'teacher'::text, id, first_name, last_name, login_phone, deactivated_at, activated_at
FROM teachers WHERE login_phone = @phone::text;

-- name: GetStudent :one
SELECT client_id, first_name, last_name, login_phone, deactivated_at, activated_at
FROM students WHERE client_id = @client_id;

-- name: GetTeacher :one
SELECT id, first_name, last_name, login_phone, deactivated_at, activated_at
FROM teachers WHERE id = @id;

-- Groups a student is enrolled in now or later.
-- name: StudentGroups :many
SELECT g.id, g.name, g.discipline, g.learning_type, coalesce(o.name, '')::text AS office
FROM groups g
LEFT JOIN offices o ON o.id = g.office_id
WHERE g.removed_at IS NULL
  AND EXISTS (
      SELECT 1 FROM enrollments e
      WHERE e.group_id = g.id AND e.student_client_id = @client_id
        AND (e.end_date IS NULL OR e.end_date >= @today::date))
ORDER BY g.discipline, g.name;

-- Groups a teacher teaches now or later.
-- name: TeacherGroups :many
SELECT g.id, g.name, g.discipline, g.learning_type, coalesce(o.name, '')::text AS office
FROM groups g
LEFT JOIN offices o ON o.id = g.office_id
WHERE g.removed_at IS NULL
  AND EXISTS (
      SELECT 1 FROM schedule_items s
      WHERE s.group_id = g.id AND @teacher_id::int = ANY (s.teacher_ids)
        AND (s.end_date IS NULL OR s.end_date >= @today::date))
ORDER BY g.discipline, g.name;

-- name: GroupSchedules :many
SELECT group_id, begin_date, end_date, begin_time, end_time, weekdays, classroom, teacher_ids
FROM schedule_items
WHERE group_id = ANY (@group_ids::int[])
  AND (end_date IS NULL OR end_date >= @today::date)
ORDER BY group_id, begin_time;

-- name: TeacherNames :many
SELECT id, first_name, last_name FROM teachers WHERE id = ANY (@ids::int[]);

-- Open data-quality issues for the admin panel (spec 13), in the admin's
-- branches: students by their HolliHop offices or their groups' branches,
-- teachers by the branches they teach in. People with no branch at all are
-- visible to super admins only.
-- name: OpenIssues :many
SELECT i.id, i.person_kind, i.person_id, i.kind, i.detail, i.first_seen_at,
       coalesce(st.first_name, t.first_name, '')::text AS first_name,
       coalesce(st.last_name, t.last_name, '')::text AS last_name,
       coalesce(st.phone_raw, t.phone_raw, '')::text AS phone_raw
FROM data_quality_issues i
LEFT JOIN students st ON i.person_kind = 'student' AND st.client_id = i.person_id
LEFT JOIN teachers t ON i.person_kind = 'teacher' AND t.id = i.person_id
WHERE i.resolved_at IS NULL
  AND (@all_offices::bool
       OR (i.person_kind = 'student' AND (
              st.office_ids && @office_ids::int[]
              OR EXISTS (SELECT 1 FROM enrollments e JOIN groups g ON g.id = e.group_id
                         WHERE e.student_client_id = i.person_id AND g.office_id = ANY (@office_ids::int[]))))
       OR (i.person_kind = 'teacher' AND EXISTS (
              SELECT 1 FROM schedule_items s JOIN groups g ON g.id = s.group_id AND g.removed_at IS NULL
              WHERE i.person_id = ANY (s.teacher_ids) AND g.office_id = ANY (@office_ids::int[]))))
ORDER BY i.kind, i.person_kind, 8, 7;
