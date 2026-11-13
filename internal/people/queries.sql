-- Writes below are used only by the HolliHop sync (people.Apply). Upserts
-- skip rows whose values didn't change, so updated_at means "last changed".
-- Negative ids aren't in HolliHop, and the sync never removes, deactivates or
-- rewrites them: -1 … -999999 is the store reviewers' demo account
-- (internal/demo), -1000001 and below are self-registered students
-- (source 'self', spec 21), until linkSelfAccounts re-keys one.
-- The sync never writes the name overrides (first/last_name_custom).

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
WHERE removed_at IS NULL AND id > 0 AND NOT (id = ANY (@present_ids::int[]));

-- name: DeleteScheduleItems :exec
DELETE FROM schedule_items WHERE group_id > 0;

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
WHERE deactivated_at IS NULL AND id > 0 AND NOT (id = ANY (@active_ids::int[]))
RETURNING id;

-- name: ReactivateTeachers :many
UPDATE teachers
SET deactivated_at = NULL, activated_at = now(), updated_at = now()
WHERE deactivated_at IS NOT NULL AND id = ANY (@active_ids::int[])
RETURNING id;

-- name: ListStudentClientIDs :many
SELECT client_id FROM students;

-- Self-registered students don't count: the mass-change guard is about
-- HolliHop's students.
-- name: CountActiveStudents :one
SELECT count(*) FROM students WHERE deactivated_at IS NULL AND source = 'hollihop';

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
WHERE hh_status_id IS NOT NULL AND client_id > 0 AND NOT (client_id = ANY (@present_ids::int[]));

-- name: DeactivateStudents :many
UPDATE students
SET deactivated_at = now(), login_phone = NULL, updated_at = now()
WHERE deactivated_at IS NULL AND client_id > 0 AND hh_status_id IS DISTINCT FROM @studying_status::int
RETURNING client_id;

-- name: ReactivateStudents :many
UPDATE students
SET deactivated_at = NULL, activated_at = now(), updated_at = now()
WHERE deactivated_at IS NOT NULL AND hh_status_id = @studying_status::int AND source = 'hollihop'
RETURNING client_id;

-- Self-registered accounts and their verified phones: a new HolliHop student
-- with one of these numbers is the same person (linkSelfAccounts); anyone
-- else in HolliHop with one is a conflict for admins.
-- name: SelfStudentPhones :many
SELECT client_id, login_phone::text AS login_phone, first_name, last_name
FROM students
WHERE source = 'self' AND login_phone IS NOT NULL AND deactivated_at IS NULL;

-- A HolliHop student stored a moment ago in this run, removed again because
-- a self-registered account takes their id (RekeySelfStudent). Nothing points
-- to them yet: enrollments come later in the run.
-- name: DeleteNewStudent :one
DELETE FROM students WHERE client_id = @client_id AND source = 'hollihop'
RETURNING hh_student_id, first_name, last_name, phone_raw, hh_status_id, office_ids;

-- Enrolment: the self-registered account becomes the HolliHop student.
-- Every foreign key cascades the new id; the password, username, Google
-- link and sessions are moved by their owners (auth.MoveStudent). Its
-- activated_at stays, so its password and sessions stay valid, and so do the
-- name overrides.
-- name: RekeySelfStudent :execrows
UPDATE students
SET client_id = @client_id, hh_student_id = @hh_student_id, first_name = @first_name, last_name = @last_name,
    phone_raw = @phone_raw, hh_status_id = @hh_status_id, office_ids = @office_ids, source = 'hollihop',
    updated_at = now()
WHERE client_id = @self_id AND source = 'self';

-- name: DeleteEnrollments :exec
DELETE FROM enrollments WHERE student_client_id > 0;

-- name: InsertEnrollments :copyfrom
INSERT INTO enrollments (group_id, student_client_id, begin_date, end_date, weekdays)
VALUES (@group_id, @student_client_id, @begin_date, @end_date, @weekdays);

-- name: ListActivePhones :many
SELECT 'student'::text AS person_kind, client_id AS person_id, phone_raw, first_name, last_name
FROM students WHERE deactivated_at IS NULL AND client_id > 0
UNION ALL
SELECT 'teacher'::text, id, phone_raw, first_name, last_name
FROM teachers WHERE deactivated_at IS NULL AND id > 0;

-- Login phones are updated in two passes — clear every value that is not
-- wanted, then set the wanted ones — so the UNIQUE index never sees a number
-- held by two rows mid-update.

