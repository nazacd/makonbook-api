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

-- Questions in the practice sets a student started since a moment (a free account's day).
-- name: PracticeQuestionsSince :one
SELECT coalesce(sum(cardinality(question_ids)), 0)::int FROM roadmap_assessments
WHERE student_client_id = @student AND kind = 'practice' AND created_at >= @since;

-- Questions the student finished in any assessment, and whether they got
-- each right the last time is worked out by the caller.
-- name: FinishedQuestions :many
SELECT DISTINCT unnest(question_ids)::uuid AS id FROM roadmap_assessments
WHERE student_client_id = @student AND finished_at IS NOT NULL;
