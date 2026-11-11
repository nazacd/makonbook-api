-- Import ----------------------------------------------------------------------

-- name: UpsertList :one
INSERT INTO vocab_lists (slug, title, position)
VALUES (@slug, @title, @position)
ON CONFLICT (slug) DO UPDATE
SET title = EXCLUDED.title, position = EXCLUDED.position, removed_at = NULL, updated_at = now()
RETURNING id;

-- name: RemoveListsExcept :execrows
UPDATE vocab_lists SET removed_at = now(), updated_at = now()
WHERE removed_at IS NULL AND NOT (slug = ANY (@slugs::text[]));

-- name: UpsertWord :exec
INSERT INTO vocab_words (list_id, position, word, part_of_speech, definition, example, image)
VALUES (@list_id, @position, @word, @part_of_speech, @definition, @example, @image)
ON CONFLICT (list_id, lower(word)) DO UPDATE
SET position = EXCLUDED.position, word = EXCLUDED.word, part_of_speech = EXCLUDED.part_of_speech,
    definition = EXCLUDED.definition, example = EXCLUDED.example, image = EXCLUDED.image,
    removed_at = NULL, updated_at = now();

-- Words of a list that the file no longer has.
-- name: RemoveWordsExcept :execrows
UPDATE vocab_words SET removed_at = now(), updated_at = now()
WHERE list_id = @list_id AND removed_at IS NULL AND NOT (lower(word) = ANY (@words::text[]));

-- name: HaveImages :many
SELECT file FROM vocab_images WHERE file = ANY (@files::text[]);

-- name: InsertImage :exec
INSERT INTO vocab_images (file, content_type, bytes, width, height)
VALUES (@file, @content_type, @bytes, @width, @height)
ON CONFLICT (file) DO NOTHING;

-- name: SetDeckVersion :exec
INSERT INTO vocab_deck (version) VALUES (@version)
ON CONFLICT (singleton) DO UPDATE SET version = EXCLUDED.version, built_at = now();

-- The deck --------------------------------------------------------------------

-- name: DeckVersion :one
SELECT version FROM vocab_deck;

-- Every word students get, list by list.
-- name: DeckWords :many
SELECT l.slug, l.title, w.id, w.word, w.part_of_speech, w.definition, w.example, w.image,
       i.bytes AS image_bytes, i.width AS image_width, i.height AS image_height
FROM vocab_lists l
JOIN vocab_words w ON w.list_id = l.id AND w.removed_at IS NULL
LEFT JOIN vocab_images i ON i.file = w.image
WHERE l.removed_at IS NULL
ORDER BY l.position, w.position;

-- name: ListImages :many
SELECT DISTINCT w.image::text AS file
FROM vocab_lists l JOIN vocab_words w ON w.list_id = l.id AND w.removed_at IS NULL
WHERE l.slug = @slug AND l.removed_at IS NULL AND w.image IS NOT NULL;

-- name: CountWords :one
SELECT count(*)::int FROM vocab_words w JOIN vocab_lists l ON l.id = w.list_id
WHERE w.removed_at IS NULL AND l.removed_at IS NULL;

-- Reviews ---------------------------------------------------------------------

-- The words among ids a student may review: SAT MAKON's, and those of
-- decks the student owns or added (removed ones too: a late upload of a
-- review made before the word was taken out still counts).
-- name: KnownWords :many
SELECT id FROM vocab_words WHERE id = ANY (@ids::uuid[])
UNION
SELECT w.id FROM vocab_user_words w JOIN vocab_user_decks d ON d.id = w.deck_id
WHERE w.id = ANY (@ids::uuid[])
  AND (d.owner_client_id = @student::int
       OR EXISTS (SELECT 1 FROM vocab_deck_adds a WHERE a.deck_id = d.id AND a.student_client_id = @student::int));

-- name: InsertReview :execrows
INSERT INTO vocab_reviews (id, student_client_id, word_id, rating, reviewed_at, duration_ms, state, due,
                           stability, difficulty, scheduled_days)
VALUES (@id, @student_client_id, @word_id, @rating, @reviewed_at, @duration_ms, @state, @due,
        @stability, @difficulty, @scheduled_days)
ON CONFLICT (id) DO NOTHING;

