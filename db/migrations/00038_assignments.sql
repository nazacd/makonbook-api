-- +goose Up
-- Assignments (spec 23): teachers give their groups homework, level checks
-- and exams with deadlines; students take them; a new one notifies the
-- group's students (D39). Status (scheduled, open, overdue, closed) is worked
-- out from the dates, not stored.

CREATE TABLE assignments (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Groups and teachers are never deleted, except the demo's: theirs go too.
    group_id       integer NOT NULL REFERENCES groups ON DELETE CASCADE,
    teacher_id     integer NOT NULL REFERENCES teachers ON DELETE CASCADE,   -- who gave it
    kind           text NOT NULL CHECK (kind IN ('homework', 'level_check', 'exam')),
    title          text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 120),
    instructions   text NOT NULL DEFAULT '' CHECK (char_length(instructions) <= 2000),
    subject        text NOT NULL CHECK (subject IN ('math', 'english')),
    course         text,                     -- the curriculum course ("math-pre-sat"), with lesson_id
    lesson_id      text,
    homework_index smallint CHECK (homework_index >= 0),
    test_code      text,                     -- exams: the mock test
    sections       text[] NOT NULL DEFAULT '{}',   -- exams: english and/or math
    question_ids   uuid[] NOT NULL DEFAULT '{}',   -- homework and level checks: the set, in order
    skills         text[] NOT NULL DEFAULT '{}',
    mix            jsonb,                    -- {easy, medium, hard} asked for, when drawn
    opens_at       timestamptz NOT NULL,
    due_at         timestamptz NOT NULL,
    closed_at      timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    CHECK (due_at > opens_at),
    CHECK ((course IS NULL) = (lesson_id IS NULL)),
    CHECK (kind <> 'level_check' OR (lesson_id IS NOT NULL AND homework_index IS NULL)),
    CHECK (homework_index IS NULL OR (kind = 'homework' AND lesson_id IS NOT NULL)),
    CHECK ((kind = 'exam') = (test_code IS NOT NULL)),
    CHECK (kind <> 'exam' OR cardinality(sections) > 0),
    CHECK (kind = 'exam' OR cardinality(question_ids) BETWEEN 1 AND 60)
);
CREATE INDEX assignments_group_idx ON assignments (group_id, created_at DESC);

-- Assigned homework and level checks are taken as roadmap assessments. One
-- with a lesson counts for the roadmap like any other; custom homework has
-- no course or lesson. times: question id → seconds spent (the web counts).
ALTER TABLE roadmap_assessments
    ADD COLUMN assignment_id uuid REFERENCES assignments ON DELETE CASCADE,
    ADD COLUMN times jsonb NOT NULL DEFAULT '{}',
    ALTER COLUMN roadmap_id DROP NOT NULL,
    ALTER COLUMN lesson_id DROP NOT NULL,
    ADD CONSTRAINT roadmap_assessments_lesson_check CHECK (lesson_id IS NOT NULL OR assignment_id IS NOT NULL),
    -- A lesson's homework names its set; custom homework (no lesson) has none.
    DROP CONSTRAINT roadmap_assessments_check,
    ADD CONSTRAINT roadmap_assessments_set_check CHECK (
        CASE WHEN kind = 'homework' THEN (homework_index IS NOT NULL) = (lesson_id IS NOT NULL)
             ELSE homework_index IS NULL END);
CREATE INDEX roadmap_assessments_assignment_idx ON roadmap_assessments (assignment_id) WHERE assignment_id IS NOT NULL;
-- At most one unfinished assessment per lesson, kind, set and assignment.
DROP INDEX roadmap_assessments_open_idx;
CREATE UNIQUE INDEX roadmap_assessments_open_idx ON roadmap_assessments (
    student_client_id, coalesce(lesson_id, ''), kind, coalesce(homework_index, -1),
    coalesce(assignment_id, '00000000-0000-0000-0000-000000000000'::uuid)) WHERE finished_at IS NULL;

-- In-app notifications (spec 23.5), one row per person and event. No foreign
-- key to people (students or teachers); a notification about an assignment
-- goes with it.
CREATE TABLE notifications (
    id            bigserial PRIMARY KEY,
    person_kind   text NOT NULL CHECK (person_kind IN ('student', 'teacher')),
    person_id     integer NOT NULL,
    kind          text NOT NULL CHECK (kind IN ('assignment')),
    title         text NOT NULL,
    body          text NOT NULL,
    link          text,                      -- a path in the web app ("/homework")
    assignment_id uuid REFERENCES assignments ON DELETE CASCADE,
    created_at    timestamptz NOT NULL DEFAULT now(),
    read_at       timestamptz
);
CREATE INDEX notifications_person_idx ON notifications (person_kind, person_id, created_at DESC);
CREATE INDEX notifications_assignment_idx ON notifications (assignment_id) WHERE assignment_id IS NOT NULL;

-- +goose Down
DROP TABLE notifications;
DELETE FROM roadmap_assessments WHERE lesson_id IS NULL;
DROP INDEX roadmap_assessments_open_idx;
-- Back to one open assessment per lesson, kind and set: drop the extra
-- unfinished ones (assigned ones first).
DELETE FROM roadmap_assessments a
WHERE a.finished_at IS NULL AND EXISTS (
    SELECT 1 FROM roadmap_assessments b
    WHERE b.finished_at IS NULL AND b.student_client_id = a.student_client_id AND b.lesson_id = a.lesson_id
      AND b.kind = a.kind AND coalesce(b.homework_index, -1) = coalesce(a.homework_index, -1)
      AND (b.assignment_id IS NULL, b.created_at, b.id) > (a.assignment_id IS NULL, a.created_at, a.id));
CREATE UNIQUE INDEX roadmap_assessments_open_idx ON roadmap_assessments (student_client_id, lesson_id, kind, coalesce(homework_index, -1))
    WHERE finished_at IS NULL;
DROP INDEX roadmap_assessments_assignment_idx;
UPDATE roadmap_assessments SET roadmap_id = '' WHERE roadmap_id IS NULL;
ALTER TABLE roadmap_assessments
    DROP CONSTRAINT roadmap_assessments_set_check,
    ADD CONSTRAINT roadmap_assessments_check CHECK ((kind = 'homework') = (homework_index IS NOT NULL)),
    DROP CONSTRAINT roadmap_assessments_lesson_check,
    ALTER COLUMN lesson_id SET NOT NULL,
    ALTER COLUMN roadmap_id SET NOT NULL,
    DROP COLUMN times,
    DROP COLUMN assignment_id;
DROP TABLE assignments;
