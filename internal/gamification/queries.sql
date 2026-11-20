-- Gamification (spec 27): the XP ledger, the streak's days, the quests and
-- the achievements. The backfill reads what other features recorded
-- (attempts, assessments, reviews, games, reading, attendance) to write the
-- ledger from history.

-- A thing done, once: the same key again changes nothing (0 rows).
-- name: InsertEvent :execrows
INSERT INTO xp_events (student_client_id, key, kind, title, amount, right_answers, happened_at)
VALUES (@student_client_id, @key, @kind, @title, @amount, @right_answers, @happened_at)
ON CONFLICT (student_client_id, key) DO NOTHING;

-- A day's aggregate: the amount grows up to the cap; returns the day's amount.
-- name: AccumulateEvent :one
INSERT INTO xp_events AS e (student_client_id, key, kind, title, amount, right_answers, happened_at)
VALUES (@student_client_id, @key, @kind, @title, LEAST(@amount::int, @cap::int), @right_answers, @happened_at)
ON CONFLICT (student_client_id, key) DO UPDATE
    SET amount = LEAST(@cap::int, e.amount + @amount::int),
        right_answers = e.right_answers + @right_answers::int,
        happened_at = GREATEST(e.happened_at, @happened_at::timestamptz)
RETURNING e.amount;

-- name: MarkDay :exec
INSERT INTO student_days (student_client_id, day) VALUES (@student_client_id, @day)
ON CONFLICT DO NOTHING;

-- Every active day, newest first.
-- name: Days :many
SELECT day FROM student_days WHERE student_client_id = @student_client_id ORDER BY day DESC;

-- name: DaysBetween :one
SELECT count(*) FROM student_days
WHERE student_client_id = @student_client_id AND day >= @since AND day < @until;

-- name: Totals :one
SELECT COALESCE(sum(amount), 0)::bigint AS xp_total,
       COALESCE(sum(amount) FILTER (WHERE happened_at >= @today_start), 0)::bigint AS xp_today,
       COALESCE(sum(right_answers), 0)::bigint AS right_answers
FROM xp_events WHERE student_client_id = @student_client_id;

-- name: RecentEvents :many
SELECT title, amount, happened_at FROM xp_events
WHERE student_client_id = @student_client_id ORDER BY happened_at DESC, id DESC LIMIT @lim;

-- name: EnsureQuest :exec
INSERT INTO quest_progress (student_client_id, code, period_start, target, reward_xp)
VALUES (@student_client_id, @code, @period_start, @target, @reward_xp)
ON CONFLICT DO NOTHING;

-- Progress towards an unfinished quest: added to, or set (the week's active days).
-- name: AdvanceQuest :one
UPDATE quest_progress
SET progress = LEAST(target, CASE WHEN @absolute::bool THEN @n::int ELSE progress + @n::int END)
WHERE student_client_id = @student_client_id AND code = @code AND period_start = @period_start AND done_at IS NULL
RETURNING progress, target, reward_xp;

-- name: FinishQuest :execrows
UPDATE quest_progress SET done_at = @done_at
WHERE student_client_id = @student_client_id AND code = @code AND period_start = @period_start AND done_at IS NULL;

-- name: QuestsAt :many
SELECT code, period_start, progress, target, reward_xp, done_at FROM quest_progress
WHERE student_client_id = @student_client_id AND period_start = ANY(@periods::date[]);

-- name: QuestsDone :one
SELECT count(*) FROM quest_progress WHERE student_client_id = @student_client_id AND done_at IS NOT NULL;

-- name: UnlockAchievement :execrows
INSERT INTO student_achievements (student_client_id, code, unlocked_at) VALUES (@student_client_id, @code, @unlocked_at)
ON CONFLICT DO NOTHING;

-- name: Achievements :many
SELECT code, unlocked_at FROM student_achievements WHERE student_client_id = @student_client_id;

-- The backfill: everything a student did that earns XP, read from the other
-- features' tables, oldest first. Each row carries what Award needs.

-- name: Wipe :exec
TRUNCATE xp_events, student_days, quest_progress, student_achievements;

-- name: PastAttempts :many
SELECT a.id, a.student_client_id, a.finished_at, t.title,
       (SELECT COALESCE(sum((v.value->>'raw')::int), 0) FROM jsonb_each(a.scores) v)::int AS right_answers
FROM practice_attempts a JOIN practice_tests t ON t.id = a.test_id
ORDER BY a.finished_at;

-- name: PastAssessments :many
SELECT a.id, a.student_client_id, a.finished_at, a.kind, COALESCE(a.correct, 0)::int AS correct,
       COALESCE(a.percent, 0)::int AS percent, (a.assignment_id IS NOT NULL)::bool AS assigned,
       COALESCE(a.assignment_id IS NOT NULL AND a.finished_at <= s.due_at, false)::bool AS on_time, s.title AS assignment_title
FROM roadmap_assessments a LEFT JOIN assignments s ON s.id = a.assignment_id
WHERE a.finished_at IS NOT NULL
ORDER BY a.finished_at;

-- name: PastReviewDays :many
SELECT student_client_id, (reviewed_at AT TIME ZONE @tz::text)::date AS day, count(*)::int AS reviews, max(reviewed_at)::timestamptz AS last_at
FROM vocab_reviews GROUP BY 1, 2 ORDER BY last_at;

-- name: PastGameDays :many
SELECT student_client_id, (played_at AT TIME ZONE @tz::text)::date AS day, sum(score)::int AS right_answers, max(played_at)::timestamptz AS last_at
FROM vocab_game_results GROUP BY 1, 2 ORDER BY last_at;

-- name: PastReading :many
SELECT person_id AS student_client_id, (updated_at AT TIME ZONE @tz::text)::date AS day, max(updated_at)::timestamptz AS last_at
FROM library_progress WHERE person_kind = 'student' GROUP BY 1, 2 ORDER BY last_at;

-- name: PastAttendance :many
SELECT m.session_id, m.student_client_id, COALESCE(m.scanned_at, m.updated_at)::timestamptz AS at
FROM attendance_marks m WHERE m.state IN ('present', 'late') ORDER BY at;
