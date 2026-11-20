-- +goose Up
-- Gamification (spec 27): what Award learns about each thing done, so the
-- achievements can look at more than totals: the share right and the number
-- of questions (assessments and attempts), a mock test's total score, and
-- the units behind a day's aggregate (reviews, pages). Null where it doesn't apply.
ALTER TABLE xp_events
    ADD COLUMN percent   smallint CHECK (percent BETWEEN 0 AND 100),
    ADD COLUMN questions integer CHECK (questions >= 0),
    ADD COLUMN score     smallint CHECK (score BETWEEN 400 AND 1600),
    ADD COLUMN units     integer NOT NULL DEFAULT 0 CHECK (units >= 0);

-- +goose Down
ALTER TABLE xp_events DROP COLUMN percent, DROP COLUMN questions, DROP COLUMN score, DROP COLUMN units;