-- name: ClearStudentLoginPhones :exec
UPDATE students s
SET login_phone = NULL
WHERE s.login_phone IS NOT NULL AND s.client_id > 0
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
WHERE t.login_phone IS NOT NULL AND t.id > 0
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

-- Names are as the person sees their own: their override, else HolliHop's.
-- name: FindByLoginPhone :many
SELECT 'student'::text AS kind, client_id AS id, coalesce(first_name_custom, first_name)::text AS first_name,
       coalesce(last_name_custom, last_name)::text AS last_name, login_phone, deactivated_at, activated_at, source
FROM students WHERE login_phone = @phone::text
UNION ALL
SELECT 'teacher'::text, id, coalesce(first_name_custom, first_name)::text, coalesce(last_name_custom, last_name)::text,
       login_phone, deactivated_at, activated_at, 'hollihop'::text
FROM teachers WHERE login_phone = @phone::text;

-- name: GetStudent :one
SELECT client_id, coalesce(first_name_custom, first_name)::text AS first_name,
       coalesce(last_name_custom, last_name)::text AS last_name, login_phone, deactivated_at, activated_at, source
FROM students WHERE client_id = @client_id;

-- name: GetTeacher :one
SELECT id, coalesce(first_name_custom, first_name)::text AS first_name,
       coalesce(last_name_custom, last_name)::text AS last_name, login_phone, deactivated_at, activated_at
FROM teachers WHERE id = @id;

-- Accounts (spec 21) ------------------------------------------------------------

-- A student who signed up themselves with a verified phone: not in HolliHop,
-- no branch, no groups. hh_student_id repeats the id (HolliHop never has it).
-- name: InsertSelfStudent :one
WITH n AS (SELECT nextval('self_student_ids')::int AS id)
INSERT INTO students (client_id, hh_student_id, first_name, last_name, phone_raw, login_phone, hh_status_id, office_ids, source)
SELECT n.id, n.id, @first_name, @last_name, coalesce(sqlc.narg('phone')::text, ''), sqlc.narg('phone')::text, NULL, '{}', 'self' FROM n
RETURNING client_id;

-- A registered student without a phone proves one (spec 21.4).
-- name: SetSelfPhone :execrows
UPDATE students SET login_phone = @phone::text, phone_raw = @phone::text
WHERE client_id = @client_id AND source = 'self' AND login_phone IS NULL;

-- Merging a registered student into another person (spec 21.4): what they
-- did moves to a student (practice and vocabulary rows are keyed by the
-- student; the people feature owns the merge as it owns the enrolment
-- link), and then they are removed. Goals move only when the other has none.
-- name: MoveStudentAttempts :exec
UPDATE practice_attempts SET student_client_id = @to_id WHERE student_client_id = @from_id;

-- name: MoveStudentReviews :exec
UPDATE vocab_reviews SET student_client_id = @to_id WHERE student_client_id = @from_id;

-- name: MoveStudentGames :exec
UPDATE vocab_game_results SET student_client_id = @to_id WHERE student_client_id = @from_id;

-- name: MoveStudentDecks :exec
UPDATE vocab_user_decks SET owner_client_id = @to_id WHERE owner_client_id = @from_id;

-- name: MoveStudentDeckAdds :exec
INSERT INTO vocab_deck_adds (student_client_id, deck_id, added_at)
SELECT @to_id::int, deck_id, added_at FROM vocab_deck_adds WHERE student_client_id = @from_id::int
ON CONFLICT DO NOTHING;

-- name: MoveStudentSavedUniversities :exec
INSERT INTO student_saved_universities (student_client_id, university_id, saved_at)
SELECT @to_id::int, university_id, saved_at FROM student_saved_universities WHERE student_client_id = @from_id::int
ON CONFLICT DO NOTHING;

-- name: MoveStudentAnswers :exec
UPDATE question_answers SET student_client_id = @to_id WHERE student_client_id = @from_id;

-- name: MoveStudentSavedQuestions :exec
INSERT INTO saved_questions (student_client_id, question_id, saved_at)
SELECT @to_id::int, question_id, saved_at FROM saved_questions WHERE student_client_id = @from_id::int
ON CONFLICT DO NOTHING;

-- name: MoveStudentParked :exec
UPDATE parked_questions SET student_client_id = @to_id WHERE student_client_id = @from_id;

