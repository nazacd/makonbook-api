-- Gamification (spec 27): the XP ledger, the streak's days, the quests and
-- the achievements. The backfill reads what other features recorded
-- (attempts, assessments, reviews, games, reading, attendance) to write the
-- ledger from history.

-- A thing done, once: the same key again changes nothing (0 rows).
-- name: InsertEvent :execrows
INSERT INTO xp_events (student_client_id, key, kind, title, amount, right_answers, happened_at, percent, questions, score, units, run)
VALUES (@student_client_id, @key, @kind, @title, @amount, @right_answers, @happened_at, @percent, @questions, @score, @units, @run)
ON CONFLICT (student_client_id, key) DO NOTHING;

-- A day's aggregate: the amount grows up to the cap; returns the day's amount.
-- name: AccumulateEvent :one
INSERT INTO xp_events AS e (student_client_id, key, kind, title, amount, right_answers, happened_at, units)
VALUES (@student_client_id, @key, @kind, @title, LEAST(@amount::int, @cap::int), @right_answers, @happened_at, @units)
ON CONFLICT (student_client_id, key) DO UPDATE
    SET amount = LEAST(@cap::int, e.amount + @amount::int),
        right_answers = e.right_answers + @right_answers::int,
        units = e.units + @units::int,
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

-- What the achievements look at beyond the totals (spec 27).
-- name: Facts :one
SELECT count(*) FILTER (WHERE kind = 'attempt')::int AS tests,
       count(*) FILTER (WHERE kind = 'competition')::int AS competitions,
       count(*) FILTER (WHERE kind = 'read')::int AS reading_days,
       count(*) FILTER (WHERE kind = 'attendance')::int AS lessons,
       count(*) FILTER (WHERE kind = 'vocab')::int AS review_days,
       COALESCE(sum(units) FILTER (WHERE kind = 'vocab'), 0)::int AS reviews,
       COALESCE(max(percent) FILTER (WHERE kind IN ('assessment', 'competition') AND questions >= 10), 0)::int AS best_percent,
       COALESCE(max(score) FILTER (WHERE kind = 'attempt'), 0)::int AS best_score,
       count(*) FILTER (WHERE kind <> 'quest' AND extract(hour FROM happened_at AT TIME ZONE @tz::text) < 4)::int AS night,
       count(*) FILTER (WHERE kind <> 'quest' AND extract(hour FROM happened_at AT TIME ZONE @tz::text) BETWEEN 4 AND 6)::int AS early,
       COALESCE(max(run), 0)::int AS best_run,
       count(*) FILTER (WHERE kind = 'graduation')::int AS graduations,
       count(*) FILTER (WHERE kind = 'podium')::int AS podiums,
       EXISTS (SELECT 1 FROM vocab_game_results r WHERE r.student_client_id = @student_client_id AND r.game = 'pairs' AND r.mistakes = 0)::bool AS perfect_pairs
FROM xp_events WHERE student_client_id = @student_client_id;

-- Days with all three daily quests done, and weeks with all six.
-- name: FullDays :one
SELECT count(*) FILTER (WHERE n = 3)::int AS full_days,
       count(*) FILTER (WHERE n = 6)::int AS full_weeks
FROM (SELECT period_start, count(*) AS n FROM quest_progress
      WHERE student_client_id = @student_client_id AND done_at IS NOT NULL GROUP BY period_start) p;

-- The student's goals, for the achievements about them (null when unset).
-- name: GoalFacts :one
SELECT (g.university_id IS NOT NULL)::bool AS has_dream,
       COALESCE(g.target_math + g.target_rw, 0)::int AS target_total
FROM student_goals g WHERE g.student_client_id = @student_client_id;

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
       (SELECT COALESCE(sum((v.value->>'raw')::int), 0) FROM jsonb_each(a.scores) v)::int AS right_answers,
       (SELECT COALESCE(sum((v.value->>'questions')::int), 0) FROM jsonb_each(a.scores) v)::int AS questions,
       (CASE WHEN jsonb_typeof(a.scores->'english') = 'object' AND jsonb_typeof(a.scores->'math') = 'object'
             THEN (a.scores->'english'->>'score')::int + (a.scores->'math'->>'score')::int ELSE 0 END)::int AS score
FROM practice_attempts a JOIN practice_tests t ON t.id = a.test_id
ORDER BY a.finished_at;

-- name: PastAssessments :many
SELECT a.id, a.student_client_id, a.finished_at, a.kind, COALESCE(a.correct, 0)::int AS correct, COALESCE(a.total, 0)::int AS total,
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

-- Mako's room (spec 28).
-- name: RoomLayout :one
SELECT slots, seen FROM room_layouts WHERE student_client_id = @student_client_id;

-- Places an item in a slot; the other slots stay.
-- name: SetRoomSlot :exec
INSERT INTO room_layouts (student_client_id, slots)
VALUES (@student_client_id, jsonb_build_object(@slot::text, @item::text))
ON CONFLICT (student_client_id) DO UPDATE
    SET slots = room_layouts.slots || jsonb_build_object(@slot::text, @item::text), updated_at = now();

-- Adds to the items already shown as new.
-- name: AddRoomSeen :exec
INSERT INTO room_layouts (student_client_id, seen)
VALUES (@student_client_id, @seen::text[])
ON CONFLICT (student_client_id) DO UPDATE
    SET seen = ARRAY(SELECT DISTINCT unnest(room_layouts.seen || @seen::text[]) ORDER BY 1), updated_at = now();
