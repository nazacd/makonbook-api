-- +goose Up
-- Practice tests (spec 9). The questions come from SAT MAKON's own bank after
-- the AI review (the private makonbook-questionbank repository); teachers
-- approve them and editors fix them here. A question that several tests use
-- is stored once, so it is fixed and approved once.
CREATE TABLE questions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    subject      text NOT NULL CHECK (subject IN ('math', 'english')),
    type         text NOT NULL CHECK (type IN ('mcq', 'grid_in')),
    content_hash text NOT NULL UNIQUE,         -- subject, type and text: how repeats are found
    passage      text,
    stem         text NOT NULL,
    choices      jsonb,                        -- {"A": …, "D": …} for mcq
    figures      jsonb NOT NULL DEFAULT '[]',  -- [{key, kind, alt}]; key is an R2 object key
    correct      text,                         -- the letter, for mcq
    accepted     text[],                       -- every form a student may enter, for grid-in
    explanation  text NOT NULL,
    domain       text NOT NULL,
    skill        text NOT NULL,
    difficulty   text CHECK (difficulty IN ('easy', 'medium', 'hard')),  -- an estimate
    status       text NOT NULL DEFAULT 'review' CHECK (status IN ('draft', 'review', 'approved')),
    ai_risk      text CHECK (ai_risk IN ('green', 'yellow', 'red')),     -- worst of its AI reviews
    version      integer NOT NULL DEFAULT 1,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    approved_at  timestamptz,
    CHECK ((type = 'mcq') = (choices IS NOT NULL AND correct IS NOT NULL)),
    CHECK ((type = 'grid_in') = (accepted IS NOT NULL)),
    CHECK ((status = 'approved') = (approved_at IS NOT NULL))
);

-- Every version of a question, so a change can be traced and undone.
CREATE TABLE question_versions (
    question_id uuid NOT NULL REFERENCES questions ON DELETE CASCADE,
    version     integer NOT NULL,
    content     jsonb NOT NULL,                -- text, key, explanation and tags as they were
    source      text NOT NULL CHECK (source IN ('import', 'editor')),
    created_by  text,                          -- the editor; null for an import
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (question_id, version)
);

-- A test is open to students only once every question in it is approved.
CREATE TABLE practice_tests (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code       text NOT NULL UNIQUE,           -- the bank's test id without spaces, e.g. DAY12
    title      text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE practice_test_questions (
    test_id     uuid NOT NULL REFERENCES practice_tests ON DELETE CASCADE,
    subject     text NOT NULL CHECK (subject IN ('math', 'english')),
    module      smallint NOT NULL CHECK (module IN (1, 2)),
    position    smallint NOT NULL CHECK (position > 0),
    question_id uuid NOT NULL REFERENCES questions,
    PRIMARY KEY (test_id, subject, module, position)
);
CREATE INDEX practice_test_questions_question ON practice_test_questions (question_id);

-- The AI review of each question as it appeared in a test (one row per test
-- position, so a repeated question keeps every review). Read-only.
CREATE TABLE question_ai_reviews (
    id          bigserial PRIMARY KEY,
    question_id uuid NOT NULL REFERENCES questions ON DELETE CASCADE,
    test_id     uuid NOT NULL REFERENCES practice_tests ON DELETE CASCADE,
    qid         text NOT NULL,                 -- the review's id, e.g. math-m1-07
    csv_id      integer NOT NULL,              -- the row in SAT MAKON's bank
    risk        text NOT NULL CHECK (risk IN ('green', 'yellow', 'red')),
    verdict     text NOT NULL,
    record      jsonb NOT NULL,                -- the whole review record
    imported_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (test_id, qid)
);
CREATE INDEX question_ai_reviews_question ON question_ai_reviews (question_id);

-- +goose Down
DROP TABLE question_ai_reviews;
DROP TABLE practice_test_questions;
DROP TABLE practice_tests;
DROP TABLE question_versions;
DROP TABLE questions;
