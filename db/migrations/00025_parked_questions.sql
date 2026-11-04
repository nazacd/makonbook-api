-- +goose Up
-- "Ask later" (spec 9.6): a question a student parks during a practice test
-- to look at later, sorted into folders, with why they were stuck, a note and,
-- once they understand it, the fix in one line. Their teachers see them. The
-- id is the device's, so saving again (or from another device) is harmless.
CREATE TABLE parked_questions (
    id                uuid PRIMARY KEY,
    student_client_id integer NOT NULL REFERENCES students ON DELETE CASCADE,
    test              text NOT NULL,                  -- the test's code, e.g. DAY12
    version           integer NOT NULL CHECK (version > 0),
    subject           text NOT NULL CHECK (subject IN ('english', 'math')),
    module            smallint NOT NULL CHECK (module IN (1, 2)),
    position          smallint NOT NULL CHECK (position > 0),
    question_id       uuid NOT NULL,                  -- the question at that place in that version
    folder            text NOT NULL DEFAULT 'Unsorted' CHECK (length(folder) BETWEEN 1 AND 40),
    reason            text CHECK (reason IN ('didnt_understand', 'didnt_know_start', 'lost_partway', 'ran_out_of_time')),
    note              text NOT NULL DEFAULT '' CHECK (length(note) <= 1000),
    fix               text NOT NULL DEFAULT '' CHECK (length(fix) <= 300),
    status            text NOT NULL DEFAULT 'waiting' CHECK (status IN ('waiting', 'sorted')),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX parked_questions_student ON parked_questions (student_client_id, created_at DESC);

-- +goose Down
DROP TABLE parked_questions;
