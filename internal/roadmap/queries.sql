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
-- name: Progress :many
SELECT lesson_id, kind, coalesce(homework_index, -1)::int AS homework_index, max(percent)::int AS best
FROM roadmap_assessments
WHERE student_client_id = @student AND finished_at IS NOT NULL
GROUP BY lesson_id, kind, homework_index;

-- name: LessonAssessments :many
SELECT * FROM roadmap_assessments
WHERE student_client_id = @student AND lesson_id = @lesson_id
ORDER BY created_at, id;

-- name: OpenAssessment :one
SELECT * FROM roadmap_assessments
WHERE student_client_id = @student AND lesson_id = @lesson_id AND kind = @kind
  AND coalesce(homework_index, -1) = @homework_index::int AND finished_at IS NULL;

-- Questions the student has had in any roadmap assessment.
-- name: SeenQuestions :many
SELECT DISTINCT unnest(question_ids)::uuid AS id FROM roadmap_assessments WHERE student_client_id = @student;

-- name: InsertAssessment :one
INSERT INTO roadmap_assessments (student_client_id, roadmap_id, lesson_id, kind, homework_index, question_ids)
VALUES (@student, @roadmap_id, @lesson_id, @kind, @homework_index, @question_ids::uuid[])
ON CONFLICT (student_client_id, lesson_id, kind, coalesce(homework_index, -1)) WHERE finished_at IS NULL DO NOTHING
RETURNING *;

-- name: GetAssessment :one
SELECT * FROM roadmap_assessments WHERE id = @id;

-- name: LockAssessment :one
SELECT * FROM roadmap_assessments WHERE id = @id FOR UPDATE;

-- name: SaveAnswers :exec
UPDATE roadmap_assessments SET answers = @answers WHERE id = @id;

-- name: FinishAssessment :exec
UPDATE roadmap_assessments
SET answers = @answers, finished_at = now(), correct = @correct, total = @total, percent = @percent
WHERE id = @id;
