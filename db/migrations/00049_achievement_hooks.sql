-- +goose Up
-- Gamification (spec 27): the longest run of right answers inside one test
-- or assessment (Sniper), and a competition's board settled once it has
-- ended (the top three get their podium XP once).
ALTER TABLE xp_events ADD COLUMN run smallint CHECK (run >= 0);
ALTER TABLE competitions ADD COLUMN settled_at timestamptz;

-- +goose Down
ALTER TABLE competitions DROP COLUMN settled_at;
ALTER TABLE xp_events DROP COLUMN run;
