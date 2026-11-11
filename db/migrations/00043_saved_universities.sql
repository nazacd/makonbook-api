-- +goose Up
-- The universities a student saved to their list (spec 24.4).
CREATE TABLE student_saved_universities (
    student_client_id integer NOT NULL REFERENCES students ON DELETE CASCADE,
    university_id     text NOT NULL REFERENCES universities ON DELETE CASCADE ON UPDATE CASCADE,
    saved_at          timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (student_client_id, university_id)
);

-- +goose Down
DROP TABLE student_saved_universities;
