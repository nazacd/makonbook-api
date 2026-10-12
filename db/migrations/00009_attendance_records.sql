-- +goose Up
-- HolliHop's own attendance marks per student per lesson day, mirrored
-- read-only for the student's attendance table (spec 8.7, D16): includes
-- lessons from before the app and admins' corrections. pass = absent.
CREATE TABLE attendance_records (
    group_id          integer NOT NULL,
    student_client_id integer NOT NULL REFERENCES students (client_id) ON DELETE CASCADE,
    date              date NOT NULL,
    pass              boolean NOT NULL,
    PRIMARY KEY (student_client_id, group_id, date)
);
CREATE INDEX attendance_records_date_idx ON attendance_records (date);

-- +goose Down
DROP TABLE attendance_records;
