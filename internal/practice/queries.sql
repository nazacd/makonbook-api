-- Practice tests and their question bank (spec 9).

-- name: GetTestByCode :one
SELECT * FROM practice_tests WHERE code = @code;

-- name: CreateTest :one
INSERT INTO practice_tests (code, title) VALUES (@code, @title) RETURNING *;

-- Questions of a test that people have already worked on: approved or
-- changed by a person. A test with any of these can't be reloaded.
-- name: TouchedQuestions :one
SELECT count(DISTINCT q.id)::int
FROM practice_test_questions tq
JOIN questions q ON q.id = tq.question_id
WHERE tq.test_id = @test_id
  AND ((q.status = 'approved' AND NOT q.auto_approved)
       OR EXISTS (SELECT 1 FROM question_versions v WHERE v.question_id = q.id AND v.source = 'editor'));

-- name: DeleteTest :exec
DELETE FROM practice_tests WHERE id = @id;

-- Questions no test uses any more and nobody has touched (left behind by a reload).
-- name: DeleteOrphanQuestions :execrows
DELETE FROM questions q
WHERE (q.status = 'review' OR q.auto_approved)
  AND NOT EXISTS (SELECT 1 FROM question_versions v WHERE v.question_id = q.id AND v.source = 'editor')
  AND NOT EXISTS (SELECT 1 FROM practice_test_questions tq WHERE tq.question_id = q.id);

-- A new question; nothing comes back if one with the same text exists. A
-- green one is approved at once.
-- name: InsertQuestion :one
INSERT INTO questions (subject, type, content_hash, passage, stem, choices, figures, correct, accepted,
                       explanation, domain, skill, difficulty, ai_risk, status, approved_at, auto_approved)
VALUES (@subject, @type, @content_hash, sqlc.narg('passage'), @stem, sqlc.narg('choices'), @figures,
        sqlc.narg('correct'), sqlc.narg('accepted')::text[], @explanation, @domain, @skill,
        sqlc.narg('difficulty'), @ai_risk,
        CASE WHEN @ai_risk = 'green' THEN 'approved' ELSE 'review' END,
        CASE WHEN @ai_risk = 'green' THEN now() END, @ai_risk = 'green')
ON CONFLICT (content_hash) DO NOTHING
RETURNING id;

-- A repeat of a stored question: keep its content, raise its risk if this
-- review is worse. A review that isn't green takes an automatic approval back.
-- name: RaiseRisk :one
UPDATE questions
SET ai_risk = CASE WHEN array_position(ARRAY['green', 'yellow', 'red'], @ai_risk::text)
                        > coalesce(array_position(ARRAY['green', 'yellow', 'red'], ai_risk), 0)
                   THEN @ai_risk::text ELSE ai_risk END,
    status = CASE WHEN auto_approved AND @ai_risk::text <> 'green' THEN 'review' ELSE status END,
    approved_at = CASE WHEN auto_approved AND @ai_risk::text <> 'green' THEN NULL ELSE approved_at END,
    auto_approved = auto_approved AND @ai_risk::text = 'green'
WHERE content_hash = @content_hash
RETURNING id;

-- name: InsertVersion :exec
INSERT INTO question_versions (question_id, version, content, source, created_by)
VALUES (@question_id, @version, @content, @source, sqlc.narg('created_by'));

-- name: LinkQuestion :exec
INSERT INTO practice_test_questions (test_id, subject, module, position, question_id)
VALUES (@test_id, @subject, @module, @position, @question_id);

-- A loaded test's review record, again with what the fix step added. Only
-- while its risk and verdict are the same: a changed review is a reload.
-- name: RefreshAIReview :execrows
UPDATE question_ai_reviews SET record = @record
WHERE test_id = @test_id AND qid = @qid AND risk = @risk AND verdict = @verdict
  AND record IS DISTINCT FROM @record::jsonb;

-- name: InsertAIReview :exec
INSERT INTO question_ai_reviews (question_id, test_id, qid, csv_id, risk, verdict, record)
VALUES (@question_id, @test_id, @qid, @csv_id, @risk, @verdict, @record);

-- Every test with how far its review has come. A test opens to students when
-- all its questions are approved.
-- name: TestProgress :many
SELECT t.code, t.title,
       count(*)::int AS questions,
       count(*) FILTER (WHERE q.status = 'approved')::int AS approved,
       count(*) FILTER (WHERE q.ai_risk = 'red' AND q.status <> 'approved')::int AS red,
       count(*) FILTER (WHERE q.ai_risk = 'yellow' AND q.status <> 'approved')::int AS yellow,
       count(*) FILTER (WHERE EXISTS (SELECT 1 FROM practice_test_questions o
                                      WHERE o.question_id = q.id AND o.test_id <> t.id))::int AS shared
FROM practice_tests t
JOIN practice_test_questions tq ON tq.test_id = t.id
JOIN questions q ON q.id = tq.question_id
GROUP BY t.id
ORDER BY length(t.code), t.code;

