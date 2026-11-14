-- +goose Up
-- Competitions (spec 26): a set of questions everyone takes once, in a
-- window, against a time limit; a live board ranks them by right answers,
-- then time. Editors make them in the admin panel from bank topics (source
-- bank) or from questions written for them (source reserved). Strict:
-- one attempt, the server keeps the clock, and answers and explanations
-- open only when the competition ends.
CREATE TABLE competitions (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title          text NOT NULL CHECK (length(title) BETWEEN 1 AND 120),
    description    text NOT NULL DEFAULT '' CHECK (length(description) <= 2000),
    prize          text NOT NULL DEFAULT '' CHECK (length(prize) <= 300),   -- shown as it is; empty: none
    subject        text NOT NULL CHECK (subject IN ('math', 'english', 'mixed')),
    source         text NOT NULL CHECK (source IN ('bank', 'reserved')),
    question_ids   uuid[] NOT NULL DEFAULT '{}',
    starts_at      timestamptz NOT NULL,
    ends_at        timestamptz NOT NULL,
    minutes        integer NOT NULL CHECK (minutes BETWEEN 1 AND 300),     -- each attempt's time limit
    published_at   timestamptz,
    created_by     text NOT NULL,                                          -- the editor, "Employee:12"
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    deleted_at     timestamptz,
    CHECK (ends_at > starts_at)
);
CREATE INDEX competitions_window ON competitions (starts_at DESC) WHERE deleted_at IS NULL;

-- Questions kept out of the bank (practice, searches, draws, saving) while a
-- competition holds them: a published one until it ends, and one written
-- for a competition (reserved) from the moment it is chosen.
CREATE VIEW held_questions AS
SELECT DISTINCT unnest(question_ids)::uuid AS question_id FROM competitions
WHERE deleted_at IS NULL AND ends_at > now() AND (published_at IS NOT NULL OR source = 'reserved');

-- A competition attempt is an assessment of kind competition: one per
-- student and competition, ever. Competition attempts stay out of the
-- one-open-at-a-time rule (a student may be in two competitions at once).
ALTER TABLE roadmap_assessments
    DROP CONSTRAINT roadmap_assessments_kind_check,
    ADD CONSTRAINT roadmap_assessments_kind_check CHECK (kind IN ('level_check', 'homework', 'practice', 'competition')),
    ADD COLUMN competition_id uuid REFERENCES competitions ON DELETE CASCADE,
    DROP CONSTRAINT roadmap_assessments_lesson_check,
    ADD CONSTRAINT roadmap_assessments_lesson_check CHECK (lesson_id IS NOT NULL OR assignment_id IS NOT NULL OR kind IN ('practice', 'competition')),
    ADD CONSTRAINT roadmap_assessments_competition_check CHECK ((kind = 'competition') = (competition_id IS NOT NULL));
CREATE UNIQUE INDEX roadmap_assessments_competition_once ON roadmap_assessments (student_client_id, competition_id) WHERE competition_id IS NOT NULL;
DROP INDEX roadmap_assessments_open_idx;
CREATE UNIQUE INDEX roadmap_assessments_open_idx ON roadmap_assessments (
    student_client_id, coalesce(lesson_id, ''), kind, coalesce(homework_index, -1),
    coalesce(assignment_id, '00000000-0000-0000-0000-000000000000'::uuid)) WHERE finished_at IS NULL AND kind <> 'competition';

-- +goose Down
DELETE FROM roadmap_assessments WHERE kind = 'competition';
DROP INDEX roadmap_assessments_open_idx;
CREATE UNIQUE INDEX roadmap_assessments_open_idx ON roadmap_assessments (
    student_client_id, coalesce(lesson_id, ''), kind, coalesce(homework_index, -1),
    coalesce(assignment_id, '00000000-0000-0000-0000-000000000000'::uuid)) WHERE finished_at IS NULL;
DROP INDEX roadmap_assessments_competition_once;
ALTER TABLE roadmap_assessments
    DROP CONSTRAINT roadmap_assessments_competition_check,
    DROP CONSTRAINT roadmap_assessments_lesson_check,
    ADD CONSTRAINT roadmap_assessments_lesson_check CHECK (lesson_id IS NOT NULL OR assignment_id IS NOT NULL OR kind = 'practice'),
    DROP COLUMN competition_id,
    DROP CONSTRAINT roadmap_assessments_kind_check,
    ADD CONSTRAINT roadmap_assessments_kind_check CHECK (kind IN ('level_check', 'homework', 'practice'));
DROP VIEW held_questions;
DROP TABLE competitions;
