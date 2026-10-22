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

-- name: KnownWords :many
SELECT id FROM vocab_words WHERE id = ANY (@ids::uuid[]);

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
