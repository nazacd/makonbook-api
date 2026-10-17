-- Practice tests and their question bank (spec 9).

-- name: GetTestByCode :one
SELECT * FROM practice_tests WHERE code = @code;

-- name: CreateTest :one
INSERT INTO practice_tests (code, title) VALUES (@code, @title) RETURNING *;

-- Questions of a test that people have already worked on: approved, or
-- changed after the import. A test with any of these can't be reloaded.
-- name: TouchedQuestions :one
SELECT count(DISTINCT q.id)::int
FROM practice_test_questions tq
JOIN questions q ON q.id = tq.question_id
WHERE tq.test_id = @test_id AND (q.status = 'approved' OR q.version > 1);

-- name: DeleteTest :exec
DELETE FROM practice_tests WHERE id = @id;

-- Questions no test uses any more and nobody has touched (left behind by a reload).
-- name: DeleteOrphanQuestions :execrows
DELETE FROM questions q
WHERE q.status = 'review' AND q.version = 1
  AND NOT EXISTS (SELECT 1 FROM practice_test_questions tq WHERE tq.question_id = q.id);

-- A new question; nothing comes back if one with the same text exists.
-- name: InsertQuestion :one
INSERT INTO questions (subject, type, content_hash, passage, stem, choices, figures, correct, accepted,
                       explanation, domain, skill, difficulty, ai_risk)
VALUES (@subject, @type, @content_hash, sqlc.narg('passage'), @stem, sqlc.narg('choices'), @figures,
        sqlc.narg('correct'), sqlc.narg('accepted')::text[], @explanation, @domain, @skill,
        sqlc.narg('difficulty'), @ai_risk)
ON CONFLICT (content_hash) DO NOTHING
RETURNING id;

-- A repeat of a stored question: keep its content, raise its risk if this
-- review is worse.
-- name: RaiseRisk :one
UPDATE questions
SET ai_risk = CASE WHEN array_position(ARRAY['green', 'yellow', 'red'], @ai_risk::text)
                        > coalesce(array_position(ARRAY['green', 'yellow', 'red'], ai_risk), 0)
                   THEN @ai_risk::text ELSE ai_risk END
WHERE content_hash = @content_hash
RETURNING id;

-- name: InsertVersion :exec
INSERT INTO question_versions (question_id, version, content, source, created_by)
VALUES (@question_id, @version, @content, @source, sqlc.narg('created_by'));

-- name: LinkQuestion :exec
INSERT INTO practice_test_questions (test_id, subject, module, position, question_id)
VALUES (@test_id, @subject, @module, @position, @question_id);

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
