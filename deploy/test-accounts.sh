#!/usr/bin/env bash
# Borrow a real teacher and one of their students to test the app, then put
# both accounts back as they were.
#
#   /opt/makonbook-api/test-accounts.sh
#
# It picks a teacher who teaches a group now, and a student enrolled in that
# group, both active with a login phone and never signed in (no password,
# username or session of their own), so they sign in with the default password
# (the last 4 digits of the phone) and the teacher's screens show the student.
# It prints their numbers and waits; type `done` when you have finished
# testing (Ctrl+C works too). Then it deletes what the test created on the two
# accounts: their password, username and sessions (back to the default
# password, signed out everywhere), and the student's practice attempts sent
# during the test.
#
# Attendance is not undone: a scan or a teacher's mark is a real mark and is
# written to HolliHop when writing is enabled. The script tells you if any was
# made, so you can correct it in the admin panel.
#
# Settings: PSQL (how to reach the database; default the shared `db`
# container), TEST_TEACHER_ID / TEST_STUDENT_ID (use these people instead of choosing;
# they must still never have signed in).
set -euo pipefail

PSQL=${PSQL:-docker exec -i db psql -U postgres -d makonbook_api}
sql() { $PSQL -v ON_ERROR_STOP=1 -qAt -F '|' "$@"; }

is_int() { [[ $1 =~ ^[0-9]+$ ]]; }
for v in "${TEST_TEACHER_ID:-}" "${TEST_STUDENT_ID:-}"; do
  if [[ -n $v ]] && ! is_int "$v"; then echo "TEST_TEACHER_ID and TEST_STUDENT_ID must be numbers" >&2; exit 2; fi
done

pick=$(sql <<SQL
WITH today AS (SELECT (now() AT TIME ZONE 'Asia/Tashkent')::date AS d)
SELECT t.id, t.first_name || ' ' || t.last_name, t.login_phone,
       s.client_id, s.first_name || ' ' || s.last_name, s.login_phone, g.name
FROM today, groups g
JOIN schedule_items si ON si.group_id = g.id
JOIN teachers t ON t.id = ANY (si.teacher_ids)
JOIN enrollments e ON e.group_id = g.id
JOIN students s ON s.client_id = e.student_client_id
WHERE g.removed_at IS NULL AND g.learning_type = 'GROUP'
  AND (si.end_date IS NULL OR si.end_date >= today.d)
  AND (e.begin_date IS NULL OR e.begin_date <= today.d)
  AND (e.end_date IS NULL OR e.end_date >= today.d)
  AND t.deactivated_at IS NULL AND t.login_phone IS NOT NULL
  AND s.deactivated_at IS NULL AND s.login_phone IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM credentials c WHERE c.person_kind = 'teacher' AND c.person_id = t.id)
  AND NOT EXISTS (SELECT 1 FROM auth_sessions a WHERE a.person_kind = 'teacher' AND a.person_id = t.id)
  AND NOT EXISTS (SELECT 1 FROM credentials c WHERE c.person_kind = 'student' AND c.person_id = s.client_id)
  AND NOT EXISTS (SELECT 1 FROM auth_sessions a WHERE a.person_kind = 'student' AND a.person_id = s.client_id)
  AND (${TEST_TEACHER_ID:-0} = 0 OR t.id = ${TEST_TEACHER_ID:-0})
  AND (${TEST_STUDENT_ID:-0} = 0 OR s.client_id = ${TEST_STUDENT_ID:-0})
ORDER BY random()
LIMIT 1;
SQL
)
if [[ -z $pick ]]; then
  echo "No teacher and student found who teach/learn in a group now and have never signed in." >&2
  exit 1
fi
IFS='|' read -r tid tname tphone sid sname sphone group <<<"$pick"
is_int "$tid" && is_int "$sid" || { echo "unexpected answer from the database" >&2; exit 1; }
started=$(sql -c "SELECT now()")

refresh() {
  trap - EXIT
  echo
  echo "Putting the accounts back..."
  sql <<SQL
BEGIN;
DELETE FROM credentials WHERE (person_kind, person_id) IN (('teacher', $tid), ('student', $sid));
DELETE FROM auth_sessions WHERE (person_kind, person_id) IN (('teacher', $tid), ('student', $sid));
DELETE FROM practice_attempts WHERE student_client_id = $sid AND received_at >= '$started';
DELETE FROM parent_sessions WHERE student_client_id = $sid AND created_at >= '$started';
DELETE FROM parent_reports WHERE student_client_id = $sid AND created_at >= '$started';
COMMIT;
SQL
  marks=$(sql -c "SELECT count(*) FROM attendance_marks WHERE student_client_id = $sid AND scanned_at >= '$started'")
  sessions=$(sql -c "SELECT count(*) FROM attendance_sessions WHERE teacher_id = $tid AND opened_at >= '$started'")
  echo "Done: both back to the default password, signed out everywhere, test results deleted"
  echo "(practice attempts, parent sign-ins with the student's code, parent reports)."
  if [[ $marks != 0 || $sessions != 0 ]]; then
    echo "Attendance made during the test is kept (it is real): $sessions session(s) opened by the teacher,"
    echo "$marks check-in(s) by the student. Correct them in the admin panel if they shouldn't count."
  fi
}
trap refresh EXIT
trap 'exit 130' INT TERM HUP # Ctrl+C or a dropped SSH connection: still put them back

cat <<EOF

Group:   $group

Teacher: $tname (id $tid)
  phone    $tphone
  password ${tphone: -4}   (default; the app asks for a new one: any you like)

Student: $sname (id $sid)
  phone    $sphone
  password ${sphone: -4}

These are real people: don't change anything they'd notice (attendance is real).
Type "done" when you have finished testing.
EOF

while IFS= read -r line; do
  [[ ${line,,} == done ]] && break
  echo 'Type "done" to put the accounts back.'
done
