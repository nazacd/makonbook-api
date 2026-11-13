-- Roadmaps (spec 22).

-- name: LatestVersions :many
SELECT DISTINCT ON (subject, level) subject, level, version
FROM curriculum_versions ORDER BY subject, level, version DESC;

-- name: LatestCourses :many
SELECT DISTINCT ON (subject, level) subject, level, version, content_hash, content
FROM curriculum_versions ORDER BY subject, level, version DESC;

-- name: CourseContent :one
SELECT content FROM curriculum_versions WHERE subject = @subject AND level = @level AND version = @version;

-- name: InsertCourseVersion :exec
INSERT INTO curriculum_versions (subject, level, version, content, content_hash)
VALUES (@subject, @level, @version, @content, @content_hash);

-- name: GetOnboarding :one
SELECT answers, updated_at FROM onboarding WHERE student_client_id = @student;

-- name: HasOnboarding :one
SELECT EXISTS (SELECT 1 FROM onboarding WHERE student_client_id = @student);

-- name: SaveOnboarding :exec
INSERT INTO onboarding (student_client_id, answers) VALUES (@student, @answers)
ON CONFLICT (student_client_id) DO UPDATE SET answers = excluded.answers, updated_at = now();

-- name: Choices :many
SELECT roadmap_id FROM roadmap_choices WHERE student_client_id = @student ORDER BY created_at, roadmap_id;

-- name: DeleteChoices :exec
DELETE FROM roadmap_choices WHERE student_client_id = @student;

-- name: InsertChoice :exec
INSERT INTO roadmap_choices (student_client_id, roadmap_id) VALUES (@student, @roadmap_id)
ON CONFLICT DO NOTHING;

-- Every finished assessment's lesson, kind, set and best score: progress.
-- Custom homework (no lesson) isn't on any roadmap.
-- name: Progress :many
SELECT coalesce(lesson_id, '')::text AS lesson_id, kind, coalesce(homework_index, -1)::int AS homework_index,
       max(percent)::int AS best
FROM roadmap_assessments
WHERE student_client_id = @student AND finished_at IS NOT NULL AND lesson_id IS NOT NULL
GROUP BY lesson_id, kind, homework_index;