-- name: TestSlots :many
SELECT tq.subject, tq.module, tq.position, q.id, q.type, q.status, q.ai_risk, q.passage, q.stem, q.version,
       (SELECT count(*) FROM practice_test_questions o WHERE o.question_id = q.id)::int AS uses
FROM practice_test_questions tq
JOIN questions q ON q.id = tq.question_id
WHERE tq.test_id = @test_id
ORDER BY tq.subject, tq.module, tq.position;

-- Questions to work on, worst first: filters are optional.
-- name: ListQuestions :many
SELECT q.id, q.subject, q.type, q.status, q.ai_risk, q.passage, q.stem, q.version, q.updated_at
FROM questions q
WHERE (sqlc.narg('risk')::text IS NULL OR q.ai_risk = sqlc.narg('risk'))
  AND (sqlc.narg('status')::text IS NULL OR q.status = sqlc.narg('status'))
  AND (sqlc.narg('subject')::text IS NULL OR q.subject = sqlc.narg('subject'))
ORDER BY coalesce(array_position(ARRAY['red', 'yellow', 'green'], q.ai_risk), 4), q.created_at, q.id
LIMIT @lim OFFSET @off;

-- name: CountQuestions :one
SELECT count(*)::int
FROM questions q
WHERE (sqlc.narg('risk')::text IS NULL OR q.ai_risk = sqlc.narg('risk'))
  AND (sqlc.narg('status')::text IS NULL OR q.status = sqlc.narg('status'))
  AND (sqlc.narg('subject')::text IS NULL OR q.subject = sqlc.narg('subject'));

-- Where questions are used: test, subject, module, position.
-- name: QuestionPlaces :many
SELECT tq.question_id, t.code, tq.subject, tq.module, tq.position
FROM practice_test_questions tq
JOIN practice_tests t ON t.id = tq.test_id
WHERE tq.question_id = ANY (@ids::uuid[])
ORDER BY length(t.code), t.code, tq.module, tq.position;

-- name: GetQuestion :one
SELECT * FROM questions WHERE id = @id;

-- name: LockQuestion :one
SELECT * FROM questions WHERE id = @id FOR UPDATE;

-- name: QuestionReviews :many
SELECT t.code, r.qid, r.risk, r.verdict, r.record, r.imported_at
FROM question_ai_reviews r
JOIN practice_tests t ON t.id = r.test_id
WHERE r.question_id = @question_id
ORDER BY length(t.code), t.code;

-- name: QuestionVersions :many
SELECT version, content, source, created_by, created_at
FROM question_versions
WHERE question_id = @question_id
ORDER BY version DESC;

-- An editor's change: a new version. An approved question (by a person or
-- automatically) goes back to review, so a teacher checks the change.
-- name: UpdateQuestion :one
UPDATE questions
SET passage = sqlc.narg('passage'), stem = @stem, choices = sqlc.narg('choices'), figures = @figures,
    correct = sqlc.narg('correct'), accepted = sqlc.narg('accepted')::text[], explanation = @explanation,
    domain = @domain, skill = @skill, difficulty = sqlc.narg('difficulty'),
    version = version + 1, updated_at = now(),
    status = CASE WHEN status = 'approved' THEN 'review' ELSE status END, approved_at = NULL, auto_approved = false,
    approved_by = NULL
WHERE id = @id
RETURNING version;

-- A person approves a question in the editor.
-- name: ApproveQuestion :exec
UPDATE questions SET status = 'approved', approved_at = now(), approved_by = @approved_by, auto_approved = false
WHERE id = @id;

-- A person takes an approval back (theirs, someone else's or the AI review's).
-- name: UnapproveQuestion :exec
UPDATE questions SET status = 'review', approved_at = NULL, approved_by = NULL, auto_approved = false
WHERE id = @id;

-- name: InsertImage :exec
INSERT INTO practice_images (key, content_type, bytes, width, height, uploaded_by)
VALUES (@key, @content_type, @bytes, @width, @height, @uploaded_by)
ON CONFLICT (key) DO NOTHING;

-- Which of these uploaded images exist.
-- name: ExistingImages :many
SELECT key FROM practice_images WHERE key = ANY (@keys::text[]);

-- Questions the AI review approved whose review proposed a fix not yet
-- applied (a repeat takes the first review's fix).
-- name: PendingAIFixes :many
SELECT q.id, r.fix::jsonb AS fix
FROM questions q
CROSS JOIN LATERAL (
    SELECT record->'proposed_fix' AS fix FROM question_ai_reviews
    WHERE question_id = q.id AND jsonb_typeof(record->'proposed_fix') = 'object'
    ORDER BY imported_at, id LIMIT 1
) r
WHERE q.auto_approved AND q.version = 1;

-- The AI review's fix, as a new version; the question stays approved.
-- name: ApplyAIFix :one
UPDATE questions
SET passage = sqlc.narg('passage'), stem = @stem, choices = sqlc.narg('choices'),
    correct = sqlc.narg('correct'), accepted = sqlc.narg('accepted')::text[],
    version = version + 1, updated_at = now()
WHERE id = @id
RETURNING version;
