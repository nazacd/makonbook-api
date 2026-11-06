-- +goose Up
-- Accounts (spec 21): people may sign up themselves. They are students like
-- any other, in the same table, but not (yet) in HolliHop:
--   - source 'self', under ids from their own sequence, -1000001 downwards,
--     clear of HolliHop's positive ClientIds and of the demo account's
--     -1 … -999999 (internal/demo);
--   - the sync never touches them; when the same verified phone shows up in
--     HolliHop as a new student, the sync re-keys the row to the HolliHop
--     ClientId (source 'hollihop' from then on), so everything they did
--     follows them. Every foreign key to students therefore cascades updates.
-- Name overrides: a name a person chose for themselves, which the sync
-- never writes. They see it instead of HolliHop's; staff keep HolliHop's.
CREATE SEQUENCE self_student_ids AS integer
    INCREMENT BY -1 MINVALUE -2147483648 MAXVALUE -1000001 START WITH -1000001;

ALTER TABLE students
    ADD COLUMN source text NOT NULL DEFAULT 'hollihop' CHECK (source IN ('hollihop', 'self')),
    ADD COLUMN first_name_custom text CHECK (char_length(first_name_custom) BETWEEN 1 AND 60),
    ADD COLUMN last_name_custom text CHECK (char_length(last_name_custom) BETWEEN 1 AND 60),
    ADD CONSTRAINT students_source_id_check CHECK ((source = 'self') = (client_id <= -1000001));

ALTER TABLE teachers
    ADD COLUMN first_name_custom text CHECK (char_length(first_name_custom) BETWEEN 1 AND 60),
    ADD COLUMN last_name_custom text CHECK (char_length(last_name_custom) BETWEEN 1 AND 60);

-- A HolliHop person whose phone is a self-registered account's: the two are
-- never merged automatically (only a new HolliHop student is), so admins see it.
ALTER TABLE data_quality_issues DROP CONSTRAINT data_quality_issues_kind_check;
ALTER TABLE data_quality_issues ADD CONSTRAINT data_quality_issues_kind_check
    CHECK (kind IN ('phone_missing', 'phone_invalid', 'phone_duplicate', 'self_account_conflict'));

-- Re-keying a student carries every row that points to them.
ALTER TABLE enrollments DROP CONSTRAINT enrollments_student_client_id_fkey,
    ADD CONSTRAINT enrollments_student_client_id_fkey FOREIGN KEY (student_client_id)
        REFERENCES students (client_id) ON DELETE CASCADE ON UPDATE CASCADE;
ALTER TABLE attendance_marks DROP CONSTRAINT attendance_marks_student_client_id_fkey,
    ADD CONSTRAINT attendance_marks_student_client_id_fkey FOREIGN KEY (student_client_id)
        REFERENCES students (client_id) ON UPDATE CASCADE;
ALTER TABLE attendance_records DROP CONSTRAINT attendance_records_student_client_id_fkey,
    ADD CONSTRAINT attendance_records_student_client_id_fkey FOREIGN KEY (student_client_id)
        REFERENCES students (client_id) ON DELETE CASCADE ON UPDATE CASCADE;
ALTER TABLE practice_attempts DROP CONSTRAINT practice_attempts_student_client_id_fkey,
    ADD CONSTRAINT practice_attempts_student_client_id_fkey FOREIGN KEY (student_client_id)
        REFERENCES students (client_id) ON UPDATE CASCADE;
ALTER TABLE vocab_reviews DROP CONSTRAINT vocab_reviews_student_client_id_fkey,
    ADD CONSTRAINT vocab_reviews_student_client_id_fkey FOREIGN KEY (student_client_id)
        REFERENCES students (client_id) ON UPDATE CASCADE;
ALTER TABLE parent_codes DROP CONSTRAINT parent_codes_student_client_id_fkey,
    ADD CONSTRAINT parent_codes_student_client_id_fkey FOREIGN KEY (student_client_id)
        REFERENCES students (client_id) ON DELETE CASCADE ON UPDATE CASCADE;
ALTER TABLE parent_sessions DROP CONSTRAINT parent_sessions_student_client_id_fkey,
    ADD CONSTRAINT parent_sessions_student_client_id_fkey FOREIGN KEY (student_client_id)
        REFERENCES students (client_id) ON DELETE CASCADE ON UPDATE CASCADE;
ALTER TABLE parent_reports DROP CONSTRAINT parent_reports_student_client_id_fkey,
    ADD CONSTRAINT parent_reports_student_client_id_fkey FOREIGN KEY (student_client_id)
        REFERENCES students (client_id) ON DELETE CASCADE ON UPDATE CASCADE;
ALTER TABLE parked_questions DROP CONSTRAINT parked_questions_student_client_id_fkey,
    ADD CONSTRAINT parked_questions_student_client_id_fkey FOREIGN KEY (student_client_id)
        REFERENCES students (client_id) ON DELETE CASCADE ON UPDATE CASCADE;
