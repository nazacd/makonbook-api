-- +goose Up
-- Every answer a student gave to a bank question in an assessment (level
-- checks, homework, practice sets), graded when given (spec 22.8): what the
-- question bank's solved counts and analytics read. Written on submit, and
-- on each check in a practice set taken at one's own pace. A discarded set
-- keeps its answers (assessment_id becomes null), so nothing is un-solved.
-- `makonbook practice backfill-answers` fills it from assessments finished
-- before this table existed.
CREATE TABLE question_answers (
    id                bigserial PRIMARY KEY,
    student_client_id integer NOT NULL REFERENCES students ON DELETE CASCADE ON UPDATE CASCADE,
    question_id       uuid NOT NULL,
    assessment_id     uuid REFERENCES roadmap_assessments ON DELETE SET NULL,
    answer            text NOT NULL,
    correct           boolean NOT NULL,
    seconds           integer CHECK (seconds >= 0),
    answered_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (assessment_id, question_id)
);
CREATE INDEX question_answers_student ON question_answers (student_client_id, answered_at);
CREATE INDEX question_answers_question ON question_answers (question_id);

-- Questions a student saved from the question bank to come back to.
CREATE TABLE saved_questions (
    student_client_id integer NOT NULL REFERENCES students ON DELETE CASCADE ON UPDATE CASCADE,
    question_id       uuid NOT NULL,
    saved_at          timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (student_client_id, question_id)
);

-- +goose Down
DROP TABLE saved_questions;
DROP TABLE question_answers;
