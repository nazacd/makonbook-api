-- The store reviewers' demo account (internal/demo). It lives in the HolliHop
-- mirror tables under negative ids, which the sync leaves alone, so the app
-- shows it like any student. Nothing else writes these rows.

-- name: UpsertOffice :exec
INSERT INTO offices (id, name, utc_offset) VALUES (@id, @name, '+05:00')
ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, updated_at = now();

-- name: UpsertTeacher :exec
INSERT INTO teachers (id, first_name, last_name, phone_raw, fired)
VALUES (@id, @first_name, @last_name, '', false)
ON CONFLICT (id) DO UPDATE
SET first_name = EXCLUDED.first_name, last_name = EXCLUDED.last_name, fired = false,
    deactivated_at = NULL, login_phone = NULL, updated_at = now();

-- name: UpsertGroup :exec
INSERT INTO groups (id, name, discipline, level, learning_type, office_id)
VALUES (@id, @name, @discipline, @level, 'GROUP', @office_id)
ON CONFLICT (id) DO UPDATE
SET name = EXCLUDED.name, discipline = EXCLUDED.discipline, level = EXCLUDED.level,
    learning_type = 'GROUP', office_id = EXCLUDED.office_id, removed_at = NULL, updated_at = now();

-- name: DeleteScheduleItems :exec
DELETE FROM schedule_items WHERE group_id < 0;

-- name: InsertScheduleItem :exec
INSERT INTO schedule_items (id, group_id, begin_date, end_date, begin_time, end_time, weekdays, classroom, teacher_ids)
VALUES (@id, @group_id, @begin_date, NULL, @begin_time, @end_time, @weekdays, @classroom, @teacher_ids::int[]);

-- Active since activated_at (kept when the student already exists: a
-- password set later stays valid).
-- name: UpsertStudent :exec
INSERT INTO students (client_id, hh_student_id, first_name, last_name, phone_raw, hh_status_id, office_ids, activated_at)
VALUES (@client_id, @client_id, @first_name, @last_name, '', 2, @office_ids::int[], @activated_at)
ON CONFLICT (client_id) DO UPDATE
SET first_name = EXCLUDED.first_name, last_name = EXCLUDED.last_name, hh_status_id = 2,
    office_ids = EXCLUDED.office_ids, deactivated_at = NULL, login_phone = NULL, updated_at = now();

-- name: DeleteEnrollments :exec
DELETE FROM enrollments WHERE student_client_id < 0;

-- name: InsertEnrollment :exec
INSERT INTO enrollments (group_id, student_client_id, begin_date, end_date, weekdays)
VALUES (@group_id, @student_client_id, @begin_date, NULL, 0);

-- name: DeleteRecords :exec
DELETE FROM attendance_records WHERE student_client_id = @student_client_id;

-- name: InsertRecords :exec
INSERT INTO attendance_records (group_id, student_client_id, date, pass)
SELECT unnest(@group_ids::int[]), @student_client_id::int, unnest(@dates::date[]), unnest(@passes::bool[]);

-- name: UsernameOwner :one
SELECT person_kind, person_id FROM credentials WHERE username = @username;

-- Lesson days, so the demo teacher has lessons on their Today screen (one
-- schedule item per group, with the group's id).
-- name: DeleteLessonDays :exec
DELETE FROM lesson_days WHERE group_id < 0;

-- name: InsertLessonDays :exec
INSERT INTO lesson_days (group_id, date, minutes, schedule_item_ids)
SELECT x.g, x.d, x.m, ARRAY[x.g]
FROM (SELECT unnest(@group_ids::int[]) AS g, unnest(@dates::date[]) AS d, unnest(@minutes::int[]) AS m) AS x;

-- Attendance taken in the demo groups before today (today's stays: a
-- reviewer may be in the middle of it).
-- name: DeleteOldSessions :exec
DELETE FROM attendance_sessions WHERE group_id < 0 AND date < @today::date;

-- name: SetCredential :exec
INSERT INTO credentials (person_kind, person_id, username, password_hash, password_changed_at)
VALUES (@person_kind, @person_id, @username, @password_hash, now())
ON CONFLICT (person_kind, person_id) DO UPDATE
SET username = EXCLUDED.username, password_hash = EXCLUDED.password_hash,
    password_changed_at = now(), failed_attempts = 0, locked_until = NULL;

-- The results Setup makes (fixed ids), made again each time.
-- name: DeleteSeededAttempts :exec
DELETE FROM practice_attempts WHERE student_client_id < 0 AND id = ANY (@ids::uuid[]);

-- Removing the account: everything under the negative ids. Parent codes,
-- sessions and reports, attendance records and enrollments go with the
-- student (ON DELETE CASCADE); schedule items with the groups.

-- name: DeleteCredentials :exec
DELETE FROM credentials WHERE person_id < 0;

-- name: DeleteSessions :exec
DELETE FROM auth_sessions WHERE person_id < 0;

-- name: DeleteAttendance :exec
DELETE FROM attendance_sessions WHERE group_id < 0;

-- name: DeleteAttempts :exec
DELETE FROM practice_attempts WHERE student_client_id < 0;

-- name: DeleteVocabReviews :exec
DELETE FROM vocab_reviews WHERE student_client_id < 0;

-- name: DeleteStudents :exec
DELETE FROM students WHERE client_id < 0;

-- name: DeleteGroups :exec
DELETE FROM groups WHERE id < 0;

-- name: DeleteTeachers :exec
DELETE FROM teachers WHERE id < 0;

-- name: DeleteOffices :exec
DELETE FROM offices WHERE id < 0;
