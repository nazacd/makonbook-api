-- +goose Up
-- Practice sets (spec 22.8): sets a student builds from the question bank
-- (topics, difficulty, how many, time a question), run like a lesson's
-- level check and graded on submit, outside any roadmap or assignment.
-- `practice` holds what was asked for. One unfinished set at a time (the
-- open index already allows one per kind with no lesson or assignment).
ALTER TABLE roadmap_assessments
    DROP CONSTRAINT roadmap_assessments_kind_check,
    ADD CONSTRAINT roadmap_assessments_kind_check CHECK (kind IN ('level_check', 'homework', 'practice')),
    ADD COLUMN practice jsonb,
    DROP CONSTRAINT roadmap_assessments_lesson_check,
    ADD CONSTRAINT roadmap_assessments_lesson_check CHECK (lesson_id IS NOT NULL OR assignment_id IS NOT NULL OR kind = 'practice'),
    ADD CONSTRAINT roadmap_assessments_practice_check CHECK ((kind = 'practice') = (practice IS NOT NULL));
CREATE INDEX roadmap_assessments_practice_idx ON roadmap_assessments (student_client_id, created_at DESC) WHERE kind = 'practice';

-- +goose Down
DELETE FROM roadmap_assessments WHERE kind = 'practice';
DROP INDEX roadmap_assessments_practice_idx;
ALTER TABLE roadmap_assessments
    DROP CONSTRAINT roadmap_assessments_practice_check,
    DROP COLUMN practice,
    DROP CONSTRAINT roadmap_assessments_lesson_check,
    ADD CONSTRAINT roadmap_assessments_lesson_check CHECK (lesson_id IS NOT NULL OR assignment_id IS NOT NULL),
    DROP CONSTRAINT roadmap_assessments_kind_check,
    ADD CONSTRAINT roadmap_assessments_kind_check CHECK (kind IN ('level_check', 'homework'));
