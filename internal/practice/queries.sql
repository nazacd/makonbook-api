-- Practice tests and their question bank (spec 9).

-- name: GetTestByCode :one
SELECT * FROM practice_tests WHERE code = @code;

-- name: CreateTest :one
INSERT INTO practice_tests (code, title, open_to_all) VALUES (@code, @title, @open_to_all) RETURNING *;

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

-- A question written outside the review waits for an editor; a repeat of a
-- stored question is skipped.
-- name: InsertDraft :one
INSERT INTO questions (subject, type, content_hash, passage, stem, choices, figures, correct, accepted,
                       explanation, domain, skill, difficulty, status)
VALUES (@subject, @type, @content_hash, sqlc.narg('passage'), @stem, sqlc.narg('choices'), '[]',
        sqlc.narg('correct'), sqlc.narg('accepted')::text[], @explanation, @domain, @skill,
        sqlc.narg('difficulty'), 'review')
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
                                      WHERE o.question_id = q.id AND o.test_id <> t.id))::int AS shared,
       coalesce((SELECT max(v.version) FROM practice_test_versions v WHERE v.test_id = t.id), 0)::int AS published
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

-- A question by its place in a reviewed test, as the questionbank names it.
-- name: QuestionByQID :one
SELECT r.question_id FROM question_ai_reviews r
JOIN practice_tests t ON t.id = r.test_id
WHERE t.code = @code AND r.qid = @qid;

-- Publishing (spec 9.5) ------------------------------------------------------

-- Tests whose questions are all approved: what can be published.
-- name: CompleteTests :many
SELECT t.id, t.code, t.title
FROM practice_tests t
WHERE EXISTS (SELECT 1 FROM practice_test_questions tq WHERE tq.test_id = t.id)
  AND NOT EXISTS (SELECT 1 FROM practice_test_questions tq JOIN questions q ON q.id = tq.question_id
                  WHERE tq.test_id = t.id AND q.status <> 'approved')
ORDER BY length(t.code), t.code;

-- A test's questions in order: Reading and Writing first, as in Bluebook.
-- name: TestContent :many
SELECT tq.module, tq.position, sqlc.embed(q)
FROM practice_test_questions tq
JOIN questions q ON q.id = tq.question_id
WHERE tq.test_id = @test_id
ORDER BY tq.subject, tq.module, tq.position;

-- name: LatestTestVersion :one
SELECT version, content_hash FROM practice_test_versions
WHERE test_id = @test_id ORDER BY version DESC LIMIT 1;

-- name: InsertTestVersion :exec
INSERT INTO practice_test_versions (test_id, version, content_hash, package, bytes)
VALUES (@test_id, @version, @content_hash, @package, @bytes);

-- name: PublishedImages :many
SELECT file, bytes, width, height FROM practice_published_images WHERE file = ANY (@files::text[]);

-- name: InsertPublishedImage :exec
INSERT INTO practice_published_images (file, content_type, bytes, width, height)
VALUES (@file, @content_type, @bytes, @width, @height)
ON CONFLICT (file) DO NOTHING;

-- For students: every test, its progress, and its latest published version.
-- name: Catalog :many
SELECT t.code, t.title, t.open_to_all,
       count(*)::int AS questions,
       count(*) FILTER (WHERE q.status = 'approved')::int AS approved,
       count(*) FILTER (WHERE tq.subject = 'english')::int AS english,
       count(*) FILTER (WHERE tq.subject = 'math')::int AS math,
       coalesce(v.version, 0)::int AS version, coalesce(v.bytes, 0)::int AS bytes, v.published_at
FROM practice_tests t
JOIN practice_test_questions tq ON tq.test_id = t.id
JOIN questions q ON q.id = tq.question_id
LEFT JOIN LATERAL (SELECT pv.version, pv.bytes, pv.published_at FROM practice_test_versions pv
                   WHERE pv.test_id = t.id ORDER BY pv.version DESC LIMIT 1) v ON true
