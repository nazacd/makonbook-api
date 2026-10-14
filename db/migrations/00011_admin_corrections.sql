-- +goose Up
-- Admins correct attendance marks from the panel (spec 13, D18) at any time,
-- in the branches they're given. Their changes are audited like teachers'.
-- An admin is a HolliHop employee or teacher, so actor_id alone is
-- ambiguous: actor_member_type says which, and actor_name keeps the name as
-- it was then.
ALTER TABLE attendance_audit DROP CONSTRAINT attendance_audit_actor_kind_check;
ALTER TABLE attendance_audit ADD CONSTRAINT attendance_audit_actor_kind_check
    CHECK (actor_kind IN ('student', 'teacher', 'system', 'admin'));
ALTER TABLE attendance_audit ADD COLUMN actor_member_type text;
ALTER TABLE attendance_audit ADD COLUMN actor_name text;
-- The corrections report reads teachers' changes over a date range.
CREATE INDEX attendance_audit_at_idx ON attendance_audit (at);

ALTER TABLE attendance_marks DROP CONSTRAINT attendance_marks_source_check;
ALTER TABLE attendance_marks ADD CONSTRAINT attendance_marks_source_check
    CHECK (source IN ('scan', 'teacher', 'auto', 'admin'));

-- +goose Down
UPDATE attendance_marks SET source = 'teacher' WHERE source = 'admin';
ALTER TABLE attendance_marks DROP CONSTRAINT attendance_marks_source_check;
ALTER TABLE attendance_marks ADD CONSTRAINT attendance_marks_source_check
    CHECK (source IN ('scan', 'teacher', 'auto'));

DROP INDEX attendance_audit_at_idx;
DELETE FROM attendance_audit WHERE actor_kind = 'admin';
ALTER TABLE attendance_audit DROP COLUMN actor_name;
ALTER TABLE attendance_audit DROP COLUMN actor_member_type;
ALTER TABLE attendance_audit DROP CONSTRAINT attendance_audit_actor_kind_check;
ALTER TABLE attendance_audit ADD CONSTRAINT attendance_audit_actor_kind_check
    CHECK (actor_kind IN ('student', 'teacher', 'system'));
