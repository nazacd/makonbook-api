-- +goose Up
-- Mirror of the HolliHop data MakonBook needs (spec 4, 5, 15). Only the sync
-- writes the HolliHop-sourced columns; app-owned columns (credentials) are
-- added by later migrations and never touched by the sync.

CREATE TABLE offices (
    id         integer PRIMARY KEY,           -- HolliHop OfficeOrCompanyId
    name       text NOT NULL,
    utc_offset text NOT NULL DEFAULT '',      -- e.g. '+05:00'
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE groups (
    id            integer PRIMARY KEY,        -- HolliHop EdUnit Id
    name          text NOT NULL,
    discipline    text NOT NULL,
    level         text NOT NULL,
    learning_type text NOT NULL,              -- GROUP, IV OFFLINE, IV ONLINE, ONLINE
    office_id     integer REFERENCES offices (id),
    removed_at    timestamptz,                -- no longer returned by HolliHop
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- Recurring lesson slots. Teachers are attached here, not to the group.
CREATE TABLE schedule_items (
    id          integer PRIMARY KEY,          -- HolliHop ScheduleItem Id
    group_id    integer NOT NULL REFERENCES groups (id) ON DELETE CASCADE,
    begin_date  date,                         -- NULL = open-ended
    end_date    date,
    begin_time  time NOT NULL,
    end_time    time NOT NULL,
    weekdays    smallint NOT NULL,            -- bit set: Mon=1 … Sun=64
    classroom   text NOT NULL,
    teacher_ids integer[] NOT NULL
);
CREATE INDEX schedule_items_group_id_idx ON schedule_items (group_id);
CREATE INDEX schedule_items_teacher_ids_idx ON schedule_items USING gin (teacher_ids);

CREATE TABLE teachers (
    id             integer PRIMARY KEY,       -- HolliHop teacher Id
    first_name     text NOT NULL,
    last_name      text NOT NULL,
    phone_raw      text NOT NULL,             -- exactly as typed in HolliHop
    -- E.164, set only while the teacher is active and the number is valid and
    -- not shared with anyone else active. NULL means "cannot log in by phone".
    login_phone    text UNIQUE,
    fired          boolean NOT NULL,
    deactivated_at timestamptz,               -- NULL = active
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE students (
    client_id      integer PRIMARY KEY,       -- HolliHop ClientId (what enrollments use)
    hh_student_id  integer NOT NULL,          -- HolliHop student Id
    first_name     text NOT NULL,
    last_name      text NOT NULL,
    phone_raw      text NOT NULL,
    login_phone    text UNIQUE,               -- see teachers.login_phone
    hh_status_id   integer,                   -- 2 = studying; NULL = gone from HolliHop
    office_ids     integer[] NOT NULL,
    deactivated_at timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- Current and future enrollments only; rebuilt on every sync.
CREATE TABLE enrollments (
    id                bigserial PRIMARY KEY,
    group_id          integer NOT NULL REFERENCES groups (id) ON DELETE CASCADE,
    student_client_id integer NOT NULL REFERENCES students (client_id) ON DELETE CASCADE,
    begin_date        date,                   -- NULL = from the group's start
    end_date          date,                   -- NULL = open-ended
    weekdays          smallint NOT NULL       -- days this student attends
);
CREATE INDEX enrollments_group_id_idx ON enrollments (group_id);
CREATE INDEX enrollments_student_client_id_idx ON enrollments (student_client_id);

-- Problems that stop an active person from logging in, for admins to fix in
-- HolliHop. One open row per (person, kind); resolved rows are kept as history.
CREATE TABLE data_quality_issues (
    id            bigserial PRIMARY KEY,
    person_kind   text NOT NULL CHECK (person_kind IN ('student', 'teacher')),
    person_id     integer NOT NULL,           -- students.client_id or teachers.id
    kind          text NOT NULL CHECK (kind IN ('phone_missing', 'phone_invalid', 'phone_duplicate')),
    detail        text NOT NULL,
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz NOT NULL DEFAULT now(),
    resolved_at   timestamptz
);
CREATE UNIQUE INDEX data_quality_issues_open_idx
    ON data_quality_issues (person_kind, person_id, kind)
    WHERE resolved_at IS NULL;

-- +goose Down
DROP TABLE data_quality_issues;
DROP TABLE enrollments;
DROP TABLE students;
DROP TABLE teachers;
DROP TABLE schedule_items;
DROP TABLE groups;
DROP TABLE offices;