GROUP BY t.id, v.version, v.bytes, v.published_at
ORDER BY length(t.code), t.code;

-- Whether registered students (spec 21) may take a test.
-- name: TestOpenToAll :one
SELECT open_to_all FROM practice_tests WHERE code = @code;

-- A published version of a test; version 0 is the latest.
-- name: TestVersion :one
SELECT t.id, t.code, t.title, v.version, v.package
FROM practice_tests t
JOIN practice_test_versions v ON v.test_id = t.id
WHERE t.code = @code AND (@version::int = 0 OR v.version = @version::int)
ORDER BY v.version DESC
LIMIT 1;

-- Attempts --------------------------------------------------------------------

-- name: InsertAttempt :execrows
INSERT INTO practice_attempts (id, student_client_id, test_id, test_version, sections, started_at, finished_at,
                               answers, scores)
VALUES (@id, @student_client_id, @test_id, @test_version, @sections, @started_at, @finished_at, @answers, @scores)
ON CONFLICT (id) DO NOTHING;

-- name: GetAttempt :one
SELECT a.id, a.student_client_id, t.code, t.title, a.test_version, a.sections, a.started_at, a.finished_at, a.scores
FROM practice_attempts a JOIN practice_tests t ON t.id = a.test_id
WHERE a.id = @id;

-- A student's own attempts with their answers (their history on a new phone).
-- name: OwnAttempts :many
SELECT a.id, a.student_client_id, t.code, t.title, a.test_version, a.sections, a.started_at, a.finished_at, a.scores, a.answers
FROM practice_attempts a JOIN practice_tests t ON t.id = a.test_id
WHERE a.student_client_id = @client_id::int
ORDER BY a.finished_at DESC;

-- name: StudentAttempts :many
SELECT a.id, a.student_client_id, t.code, t.title, a.test_version, a.sections, a.started_at, a.finished_at, a.scores
FROM practice_attempts a JOIN practice_tests t ON t.id = a.test_id
WHERE a.student_client_id = ANY (@client_ids::int[])
ORDER BY a.finished_at DESC;

-- Whether a teacher teaches a group now or later (as people.TeacherGroups).
-- name: TeachesGroup :one
SELECT EXISTS (
    SELECT 1 FROM schedule_items s JOIN groups g ON g.id = s.group_id
    WHERE s.group_id = @group_id AND g.removed_at IS NULL AND @teacher_id::int = ANY (s.teacher_ids)
      AND (s.end_date IS NULL OR s.end_date >= @today::date));

-- The students enrolled in a group now.
-- name: GroupStudents :many
SELECT DISTINCT st.client_id, st.first_name, st.last_name
FROM enrollments e JOIN students st ON st.client_id = e.student_client_id
WHERE e.group_id = @group_id AND st.deactivated_at IS NULL
  AND (e.begin_date IS NULL OR e.begin_date <= @today::date)
  AND (e.end_date IS NULL OR e.end_date >= @today::date)
ORDER BY st.last_name, st.first_name;

-- Question statistics (spec 9.6) ----------------------------------------------
-- Not the demo account's attempts (ids -1 … -999999, internal/demo): its
-- results are made up. Self-registered students' (further down) count.

-- Each question place of each version students took, with how many attempts
-- took its section and the question's key (to grade the answers below).
-- name: StatsSlots :many
WITH taken AS (
    SELECT a.test_id, a.test_version, s.subject::text AS subject, count(*)::int AS attempts
    FROM practice_attempts a
    CROSS JOIN LATERAL unnest(a.sections) AS s(subject)
    WHERE a.student_client_id NOT BETWEEN -999999 AND -1
    GROUP BY a.test_id, a.test_version, s.subject
)
SELECT t.test_id, t.test_version, t.subject,
       (m.value->>'module')::smallint AS module, (q.value->>'position')::smallint AS position,
       jsonb_build_object('id', q.value->'id', 'type', q.value->'type', 'correct', q.value->'correct',
                          'accepted', q.value->'accepted')::jsonb AS question,
       t.attempts
