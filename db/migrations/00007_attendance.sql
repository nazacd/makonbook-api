-- +goose Up
-- QR attendance (spec 8). A session is one lesson's attendance: opened by a
-- teacher of that lesson, with the roster frozen at that moment.
CREATE TABLE attendance_sessions (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id    integer NOT NULL REFERENCES groups (id),
    date        date NOT NULL,
    teacher_id  integer NOT NULL REFERENCES teachers (id),   -- who opened it
    secret      bytea NOT NULL,                               -- QR signing key (8.2)
    starts_at   timestamptz NOT NULL,                         -- the lesson's start and end
    ends_at     timestamptz NOT NULL,
    opened_at   timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    finished_by text CHECK (finished_by IN ('teacher', 'auto')),
    UNIQUE (group_id, date)
);
CREATE INDEX attendance_sessions_open_idx ON attendance_sessions (ends_at) WHERE finished_at IS NULL;

-- One row per roster student. 'pending' until they scan or the teacher marks
-- them; pending rows become 'absent' when the session finishes.
CREATE TABLE attendance_marks (
    session_id        uuid NOT NULL REFERENCES attendance_sessions (id) ON DELETE CASCADE,
    student_client_id integer NOT NULL REFERENCES students (client_id),
    state             text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'present', 'late', 'absent')),
    source            text CHECK (source IN ('scan', 'teacher', 'auto')),
    scanned_at        timestamptz,
    device_id         text,           -- the device that scanned
    updated_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (session_id, student_client_id)
);
CREATE INDEX attendance_marks_student_idx ON attendance_marks (student_client_id);
-- One device checks in one student per session (8.3).
CREATE UNIQUE INDEX attendance_marks_device_idx ON attendance_marks (session_id, device_id) WHERE device_id IS NOT NULL;

-- Every change of a mark, for disputes and the admins' "overrides per
-- teacher" report (8.4, 8.5).
CREATE TABLE attendance_audit (
    id                bigserial PRIMARY KEY,
    session_id        uuid NOT NULL REFERENCES attendance_sessions (id) ON DELETE CASCADE,
    student_client_id integer NOT NULL,
    actor_kind        text NOT NULL CHECK (actor_kind IN ('student', 'teacher', 'system')),
    actor_id          integer,        -- student client id or teacher id; NULL for system
    from_state        text NOT NULL,
    to_state          text NOT NULL,
    at                timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX attendance_audit_session_idx ON attendance_audit (session_id);

-- +goose Down
DROP TABLE attendance_audit;
DROP TABLE attendance_marks;
DROP TABLE attendance_sessions;