ALTER TABLE practice_reports DROP CONSTRAINT practice_reports_student_client_id_fkey,
    ADD CONSTRAINT practice_reports_student_client_id_fkey FOREIGN KEY (student_client_id)
        REFERENCES students (client_id) ON DELETE CASCADE ON UPDATE CASCADE;
ALTER TABLE student_goals DROP CONSTRAINT student_goals_student_client_id_fkey,
    ADD CONSTRAINT student_goals_student_client_id_fkey FOREIGN KEY (student_client_id)
        REFERENCES students (client_id) ON DELETE CASCADE ON UPDATE CASCADE;

-- +goose Down
ALTER TABLE student_goals DROP CONSTRAINT student_goals_student_client_id_fkey,
    ADD CONSTRAINT student_goals_student_client_id_fkey FOREIGN KEY (student_client_id)
        REFERENCES students (client_id) ON DELETE CASCADE;
ALTER TABLE practice_reports DROP CONSTRAINT practice_reports_student_client_id_fkey,
    ADD CONSTRAINT practice_reports_student_client_id_fkey FOREIGN KEY (student_client_id)
        REFERENCES students (client_id) ON DELETE CASCADE;
ALTER TABLE parked_questions DROP CONSTRAINT parked_questions_student_client_id_fkey,
    ADD CONSTRAINT parked_questions_student_client_id_fkey FOREIGN KEY (student_client_id)
        REFERENCES students (client_id) ON DELETE CASCADE;
ALTER TABLE parent_reports DROP CONSTRAINT parent_reports_student_client_id_fkey,
    ADD CONSTRAINT parent_reports_student_client_id_fkey FOREIGN KEY (student_client_id)
        REFERENCES students (client_id) ON DELETE CASCADE;
ALTER TABLE parent_sessions DROP CONSTRAINT parent_sessions_student_client_id_fkey,
    ADD CONSTRAINT parent_sessions_student_client_id_fkey FOREIGN KEY (student_client_id)
        REFERENCES students (client_id) ON DELETE CASCADE;
ALTER TABLE parent_codes DROP CONSTRAINT parent_codes_student_client_id_fkey,
    ADD CONSTRAINT parent_codes_student_client_id_fkey FOREIGN KEY (student_client_id)
        REFERENCES students (client_id) ON DELETE CASCADE;
ALTER TABLE vocab_reviews DROP CONSTRAINT vocab_reviews_student_client_id_fkey,
    ADD CONSTRAINT vocab_reviews_student_client_id_fkey FOREIGN KEY (student_client_id)
        REFERENCES students (client_id);
ALTER TABLE practice_attempts DROP CONSTRAINT practice_attempts_student_client_id_fkey,
    ADD CONSTRAINT practice_attempts_student_client_id_fkey FOREIGN KEY (student_client_id)
        REFERENCES students (client_id);
ALTER TABLE attendance_records DROP CONSTRAINT attendance_records_student_client_id_fkey,
    ADD CONSTRAINT attendance_records_student_client_id_fkey FOREIGN KEY (student_client_id)
        REFERENCES students (client_id) ON DELETE CASCADE;
ALTER TABLE attendance_marks DROP CONSTRAINT attendance_marks_student_client_id_fkey,
    ADD CONSTRAINT attendance_marks_student_client_id_fkey FOREIGN KEY (student_client_id)
        REFERENCES students (client_id);
ALTER TABLE enrollments DROP CONSTRAINT enrollments_student_client_id_fkey,
    ADD CONSTRAINT enrollments_student_client_id_fkey FOREIGN KEY (student_client_id)
        REFERENCES students (client_id) ON DELETE CASCADE;

DELETE FROM data_quality_issues WHERE kind = 'self_account_conflict';
ALTER TABLE data_quality_issues DROP CONSTRAINT data_quality_issues_kind_check;
ALTER TABLE data_quality_issues ADD CONSTRAINT data_quality_issues_kind_check
    CHECK (kind IN ('phone_missing', 'phone_invalid', 'phone_duplicate'));

ALTER TABLE teachers DROP COLUMN last_name_custom, DROP COLUMN first_name_custom;
-- Self-registered students can't be kept without their column.
DELETE FROM practice_attempts WHERE student_client_id <= -1000001;
DELETE FROM vocab_reviews WHERE student_client_id <= -1000001;
DELETE FROM auth_sessions WHERE person_kind = 'student' AND person_id <= -1000001;
DELETE FROM credentials WHERE person_kind = 'student' AND person_id <= -1000001;
DELETE FROM students WHERE source = 'self';
ALTER TABLE students DROP COLUMN last_name_custom, DROP COLUMN first_name_custom, DROP COLUMN source;
DROP SEQUENCE self_student_ids;
