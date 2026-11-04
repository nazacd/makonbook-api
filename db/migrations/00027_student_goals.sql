-- +goose Up
-- A student's SAT goals (spec 9.6), for the web app's dashboard: a target
-- score per section and the exam date, each optional. One row per student.
CREATE TABLE student_goals (
    student_client_id integer PRIMARY KEY REFERENCES students ON DELETE CASCADE,
    target_math       smallint CHECK (target_math BETWEEN 200 AND 800 AND target_math % 10 = 0),
    target_rw         smallint CHECK (target_rw BETWEEN 200 AND 800 AND target_rw % 10 = 0),
    exam_date         date,
    updated_at        timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE student_goals;