-- Progress of several students at once (a group's, for its teachers).
-- name: ProgressOf :many
SELECT student_client_id, coalesce(lesson_id, '')::text AS lesson_id, kind,
       coalesce(homework_index, -1)::int AS homework_index, max(percent)::int AS best
FROM roadmap_assessments
WHERE student_client_id = ANY (@students::int[]) AND finished_at IS NOT NULL AND lesson_id IS NOT NULL
GROUP BY student_client_id, lesson_id, kind, homework_index;

-- name: LessonAssessments :many
SELECT * FROM roadmap_assessments
WHERE student_client_id = @student AND lesson_id = @lesson_id
ORDER BY created_at, id;

-- The unfinished one of a lesson's level check or homework set: an assigned
-- one first (spec 23.3), then the roadmap's own.
-- name: OpenAssessment :one
SELECT * FROM roadmap_assessments
WHERE student_client_id = @student AND lesson_id = @lesson_id::text AND kind = @kind
  AND coalesce(homework_index, -1) = @homework_index::int AND finished_at IS NULL
ORDER BY assignment_id IS NULL, created_at
LIMIT 1;

-- The roadmap's own unfinished one (no assignment): what InsertAssessment
-- may have lost a race to.
-- name: OpenOwnAssessment :one
SELECT * FROM roadmap_assessments
WHERE student_client_id = @student AND lesson_id = @lesson_id::text AND kind = @kind
  AND coalesce(homework_index, -1) = @homework_index::int AND finished_at IS NULL AND assignment_id IS NULL;

-- name: OpenAssigned :one
SELECT * FROM roadmap_assessments
WHERE student_client_id = @student AND assignment_id = @assignment_id AND finished_at IS NULL;

-- Questions the student has had in any roadmap assessment.
-- name: SeenQuestions :many
SELECT DISTINCT unnest(question_ids)::uuid AS id FROM roadmap_assessments WHERE student_client_id = @student;

-- name: InsertAssessment :one
INSERT INTO roadmap_assessments (student_client_id, roadmap_id, lesson_id, kind, homework_index, question_ids, assignment_id)
VALUES (@student, @roadmap_id, @lesson_id, @kind, @homework_index, @question_ids::uuid[], @assignment_id)
ON CONFLICT (student_client_id, coalesce(lesson_id, ''), kind, coalesce(homework_index, -1),
             coalesce(assignment_id, '00000000-0000-0000-0000-000000000000'::uuid)) WHERE finished_at IS NULL DO NOTHING
RETURNING *;

-- name: GetAssessment :one
SELECT * FROM roadmap_assessments WHERE id = @id;

-- name: LockAssessment :one
SELECT * FROM roadmap_assessments WHERE id = @id FOR UPDATE;

-- name: SaveAnswers :exec
UPDATE roadmap_assessments SET answers = @answers, times = @times WHERE id = @id;

-- name: FinishAssessment :exec
UPDATE roadmap_assessments
SET answers = @answers, times = @times, finished_at = now(), correct = @correct, total = @total, percent = @percent
WHERE id = @id;

-- The assignment an assessment was started from (spec 23).
-- name: AssessmentAssignment :one
SELECT id, kind, title, subject, due_at FROM assignments WHERE id = @id;

-- name: LogoHashes :many
SELECT slug, sha256 FROM university_logos;

-- name: GetLogo :one
SELECT content_type, body, sha256 FROM university_logos WHERE slug = @slug;

-- name: SaveLogo :exec
INSERT INTO university_logos (slug, content_type, body, sha256) VALUES (@slug, @content_type, @body, @sha256)
ON CONFLICT (slug) DO UPDATE SET content_type = excluded.content_type, body = excluded.body,
    sha256 = excluded.sha256, updated_at = now();

-- Practice sets (spec 22.8) ----------------------------------------------------

-- name: InsertPractice :one
INSERT INTO roadmap_assessments (student_client_id, kind, question_ids, practice)
VALUES (@student, 'practice', @question_ids::uuid[], @practice)
ON CONFLICT (student_client_id, coalesce(lesson_id, ''), kind, coalesce(homework_index, -1),
             coalesce(assignment_id, '00000000-0000-0000-0000-000000000000'::uuid)) WHERE finished_at IS NULL DO NOTHING
RETURNING *;

-- name: OpenPractice :one
SELECT * FROM roadmap_assessments WHERE student_client_id = @student AND kind = 'practice' AND finished_at IS NULL;

-- name: DiscardPractice :execrows
DELETE FROM roadmap_assessments WHERE id = @id AND student_client_id = @student AND kind = 'practice' AND finished_at IS NULL;

-- A student's practice sets, newest first.
-- name: PracticeSets :many
SELECT id, practice, cardinality(question_ids)::int AS questions, created_at, finished_at, correct, total, percent
FROM roadmap_assessments WHERE student_client_id = @student AND kind = 'practice'
ORDER BY created_at DESC LIMIT @lim;

-- Questions the student finished in any assessment, and whether they got
-- each right the last time is worked out by the caller.
-- name: FinishedQuestions :many
SELECT DISTINCT unnest(question_ids)::uuid AS id FROM roadmap_assessments
WHERE student_client_id = @student AND finished_at IS NOT NULL;

-- Answers (spec 22.8) ----------------------------------------------------------

-- name: InsertAnswer :execrows
INSERT INTO question_answers (student_client_id, question_id, assessment_id, answer, correct, seconds, answered_at)
VALUES (@student, @question_id, @assessment_id, @answer, @correct, sqlc.narg(seconds), @answered_at)
ON CONFLICT (assessment_id, question_id) DO NOTHING;

-- The questions of an assessment already checked one at a time, and how.
-- name: CheckedAnswers :many
SELECT question_id, correct FROM question_answers WHERE assessment_id = @assessment_id;

-- Finished assessments, for filling question_answers from them.
-- name: FinishedAssessments :many
SELECT id, student_client_id, question_ids, answers, times, finished_at FROM roadmap_assessments
WHERE finished_at IS NOT NULL ORDER BY finished_at;

-- name: SolvedQuestions :many
SELECT DISTINCT question_id FROM question_answers WHERE student_client_id = @student AND correct;

-- Saved questions ---------------------------------------------------------------

-- name: SavedQuestionIDs :many
SELECT question_id FROM saved_questions WHERE student_client_id = @student ORDER BY saved_at DESC;

-- name: SaveQuestion :exec
INSERT INTO saved_questions (student_client_id, question_id)
SELECT @student, id FROM questions WHERE id = @question_id AND status = 'approved'
ON CONFLICT DO NOTHING;

-- name: UnsaveQuestion :exec
DELETE FROM saved_questions WHERE student_client_id = @student AND question_id = @question_id;

-- Stats (spec 22.8), school time (tz) -------------------------------------------

-- Per subject: approved questions, and those the student answered and solved.
-- name: SubjectProgress :many
SELECT q.subject,
       count(*)::int AS total,
       count(*) FILTER (WHERE EXISTS (SELECT 1 FROM question_answers a WHERE a.student_client_id = @student AND a.question_id = q.id))::int AS attempted,
       count(*) FILTER (WHERE EXISTS (SELECT 1 FROM question_answers a WHERE a.student_client_id = @student AND a.question_id = q.id AND a.correct))::int AS solved
FROM questions q WHERE q.status = 'approved'
GROUP BY q.subject;

-- Per skill, the approved questions the student solved.
-- name: SkillSolved :many
SELECT q.subject, q.skill, count(*)::int AS solved
FROM questions q
WHERE q.status = 'approved'
  AND EXISTS (SELECT 1 FROM question_answers a WHERE a.student_client_id = @student AND a.question_id = q.id AND a.correct)
GROUP BY q.subject, q.skill;

-- name: AnswerTotals :one
SELECT count(*)::int AS answers, count(*) FILTER (WHERE correct)::int AS correct,
       count(DISTINCT question_id)::int AS questions
FROM question_answers WHERE student_client_id = @student;

-- Days (school time) the student answered on, newest first.
-- name: AnswerDays :many
SELECT DISTINCT (answered_at AT TIME ZONE @tz::text)::date AS day
FROM question_answers WHERE student_client_id = @student
ORDER BY day DESC LIMIT 400;

-- Answers by week (Monday, school time), right or wrong, and difficulty.
-- name: WeeklyAnswers :many
SELECT date_trunc('week', a.answered_at AT TIME ZONE @tz::text)::date AS week,
       a.correct, coalesce(q.difficulty, '')::text AS difficulty, count(*)::int AS n
FROM question_answers a JOIN questions q ON q.id = a.question_id
WHERE a.student_client_id = @student AND a.answered_at >= @since
GROUP BY 1, 2, 3;

-- Per subject and domain: answers and right ones.
-- name: DomainAccuracy :many
SELECT q.subject, q.domain, count(*)::int AS answers, count(*) FILTER (WHERE a.correct)::int AS correct
FROM question_answers a JOIN questions q ON q.id = a.question_id
WHERE a.student_client_id = @student
GROUP BY q.subject, q.domain;

-- Per subject and difficulty: answers, right ones, and seconds (where timed).
-- student 0: everyone (the site-wide averages).
-- name: DifficultyStats :many
SELECT q.subject, q.difficulty::text AS difficulty, count(*)::int AS answers, count(*) FILTER (WHERE a.correct)::int AS correct,
       coalesce(avg(a.seconds) FILTER (WHERE a.seconds > 0), 0)::float8 AS avg_seconds,
       count(a.seconds) FILTER (WHERE a.seconds > 0)::int AS timed
FROM question_answers a JOIN questions q ON q.id = a.question_id
WHERE (@student::int = 0 OR a.student_client_id = @student::int) AND q.difficulty IS NOT NULL
GROUP BY q.subject, q.difficulty;