FROM taken t
JOIN practice_test_versions v ON v.test_id = t.test_id AND v.version = t.test_version
CROSS JOIN LATERAL jsonb_array_elements(v.package->'sections') AS sec(value)
CROSS JOIN LATERAL jsonb_array_elements(sec.value->'modules') AS m(value)
CROSS JOIN LATERAL jsonb_array_elements(m.value->'questions') AS q(value)
WHERE sec.value->>'subject' = t.subject;

-- The answers in those attempts, counted per place and answer (blank ones
-- are left out: they are wrong).
-- name: StatsAnswers :many
SELECT a.test_id, a.test_version, (x.value->>'subject')::text AS subject,
       (x.value->>'module')::smallint AS module, (x.value->>'position')::smallint AS position,
       (x.value->>'answer')::text AS answer, count(*)::int AS answers
FROM practice_attempts a
CROSS JOIN LATERAL jsonb_array_elements(a.answers) AS x(value)
WHERE a.student_client_id NOT BETWEEN -999999 AND -1 AND x.value->>'answer' IS NOT NULL
GROUP BY a.test_id, a.test_version, x.value->>'subject', x.value->>'module', x.value->>'position', x.value->>'answer';

-- "Ask later" (spec 9.6) -------------------------------------------------------

-- The question at a place in a published version: what a device says about a
-- question must match its package.
-- name: VersionSlot :one
SELECT (q.value->>'id')::text AS question_id
FROM practice_tests t
JOIN practice_test_versions v ON v.test_id = t.id
CROSS JOIN LATERAL jsonb_array_elements(v.package->'sections') AS sec(value)
CROSS JOIN LATERAL jsonb_array_elements(sec.value->'modules') AS m(value)
CROSS JOIN LATERAL jsonb_array_elements(m.value->'questions') AS q(value)
WHERE t.code = @code AND v.version = @version::int AND sec.value->>'subject' = @subject::text
  AND (m.value->>'module')::int = @module::int AND (q.value->>'position')::int = @position::int
LIMIT 1;

-- A student's parked question, new or changed; nothing comes back when the id
-- is another student's.
-- name: UpsertParked :one
INSERT INTO parked_questions (id, student_client_id, test, version, subject, module, position, question_id,
                              folder, reason, note, fix, status)
VALUES (@id, @student_client_id, @test, @version, @subject, @module, @position, @question_id,
        @folder, sqlc.narg('reason'), @note, @fix, @status)
ON CONFLICT (id) DO UPDATE
SET test = EXCLUDED.test, version = EXCLUDED.version, subject = EXCLUDED.subject, module = EXCLUDED.module,
    position = EXCLUDED.position, question_id = EXCLUDED.question_id, folder = EXCLUDED.folder,
    reason = EXCLUDED.reason, note = EXCLUDED.note, fix = EXCLUDED.fix, status = EXCLUDED.status,
    updated_at = now()
WHERE parked_questions.student_client_id = EXCLUDED.student_client_id
RETURNING *;

-- Students' parked questions, newest first.
-- name: ParkedOf :many
SELECT * FROM parked_questions
WHERE student_client_id = ANY (@client_ids::int[])
ORDER BY created_at DESC, id;

-- name: DeleteParked :execrows
DELETE FROM parked_questions WHERE id = @id AND student_client_id = @student_client_id;

-- Issue reports (spec 9.6) -----------------------------------------------------

-- name: InsertReport :execrows
INSERT INTO practice_reports (id, student_client_id, test, version, subject, module, position, question_id,
                              kind, reason, text)
VALUES (@id, @student_client_id, @test, @version, @subject, @module, @position, @question_id,
        @kind, sqlc.narg('reason'), @text)
ON CONFLICT (id) DO NOTHING;

-- name: ReportOwner :one
SELECT student_client_id FROM practice_reports WHERE id = @id;

