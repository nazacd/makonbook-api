-- +goose Up
-- Roadmaps (spec 22): SAT MAKON's curriculum as a route of units and
-- lessons, each lesson with a level check and homework drawn from the
-- question bank and graded on the server. The curriculum is imported
-- (`makonbook curriculum import`), never committed: it is SAT MAKON's own.

-- Each import of a course (subject × level) is a new version; the latest is
-- live. Progress is keyed by lesson id, so a new version keeps it.
CREATE TABLE curriculum_versions (
    subject      text NOT NULL CHECK (subject IN ('math', 'english')),
    level        text NOT NULL CHECK (level IN ('foundation', 'pre-sat', 'advanced')),
    version      integer NOT NULL CHECK (version > 0),
    content      jsonb NOT NULL,
    content_hash text NOT NULL,
    imported_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (subject, level, version)
);

-- A registered student's answers to onboarding (graduation, SAT dates,
-- scores, focus, schedule).
CREATE TABLE onboarding (
    student_client_id integer PRIMARY KEY REFERENCES students ON DELETE CASCADE ON UPDATE CASCADE,
    answers           jsonb NOT NULL,
    updated_at        timestamptz NOT NULL DEFAULT now()
);

-- The roadmaps onboarding gave a registered student ("math-foundation"…).
-- Enrolled students' come from their groups, not from here.
CREATE TABLE roadmap_choices (
    student_client_id integer NOT NULL REFERENCES students ON DELETE CASCADE ON UPDATE CASCADE,
    roadmap_id        text NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (student_client_id, roadmap_id)
);

-- A level check or a homework set, with its questions (from the bank), the
-- answers saved so far, and once submitted the result.
CREATE TABLE roadmap_assessments (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    student_client_id integer NOT NULL REFERENCES students ON DELETE CASCADE ON UPDATE CASCADE,
    roadmap_id        text NOT NULL,
    lesson_id         text NOT NULL,
    kind              text NOT NULL CHECK (kind IN ('level_check', 'homework')),
    homework_index    smallint CHECK (homework_index >= 0),
    question_ids      uuid[] NOT NULL CHECK (cardinality(question_ids) > 0),
    answers           jsonb NOT NULL DEFAULT '{}',
    created_at        timestamptz NOT NULL DEFAULT now(),
    finished_at       timestamptz,
    correct           smallint,
    total             smallint,
    percent           smallint CHECK (percent BETWEEN 0 AND 100),
    CHECK ((kind = 'homework') = (homework_index IS NOT NULL)),
    CHECK ((finished_at IS NULL) = (percent IS NULL))
);
CREATE INDEX roadmap_assessments_student_idx ON roadmap_assessments (student_client_id, lesson_id);
-- At most one unfinished assessment per lesson, kind and set.
CREATE UNIQUE INDEX roadmap_assessments_open_idx ON roadmap_assessments (student_client_id, lesson_id, kind, coalesce(homework_index, -1))
    WHERE finished_at IS NULL;

-- Which published image file holds a question figure (practice): filled
-- when a figure is copied for an assessment outside any published test.
CREATE TABLE figure_files (
    key  text PRIMARY KEY,
    file text NOT NULL REFERENCES practice_published_images
);

-- +goose Down
DROP TABLE figure_files;
DROP TABLE roadmap_assessments;
DROP TABLE roadmap_choices;
DROP TABLE onboarding;
DROP TABLE curriculum_versions;