-- A student's reviews after a point (seq), oldest first.
-- name: StudentReviews :many
SELECT seq, id, word_id, rating, reviewed_at, duration_ms, state, due, stability, difficulty, scheduled_days
FROM vocab_reviews
WHERE student_client_id = @student_client_id AND seq > @after
ORDER BY seq
LIMIT @lim;

-- Teachers --------------------------------------------------------------------

-- Whether a teacher teaches a group now or later (as people.TeacherGroups).
-- name: TeachesGroup :one
SELECT EXISTS (
    SELECT 1 FROM schedule_items s JOIN groups g ON g.id = s.group_id
    WHERE s.group_id = @group_id AND g.removed_at IS NULL AND @teacher_id::int = ANY (s.teacher_ids)
      AND (s.end_date IS NULL OR s.end_date >= @today::date));

-- Each student enrolled in the group now, with their vocabulary progress:
-- words started, words learned (the latest review scheduled the next one
-- 21 days or more away, Anki's "mature"), reviews in the last 7 days.
-- name: GroupProgress :many
WITH students_now AS (
    SELECT DISTINCT st.client_id, st.first_name, st.last_name
    FROM enrollments e JOIN students st ON st.client_id = e.student_client_id
    WHERE e.group_id = @group_id AND st.deactivated_at IS NULL
      AND (e.begin_date IS NULL OR e.begin_date <= @today::date)
      AND (e.end_date IS NULL OR e.end_date >= @today::date)
), latest AS (
    SELECT DISTINCT ON (r.student_client_id, r.word_id) r.student_client_id, r.scheduled_days
    FROM vocab_reviews r JOIN students_now s ON s.client_id = r.student_client_id
    ORDER BY r.student_client_id, r.word_id, r.reviewed_at DESC, r.seq DESC
)
SELECT s.client_id, s.first_name, s.last_name,
       (SELECT count(*) FROM latest l WHERE l.student_client_id = s.client_id)::int AS started,
       (SELECT count(*) FROM latest l WHERE l.student_client_id = s.client_id AND l.scheduled_days >= 21)::int AS learned,
       (SELECT count(*) FROM vocab_reviews r WHERE r.student_client_id = s.client_id
          AND r.reviewed_at >= now() - interval '7 days')::int AS reviews_week,
       (SELECT max(r.reviewed_at) FROM vocab_reviews r WHERE r.student_client_id = s.client_id)::timestamptz AS last_review
FROM students_now s
ORDER BY s.last_name, s.first_name;

-- Each student's games this week and points this month (GroupProgress's students).
-- name: GroupGames :many
SELECT student_client_id,
       count(*) FILTER (WHERE played_at >= @week_from)::int AS games_week,
       coalesce(sum(points) FILTER (WHERE played_at >= @month_from), 0)::int AS game_points_month
FROM vocab_game_results
WHERE student_client_id = ANY (@students::int[]) AND played_at >= least(@week_from, @month_from)
GROUP BY student_client_id;

-- The latest games of a group's students now, for its teacher.
-- name: GroupRecentGames :many
SELECT g.id, g.student_client_id, st.first_name, st.last_name, g.game, g.score, g.total, g.mistakes,
       g.duration_ms, g.missed, g.played_at
FROM vocab_game_results g JOIN students st ON st.client_id = g.student_client_id
WHERE g.student_client_id = ANY (@students::int[])
ORDER BY g.played_at DESC
LIMIT @lim;

-- Words by id, wherever they come from (for showing missed words).
-- name: WordsByID :many
SELECT id, word, definition FROM vocab_words WHERE id = ANY (@ids::uuid[])
UNION ALL
SELECT id, word, definition FROM vocab_user_words WHERE id = ANY (@ids::uuid[]);

-- Games ----------------------------------------------------------------------

-- Game points a student already has on a day (school time).
-- name: GamePointsBetween :one
SELECT coalesce(sum(points), 0)::int FROM vocab_game_results
WHERE student_client_id = @student_client_id AND played_at >= @since AND played_at < @until;

-- name: InsertGameResult :execrows
INSERT INTO vocab_game_results (id, student_client_id, game, score, total, mistakes, duration_ms, missed, played_at, points)
VALUES (@id, @student_client_id, @game, @score, @total, @mistakes, @duration_ms, @missed, @played_at, @points)
ON CONFLICT (id) DO NOTHING;

