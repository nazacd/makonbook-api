-- Assignments and notifications (spec 23).

-- Groups and their people ------------------------------------------------------

-- Whether a teacher teaches a group now or later (as practice's TeachesGroup).
-- name: TeachesGroup :one
SELECT EXISTS (
    SELECT 1 FROM schedule_items s JOIN groups g ON g.id = s.group_id
    WHERE s.group_id = @group_id AND g.removed_at IS NULL AND @teacher_id::int = ANY (s.teacher_ids)
      AND (s.end_date IS NULL OR s.end_date >= @today::date));

-- The groups a teacher teaches now or later.
-- name: TeacherGroupIDs :many
SELECT g.id FROM groups g
WHERE g.removed_at IS NULL AND EXISTS (
    SELECT 1 FROM schedule_items s
    WHERE s.group_id = g.id AND @teacher_id::int = ANY (s.teacher_ids)
      AND (s.end_date IS NULL OR s.end_date >= @today::date))
ORDER BY g.id;

-- name: GetGroup :one
SELECT id, name, discipline, level FROM groups WHERE id = @id;

-- name: GroupNames :many
SELECT id, name FROM groups WHERE id = ANY (@ids::int[]);

-- The students enrolled in groups now (as practice's GroupStudents), by name.
-- name: Rosters :many
SELECT DISTINCT e.group_id, st.client_id, st.first_name, st.last_name
FROM enrollments e JOIN students st ON st.client_id = e.student_client_id
WHERE e.group_id = ANY (@group_ids::int[]) AND st.deactivated_at IS NULL
  AND (e.begin_date IS NULL OR e.begin_date <= @today::date)
  AND (e.end_date IS NULL OR e.end_date >= @today::date)
ORDER BY st.last_name, st.first_name, st.client_id, e.group_id;

-- The groups a student is enrolled in now (the same test as Rosters).
-- name: StudentGroupIDs :many
SELECT DISTINCT e.group_id
FROM enrollments e JOIN students st ON st.client_id = e.student_client_id JOIN groups g ON g.id = e.group_id
WHERE e.student_client_id = @student AND st.deactivated_at IS NULL AND g.removed_at IS NULL
  AND (e.begin_date IS NULL OR e.begin_date <= @today::date)
  AND (e.end_date IS NULL OR e.end_date >= @today::date)
ORDER BY e.group_id;

-- Assignments ------------------------------------------------------------------

-- name: InsertAssignment :one
INSERT INTO assignments (group_id, teacher_id, kind, title, instructions, subject, course, lesson_id, homework_index,
                         test_code, sections, question_ids, skills, mix, opens_at, due_at)
VALUES (@group_id, @teacher_id, @kind, @title, @instructions, @subject, @course, @lesson_id, @homework_index,
        @test_code, @sections::text[], @question_ids::uuid[], @skills::text[], @mix, @opens_at, @due_at)
RETURNING *;

-- name: GetAssignment :one
SELECT * FROM assignments WHERE id = @id;

-- name: GroupAssignments :many
SELECT * FROM assignments WHERE group_id = ANY (@group_ids::int[]) ORDER BY created_at DESC, id;

-- Those students see, soonest deadline first (scheduled ones too, locked until they open).
-- name: StudentAssignments :many
SELECT * FROM assignments WHERE group_id = ANY (@group_ids::int[]) ORDER BY due_at, id;

-- name: UpdateAssignment :one
UPDATE assignments
SET title = @title, instructions = @instructions, opens_at = @opens_at, due_at = @due_at,
    closed_at = CASE WHEN @closed::bool THEN coalesce(closed_at, now()) END, updated_at = now()
WHERE id = @id
RETURNING *;

-- name: DeleteAssignment :exec
DELETE FROM assignments WHERE id = @id;

-- name: HasAssessments :one
SELECT EXISTS (SELECT 1 FROM roadmap_assessments WHERE assignment_id = @id);

-- Work on assignments: the roadmap assessments started from them, oldest first.
-- name: Work :many
SELECT id, assignment_id, student_client_id, created_at, finished_at, correct, total, percent, answers, times
FROM roadmap_assessments
WHERE assignment_id = ANY (@ids::uuid[]) AND student_client_id = ANY (@students::int[])
ORDER BY created_at, id;

-- Students' attempts on mock tests (exams count those finished after they
-- opened, with the sections asked), oldest first.
-- name: ExamAttempts :many
SELECT a.id, a.student_client_id, t.code, t.title, a.test_version, a.sections, a.started_at, a.finished_at,
       a.scores, a.answers
FROM practice_attempts a JOIN practice_tests t ON t.id = a.test_id
WHERE t.code = ANY (@codes::text[]) AND a.student_client_id = ANY (@students::int[])
ORDER BY a.finished_at, a.id;

-- Questions ----------------------------------------------------------------------

-- name: ApprovedSubjects :many
SELECT id, subject FROM questions WHERE id = ANY (@ids::uuid[]) AND status = 'approved';

-- Questions students have had in any roadmap assessment (assigned ones too).
-- name: SeenByStudents :many
SELECT DISTINCT unnest(question_ids)::uuid AS id FROM roadmap_assessments WHERE student_client_id = ANY (@students::int[]);

-- Questions a group was given.
-- name: GroupQuestions :many
SELECT DISTINCT unnest(question_ids)::uuid AS id FROM assignments WHERE group_id = @group_id;

-- Notifications (spec 23.5) -------------------------------------------------------

-- name: InsertNotification :exec
INSERT INTO notifications (person_kind, person_id, kind, title, body, link, assignment_id)
VALUES (@person_kind, @person_id, @kind, @title, @body, @link, @assignment_id);

-- name: Notifications :many
SELECT id, kind, title, body, link, created_at, read_at FROM notifications
WHERE person_kind = @person_kind AND person_id = @person_id
ORDER BY created_at DESC, id DESC
LIMIT 100;

-- name: UnreadCount :one
SELECT count(*)::int FROM notifications
WHERE person_kind = @person_kind AND person_id = @person_id AND read_at IS NULL;

-- name: MarkRead :exec
UPDATE notifications SET read_at = now()
WHERE person_kind = @person_kind AND person_id = @person_id AND read_at IS NULL AND id = ANY (@ids::bigint[]);

-- name: MarkAllRead :exec
UPDATE notifications SET read_at = now()
WHERE person_kind = @person_kind AND person_id = @person_id AND read_at IS NULL;
