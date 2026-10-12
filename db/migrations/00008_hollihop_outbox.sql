-- +goose Up
-- Attendance waiting to be written to HolliHop (spec 8.6): one row per
-- student per finished lesson, and one more for each later correction that
-- changes present/absent. The worker sends pending rows in batches.
CREATE TABLE hollihop_outbox (
    id                bigserial PRIMARY KEY,
    session_id        uuid NOT NULL REFERENCES attendance_sessions (id) ON DELETE CASCADE,
    group_id          integer NOT NULL,
    student_client_id integer NOT NULL,
    date              date NOT NULL,
    pass              boolean NOT NULL,       -- true = absent
    status            text NOT NULL DEFAULT 'pending' CHECK (status IN (
                          'pending',          -- waiting to be sent
                          'sent',             -- HolliHop applied it
                          'unchanged',        -- HolliHop already had this state
                          'set_manually',     -- an admin set this day by hand; theirs stands
                          'dry_run',          -- recorded only: writing was switched off
                          'superseded',       -- a newer correction replaced it before sending
                          'failed')),         -- gave up after repeated errors
    attempts          integer NOT NULL DEFAULT 0,
    next_attempt_at   timestamptz NOT NULL DEFAULT now(),
    last_error        text,
    created_at        timestamptz NOT NULL DEFAULT now(),
    done_at           timestamptz
);
CREATE INDEX hollihop_outbox_due_idx ON hollihop_outbox (next_attempt_at) WHERE status = 'pending';
CREATE INDEX hollihop_outbox_mark_idx ON hollihop_outbox (session_id, student_client_id);

-- +goose Down
DROP TABLE hollihop_outbox;
