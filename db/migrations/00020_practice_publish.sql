-- +goose Up
-- Publishing (spec 9.5): a test whose questions are all approved is built
-- into an immutable package, one version after another. Devices download a
-- version and take it offline; attempts stay tied to the version they used.
-- Editing a question later makes a new version once the test is approved
-- again; until then students keep the last one.
CREATE TABLE practice_test_versions (
    test_id      uuid NOT NULL REFERENCES practice_tests ON DELETE CASCADE,
    version      integer NOT NULL CHECK (version > 0),
    content_hash text NOT NULL,                -- the package without its version: what changed
    package      jsonb NOT NULL,               -- questions, keys, explanations, score scales, image files
    bytes        integer NOT NULL,             -- the package and its images: the download's size
    published_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (test_id, version)
);

-- Images packages use, copied into MakonBook's bucket under p/img/<file>,
-- named by their bytes, so devices fetch them straight from R2.
CREATE TABLE practice_published_images (
    file         text PRIMARY KEY,             -- <sha-256>.<ext>
    content_type text NOT NULL,
    bytes        integer NOT NULL,
    width        integer NOT NULL,
    height       integer NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- A student's attempt, uploaded from the device when it is online. The id is
-- the device's, so an upload repeated after a lost answer is stored once.
CREATE TABLE practice_attempts (
    id                uuid PRIMARY KEY,
    student_client_id integer NOT NULL REFERENCES students,
    test_id           uuid NOT NULL,
    test_version      integer NOT NULL,
    sections          text[] NOT NULL,         -- english and/or math, in the order taken
    started_at        timestamptz NOT NULL,
    finished_at       timestamptz NOT NULL,
    answers           jsonb NOT NULL,          -- [{subject, module, position, answer, seconds}]
    scores            jsonb NOT NULL,          -- graded here from the version's keys:
                                               -- {subject: {raw, questions, score}} per section taken
    received_at       timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (test_id, test_version) REFERENCES practice_test_versions (test_id, version) ON DELETE CASCADE,
    CHECK (finished_at >= started_at)
);
CREATE INDEX practice_attempts_student ON practice_attempts (student_client_id, finished_at DESC);

-- +goose Down
DROP TABLE practice_attempts;
DROP TABLE practice_published_images;
DROP TABLE practice_test_versions;
