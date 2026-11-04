-- +goose Up
-- "Report an issue" on a question of a practice test (spec 9.6): a wrong key,
-- a typo, a broken image, or a technical problem, for the admins to look at
-- in the panel. The id is the device's, so sending it again is harmless.
CREATE TABLE practice_reports (
    id                uuid PRIMARY KEY,
    student_client_id integer NOT NULL REFERENCES students ON DELETE CASCADE,
    test              text NOT NULL,                  -- the test's code, e.g. DAY12
    version           integer NOT NULL CHECK (version > 0),
    subject           text NOT NULL CHECK (subject IN ('english', 'math')),
    module            smallint NOT NULL CHECK (module IN (1, 2)),
    position          smallint NOT NULL CHECK (position > 0),
    question_id       uuid NOT NULL,                  -- the question at that place in that version
    kind              text NOT NULL CHECK (kind IN ('question', 'technical')),
    reason            text CHECK (reason IN ('answer_key_wrong', 'typo', 'unclear', 'image_broken', 'formatting', 'other')),
    text              text NOT NULL DEFAULT '' CHECK (length(text) <= 2000),
    status            text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved')),
    created_at        timestamptz NOT NULL DEFAULT now(),
    resolved_at       timestamptz,
    resolved_by       text,                           -- the admin's name
    CHECK ((status = 'resolved') = (resolved_at IS NOT NULL)),
    CHECK ((resolved_at IS NULL) = (resolved_by IS NULL))
);
CREATE INDEX practice_reports_status ON practice_reports (status, created_at DESC);

-- +goose Down
DROP TABLE practice_reports;