-- A student's games: plays and the best of each (Pairs: the quickest clean
-- round; the others: the highest score).
-- name: GameBests :many
SELECT game, count(*)::int AS plays,
       coalesce(min(duration_ms + mistakes * 5000) FILTER (WHERE game = 'pairs'), 0)::int AS best_ms,
       max(score)::int AS best_score,
       max(total)::int AS best_total,
       coalesce(sum(points), 0)::int AS points
FROM vocab_game_results WHERE student_client_id = @student_client_id
GROUP BY game;

-- Leaderboard ----------------------------------------------------------------

-- Everyone's points in a period: a point for each word reviewed on a day
-- (however often), and the games' points. Students still with us only.
-- name: Points :many
WITH r AS (
    SELECT student_client_id AS s, count(DISTINCT (word_id, (reviewed_at AT TIME ZONE @tz::text)::date))::int AS p
    FROM vocab_reviews WHERE reviewed_at >= @since AND reviewed_at < @until
    GROUP BY 1
), g AS (
    SELECT student_client_id AS s, sum(points)::int AS p
    FROM vocab_game_results WHERE played_at >= @since AND played_at < @until
    GROUP BY 1
), t AS (
    SELECT s, sum(p)::int AS points FROM (SELECT s, p FROM r UNION ALL SELECT s, p FROM g) x GROUP BY s
)
SELECT st.client_id, st.first_name, st.last_name, t.points, c.photo_id
FROM t JOIN students st ON st.client_id = t.s
LEFT JOIN credentials c ON c.person_kind = 'student' AND c.person_id = st.client_id
WHERE st.deactivated_at IS NULL AND t.points > 0
ORDER BY t.points DESC, st.client_id;

-- The students who share a group with this one now (this one included).
-- name: Classmates :many
SELECT DISTINCT e2.student_client_id
FROM enrollments e1 JOIN enrollments e2 ON e2.group_id = e1.group_id
JOIN groups g ON g.id = e1.group_id AND g.removed_at IS NULL
WHERE e1.student_client_id = @student_client_id
  AND (e1.begin_date IS NULL OR e1.begin_date <= @today::date) AND (e1.end_date IS NULL OR e1.end_date >= @today::date)
  AND (e2.begin_date IS NULL OR e2.begin_date <= @today::date) AND (e2.end_date IS NULL OR e2.end_date >= @today::date);

-- Decks ----------------------------------------------------------------------

-- name: CountOwnDecks :one
SELECT count(*)::int FROM vocab_user_decks WHERE owner_client_id = @owner AND deleted_at IS NULL;

-- name: InsertDeck :one
INSERT INTO vocab_user_decks (owner_client_id, title, description) VALUES (@owner, @title, @description)
RETURNING id;

-- name: GetDeck :one
SELECT d.*, st.first_name AS owner_first, st.last_name AS owner_last,
       (SELECT count(*) FROM vocab_deck_adds a WHERE a.deck_id = d.id)::int AS adds,
       (SELECT count(*) FROM vocab_deck_reports p WHERE p.deck_id = d.id AND p.resolved_at IS NULL)::int AS reports
FROM vocab_user_decks d JOIN students st ON st.client_id = d.owner_client_id
WHERE d.id = @id AND d.deleted_at IS NULL;

-- name: UpdateDeck :exec
UPDATE vocab_user_decks SET title = @title, description = @description, updated_at = now() WHERE id = @id;

-- name: DeleteDeck :exec
UPDATE vocab_user_decks SET deleted_at = now(), updated_at = now() WHERE id = @id;

-- name: SetPublished :exec
UPDATE vocab_user_decks SET published_at = CASE WHEN @publish::bool THEN coalesce(published_at, now()) END, updated_at = now()
WHERE id = @id;

-- name: HideDeck :exec
UPDATE vocab_user_decks SET hidden_at = now(), hidden_by = sqlc.narg(teacher), hidden_reason = @reason, updated_at = now()
WHERE id = @id;

-- name: UnhideDeck :exec
UPDATE vocab_user_decks SET hidden_at = NULL, hidden_by = NULL, hidden_reason = NULL, updated_at = now() WHERE id = @id;

-- name: ResolveReports :exec
UPDATE vocab_deck_reports SET resolved_at = now() WHERE deck_id = @deck_id AND resolved_at IS NULL;

-- name: DeckWordsOf :many
SELECT deck_id, id, word, part_of_speech, definition, example
FROM vocab_user_words WHERE deck_id = ANY (@decks::uuid[]) AND removed_at IS NULL
ORDER BY deck_id, position;