-- name: MoveStudentReports :exec
UPDATE practice_reports SET student_client_id = @to_id WHERE student_client_id = @from_id;

-- name: MoveStudentGoals :exec
UPDATE student_goals AS s SET student_client_id = @to_id::int
WHERE s.student_client_id = @from_id::int
  AND NOT EXISTS (SELECT 1 FROM student_goals g WHERE g.student_client_id = @to_id::int);

-- Rows a teacher can't take: removed with the student.
-- name: DeleteStudentAttempts :exec
DELETE FROM practice_attempts WHERE student_client_id = @client_id;

-- name: DeleteStudentReviews :exec
DELETE FROM vocab_reviews WHERE student_client_id = @client_id;

-- The rest goes with them (foreign keys cascade).
-- name: DeleteSelfStudent :execrows
DELETE FROM students WHERE client_id = @client_id AND source = 'self';

-- Name overrides: NULL clears one (back to HolliHop's name).
-- name: SetStudentNames :execrows
UPDATE students SET first_name_custom = @first_name, last_name_custom = @last_name
WHERE client_id = @client_id;

-- name: SetTeacherNames :execrows
UPDATE teachers SET first_name_custom = @first_name, last_name_custom = @last_name
WHERE id = @id;

-- Groups a student is enrolled in now or later.
-- name: StudentGroups :many
SELECT g.id, g.name, g.discipline, g.level, g.learning_type, coalesce(o.name, '')::text AS office
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
SELECT g.id, g.name, g.discipline, g.level, g.learning_type, coalesce(o.name, '')::text AS office
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

-- Roadmaps (spec 22): an open assessment the other already has for the
-- same set gives way; the rest moves.
-- name: DropClashingAssessments :exec
DELETE FROM roadmap_assessments AS a
WHERE a.student_client_id = @from_id::int AND a.finished_at IS NULL
  AND EXISTS (SELECT 1 FROM roadmap_assessments b
              WHERE b.student_client_id = @to_id::int AND b.finished_at IS NULL
                AND b.lesson_id IS NOT DISTINCT FROM a.lesson_id AND b.kind = a.kind
                AND coalesce(b.homework_index, -1) = coalesce(a.homework_index, -1)
                AND b.assignment_id IS NOT DISTINCT FROM a.assignment_id);

-- name: MoveStudentAssessments :exec
UPDATE roadmap_assessments SET student_client_id = @to_id WHERE student_client_id = @from_id;

-- Notifications (spec 23.5) have no foreign key to people: a merge moves or
-- deletes them.
-- name: MoveStudentNotifications :exec
UPDATE notifications SET person_id = @to_id WHERE person_kind = 'student' AND person_id = @from_id;

-- name: DeleteStudentNotifications :exec
DELETE FROM notifications WHERE person_kind = 'student' AND person_id = @from_id;

-- The registered student's place in the library's books goes with them
-- (CopyStudentLibrary, then DeleteStudentLibrary): where both have opened a
-- book, the further page stays.
-- name: CopyStudentLibrary :exec
INSERT INTO library_progress AS t (person_kind, person_id, book_id, page, opened_at, updated_at)
SELECT 'student', @to_id::int, f.book_id, f.page, f.opened_at, f.updated_at
FROM library_progress f
WHERE f.person_kind = 'student' AND f.person_id = @from_id::int
ON CONFLICT (person_kind, person_id, book_id) DO UPDATE
SET page = greatest(t.page, EXCLUDED.page),
    opened_at = least(t.opened_at, EXCLUDED.opened_at),
    updated_at = greatest(t.updated_at, EXCLUDED.updated_at);

-- name: DeleteStudentLibrary :exec
DELETE FROM library_progress WHERE person_kind = 'student' AND person_id = @from_id;

-- name: MoveStudentChoices :exec
UPDATE roadmap_choices AS c SET student_client_id = @to_id::int
WHERE c.student_client_id = @from_id::int
  AND NOT EXISTS (SELECT 1 FROM roadmap_choices d WHERE d.student_client_id = @to_id::int AND d.roadmap_id = c.roadmap_id);

-- name: MoveStudentOnboarding :exec
UPDATE onboarding AS o SET student_client_id = @to_id::int
WHERE o.student_client_id = @from_id::int
  AND NOT EXISTS (SELECT 1 FROM onboarding p WHERE p.student_client_id = @to_id::int);
