-- +goose Up
-- Gamification (spec 27): XP the server awards once per thing done, the days
-- that count for the streak, the quests' progress and the achievements.

-- One row per thing done; a key is unique per student, so a retried upload
-- or a second submit awards nothing twice. The day's aggregates (word
-- reviews, game rounds, reading) are one row per day whose amount grows up
-- to a cap. happened_at is when the work was done (an attempt's finished_at,
-- a review's reviewed_at), not when the server heard of it.
CREATE TABLE xp_events (
    id                bigserial PRIMARY KEY,
    student_client_id integer NOT NULL REFERENCES students ON DELETE CASCADE ON UPDATE CASCADE,
    key               text NOT NULL,            -- attempt:<id>, assessment:<id>, vocab:<day>, games:<day>, read:<day>, attendance:<session>, quest:<code>:<period>
    kind              text NOT NULL,            -- the part of the key before the colon
    title             text NOT NULL,
    amount            integer NOT NULL CHECK (amount >= 0),
    right_answers     integer NOT NULL DEFAULT 0 CHECK (right_answers >= 0),
    happened_at       timestamptz NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (student_client_id, key)
);
CREATE INDEX xp_events_student_time_idx ON xp_events (student_client_id, happened_at DESC);

-- A day (in school time) with any XP: the streak.
CREATE TABLE student_days (
    student_client_id integer NOT NULL REFERENCES students ON DELETE CASCADE ON UPDATE CASCADE,
    day               date NOT NULL,
    PRIMARY KEY (student_client_id, day)
);

-- A quest's progress in one period: daily quests in school days, weekly ones
-- in Monday weeks (period_start is the day or the Monday). Made when first
-- touched or read; done_at set once, with the reward written to xp_events.
CREATE TABLE quest_progress (
    student_client_id integer NOT NULL REFERENCES students ON DELETE CASCADE ON UPDATE CASCADE,
    code              text NOT NULL,
    period_start      date NOT NULL,
    progress          integer NOT NULL DEFAULT 0 CHECK (progress >= 0),
    target            integer NOT NULL CHECK (target > 0),
    reward_xp         integer NOT NULL CHECK (reward_xp > 0),
    done_at           timestamptz,
    PRIMARY KEY (student_client_id, code, period_start)
);

CREATE TABLE student_achievements (
    student_client_id integer NOT NULL REFERENCES students ON DELETE CASCADE ON UPDATE CASCADE,
    code              text NOT NULL,
    unlocked_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (student_client_id, code)
);

-- +goose Down
DROP TABLE student_achievements;
DROP TABLE quest_progress;
DROP TABLE student_days;
DROP TABLE xp_events;