-- Reports for the admin panel, newest first.
-- name: AdminReports :many
SELECT r.*, s.first_name, s.last_name, coalesce(t.title, '')::text AS test_title
FROM practice_reports r
JOIN students s ON s.client_id = r.student_client_id
LEFT JOIN practice_tests t ON t.code = r.test
WHERE r.status = @status
ORDER BY r.created_at DESC, r.id
LIMIT 200;

-- name: AdminReport :one
SELECT r.*, s.first_name, s.last_name, coalesce(t.title, '')::text AS test_title
FROM practice_reports r
JOIN students s ON s.client_id = r.student_client_id
LEFT JOIN practice_tests t ON t.code = r.test
WHERE r.id = @id;

-- Resolving keeps who resolved it first; reopening clears it.
-- name: SetReportStatus :execrows
UPDATE practice_reports
SET status = @status::text,
    resolved_at = CASE WHEN @status::text = 'resolved' THEN coalesce(resolved_at, now()) END,
    resolved_by = CASE WHEN @status::text = 'resolved' THEN coalesce(resolved_by, @resolved_by::text) END
WHERE id = @id;

-- Goals (spec 9.6) -------------------------------------------------------------

-- name: Goals :one
SELECT * FROM student_goals WHERE student_client_id = @student_client_id;

-- name: SetGoals :one
INSERT INTO student_goals (student_client_id, target_math, target_rw, exam_date)
VALUES (@student_client_id, sqlc.narg('target_math'), sqlc.narg('target_rw'), sqlc.narg('exam_date'))
ON CONFLICT (student_client_id) DO UPDATE
SET target_math = EXCLUDED.target_math, target_rw = EXCLUDED.target_rw, exam_date = EXCLUDED.exam_date,
    updated_at = now()
RETURNING *;

-- Roadmaps (spec 22) draw their level checks and homework from the bank.
-- name: BankQuestions :many
SELECT * FROM questions WHERE id = ANY (@ids::uuid[]);

-- name: Candidates :many
SELECT id, difficulty FROM questions
WHERE status = 'approved' AND subject = @subject AND lower(skill) = ANY (@skills::text[]);

-- name: ApprovedCounts :many
SELECT skill, coalesce(difficulty, '')::text AS difficulty, count(*)::int AS n
FROM questions WHERE status = 'approved' AND subject = @subject
GROUP BY skill, difficulty;

-- name: FigureFiles :many
SELECT f.key, i.file, i.width, i.height
FROM figure_files f JOIN practice_published_images i ON i.file = f.file
WHERE f.key = ANY (@keys::text[]);

-- name: InsertFigureFile :exec
INSERT INTO figure_files (key, file) VALUES (@key, @file)
ON CONFLICT (key) DO UPDATE SET file = excluded.file;

-- Approved questions for teachers to put in a homework (spec 23.2), newest
-- approved first. q is an ILIKE pattern.
-- name: ApprovedQuestions :many
SELECT id, type, domain, skill, difficulty, passage, stem FROM questions
WHERE status = 'approved' AND subject = @subject
  AND (sqlc.narg(skill)::text IS NULL OR lower(skill) = lower(sqlc.narg(skill)::text))
  AND (sqlc.narg(difficulty)::text IS NULL OR difficulty = sqlc.narg(difficulty)::text)
  AND (sqlc.narg(q)::text IS NULL OR stem ILIKE sqlc.narg(q)::text OR passage ILIKE sqlc.narg(q)::text)
ORDER BY approved_at DESC, id
LIMIT @lim OFFSET @off;

-- name: CountApprovedQuestions :one
SELECT count(*)::int FROM questions
WHERE status = 'approved' AND subject = @subject
  AND (sqlc.narg(skill)::text IS NULL OR lower(skill) = lower(sqlc.narg(skill)::text))
  AND (sqlc.narg(difficulty)::text IS NULL OR difficulty = sqlc.narg(difficulty)::text)
  AND (sqlc.narg(q)::text IS NULL OR stem ILIKE sqlc.narg(q)::text OR passage ILIKE sqlc.narg(q)::text);