-- name: AllDeckWords :many
SELECT id, lower(word)::text AS key FROM vocab_user_words WHERE deck_id = @deck_id AND removed_at IS NULL;

-- name: InsertDeckWord :one
INSERT INTO vocab_user_words (deck_id, position, word, part_of_speech, definition, example)
VALUES (@deck_id, @position, @word, @part_of_speech, @definition, @example)
RETURNING id;

-- name: UpdateDeckWord :execrows
UPDATE vocab_user_words SET position = @position, word = @word, part_of_speech = @part_of_speech,
    definition = @definition, example = @example, updated_at = now()
WHERE id = @id AND deck_id = @deck_id AND removed_at IS NULL;

-- name: RemoveDeckWords :exec
UPDATE vocab_user_words SET removed_at = now(), updated_at = now()
WHERE deck_id = @deck_id AND removed_at IS NULL AND NOT (id = ANY (@keep::uuid[]));

-- A student's own decks and those they added, newest first.
-- name: MyDecks :many
SELECT d.id, d.owner_client_id, d.title, d.description, d.published_at, d.hidden_at, d.hidden_reason, d.updated_at,
       st.first_name AS owner_first, st.last_name AS owner_last,
       (SELECT count(*) FROM vocab_deck_adds a WHERE a.deck_id = d.id)::int AS adds
FROM vocab_user_decks d JOIN students st ON st.client_id = d.owner_client_id
WHERE d.deleted_at IS NULL
  AND (d.owner_client_id = @student::int
       OR (d.hidden_at IS NULL AND d.published_at IS NOT NULL
           AND EXISTS (SELECT 1 FROM vocab_deck_adds a WHERE a.deck_id = d.id AND a.student_client_id = @student::int)))
ORDER BY (d.owner_client_id = @student::int) DESC, d.created_at DESC;

-- The community: published decks nobody hid (teachers, with reported ones:
-- hidden ones too, and those with open reports first).
-- name: CommunityDecks :many
SELECT d.id, d.owner_client_id, d.title, d.description, d.published_at, d.hidden_at, d.hidden_reason,
       st.first_name AS owner_first, st.last_name AS owner_last,
       (SELECT count(*) FROM vocab_deck_adds a WHERE a.deck_id = d.id)::int AS adds,
       (SELECT count(*) FROM vocab_user_words w WHERE w.deck_id = d.id AND w.removed_at IS NULL)::int AS words,
       (SELECT count(*) FROM vocab_deck_reports p WHERE p.deck_id = d.id AND p.resolved_at IS NULL)::int AS reports,
       EXISTS (SELECT 1 FROM vocab_deck_adds a WHERE a.deck_id = d.id AND a.student_client_id = @student::int) AS added
FROM vocab_user_decks d JOIN students st ON st.client_id = d.owner_client_id
WHERE d.deleted_at IS NULL AND d.published_at IS NOT NULL
  AND (d.hidden_at IS NULL OR @moderation::bool)
  AND (NOT @moderation::bool OR d.hidden_at IS NOT NULL
       OR EXISTS (SELECT 1 FROM vocab_deck_reports p WHERE p.deck_id = d.id AND p.resolved_at IS NULL))
  AND (@q::text = '' OR d.title ILIKE '%' || @q::text || '%' OR d.description ILIKE '%' || @q::text || '%')
ORDER BY adds DESC, d.published_at DESC
LIMIT 200;

-- name: AddDeck :exec
INSERT INTO vocab_deck_adds (student_client_id, deck_id) VALUES (@student_client_id, @deck_id) ON CONFLICT DO NOTHING;

-- name: RemoveDeckAdd :exec
DELETE FROM vocab_deck_adds WHERE student_client_id = @student_client_id AND deck_id = @deck_id;

-- name: ReportDeck :exec
INSERT INTO vocab_deck_reports (deck_id, student_client_id, reason) VALUES (@deck_id, @student_client_id, @reason)
ON CONFLICT (deck_id, student_client_id) DO UPDATE SET reason = EXCLUDED.reason, created_at = now(), resolved_at = NULL;

-- name: OpenReports :one
SELECT count(*)::int FROM vocab_deck_reports WHERE deck_id = @deck_id AND resolved_at IS NULL;

-- name: ReportReasons :many
SELECT reason FROM vocab_deck_reports WHERE deck_id = @deck_id AND resolved_at IS NULL ORDER BY created_at;
