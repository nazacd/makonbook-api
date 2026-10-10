# MakonBook Platform — Spec v1

Status: **agreed plan, not yet implemented.** Covers the Go API (this repo), the
React admin web app, and the next phases of the Expo app (`makonbook-mobile`).
The existing placement-test spec lives in `makonbook-mobile/sat-makon-placement-test-spec.md`
and stays valid except where section 10 below changes it.

---

## 1. What we are building

A platform for current MakonBook students only, with three products on top of one
backend:

| Product | Users | Phase |
|---|---|---|
| **Attendance by QR** — teacher shows a rotating QR, students scan it, result is written to HolliHop | students, teachers, admins | 2 |
| **SAT practice tests** — Bluebook-style adaptive tests written by MakonBook's main teachers, usable offline | students, teachers, content authors | 3 |
| **Vocabulary bank** — Anki-style spaced repetition (FSRS) | students | 4 |
| Parent access (see child's attendance, report problems to admins) | parents | 5 |
| Placement (Level Check) test — public, no login, unchanged in spirit | prospective students + staff | exists |

Access (except placement) is limited to people who are active in HolliHop.

## 2. Decisions log

| # | Decision | Notes |
|---|---|---|
| D1 | Placement test stays **public**: offered on first launch, no login, nothing stored. | Taken under staff supervision. |
| D2 | **Login = mobile number** from HolliHop. Initial password = last 4 digits. Forced password change on first login; user may also set a username. Problems are fixed by admins in HolliHop. | |
| D3 | Attendance is a **student–teacher** flow in the app and is **written directly to HolliHop** (`SetStudentPasses`). | Admins stop marking presence by hand. Billing stays with admins (see 8.6). |
| D4 | Online and one-to-one lessons are **excluded** from app attendance. | |
| D5 | Admin panel is a **small React web app**. | |
| D6 | Parents: later phase. Telegram (bot login/reset): later. Vocabulary: later, Anki-style (FSRS). | |
| D7 | Hosting: **Hetzner VPS, Germany**. | Chosen knowingly; see 16. |
| D8 | Mobile: **development builds** (not Expo Go), Android + **iOS**. | |
| D9 | Placement JSON keeps integer IDs; gains explicit `type` and multi-answer grid-ins. | |
| D10 | Practice content: questions stored in Postgres, tests **published as immutable JSON packages** for offline download. | |
| D11 | Students/teachers who leave are **deactivated, not hard-deleted**. | Changed from the original sync note: attendance, attempts and vocab progress exist only in our DB, and a bad sync must not be able to wipe them. A returning student keeps the same HolliHop `ClientId` and is simply reactivated (password reset to default + forced change). |

## 3. System overview

```
 ┌──────────────┐        ┌──────────────────────────────┐        ┌──────────┐
 │ Expo app     │ HTTPS  │ makonbook (Go, one binary)   │ HTTPS  │ HolliHop │
 │ student /    ├───────►│  serve : REST API            ├───────►│ Api/V2   │
 │ teacher      │        │  worker: sync + HolliHop     │◄───────┤          │
 └──────────────┘        │          writes (outbox)     │        └──────────┘
 ┌──────────────┐        │                              │
 │ Admin web    ├───────►│                              │
 │ (React)      │        └──────────────┬───────────────┘
 └──────────────┘                       │
                                ┌───────┴────────┐   ┌──────────────────┐
                                │ PostgreSQL     │   │ Object storage   │
                                │                │   │ (test packages,  │
                                └────────────────┘   │  images)         │
                                                     └──────────────────┘
```

- **HolliHop is the source of truth** for people, groups, schedules and enrollments.
  We hold a minimal mirror of it.
- **MakonBook's DB is the source of truth** for credentials, attendance sessions/scans,
  practice content and attempts, vocab progress.
- Attendance results flow **back** to HolliHop.

## 4. HolliHop: facts the design relies on

Verified against the live account (read-only) on 2026-10-09/10 and the
[API 2.0 docs](https://hollipedia.t8s.ru/books/api/page/hollihop-api-20).

### 4.1 API constraints
- Base `https://<school>.t8s.ru/Api/V2/<Method>?authkey=…`. Key lives only in server env (`HOLLIHOP_BASE_URL`, `HOLLIHOP_AUTHKEY`).
- **Rate limit: 600 requests / 30 s.** Bulk enumeration patterns can get the key blocked — keep paging sane (`take` up to 10 000) and never parallelise needlessly.
- `GetStudents.lastUpdatedFrom` only reflects **status, contact and assignee** changes. Name changes don't bump it → a nightly full pull is required.
- Responses carry a `Now` timestamp (server time) — use it as the next incremental cursor.

### 4.2 Students
- 5 369 records; **1 065 with status `Занимается` (StatusId 2)**. Other statuses: 3 Закончил обучение, 4 Ушел (старый ученик), 5 Ушел (после пробного урока), 6 Отложил обучение.
- **Identity key is `ClientId`, not `Id`.** Enrollments reference `StudentClientId`.
- Phone formats vary (`+998…`, bare 9 digits, spaces, brackets) → must normalise.
- Active-student phone quality: 4 blank, 5 wrong length, 4 numbers shared by 2 students (3 of 4 are siblings on a parent's phone), 15 students whose number equals their parent's.
- `Birthday` is junk for most records (defaults to creation date) and `Maturity` is unreliable → **not synced**.

### 4.3 Groups, schedules, teachers
- 743 ed units (all `Type=Group`, including one-to-one), **177 with a current schedule**.
- Each group is **single-subject** (`Discipline`: FOUNDATION MATH, PRE SAT ENG, …). Typical student is in 2 groups (one Math, one English).
- Teachers are attached to **schedule items** (`ScheduleItems[].TeacherIds`), not to the group. Currently 170/177 groups have exactly one teacher, 7 none; no item has >1 teacher. 243 groups have changed teacher over their lifetime.
- `LearningType`: `GROUP`, `IV OFFLINE`, `IV ONLINE`, `ONLINE`.
- Teachers: `Fired` flag ≡ `GetEmployeeStatuses` (1 Работает / 2 Уволен). 91 not fired, but only **48 appear in current schedules**; **39 of those 48 have no mobile number** (admins are filling them in — prerequisite for teacher login).

### 4.4 Enrollments
- `GetEdUnitStudents?statuses=Normal` returns **full history** (11 820 rows) — filter by `BeginDate ≤ today ≤ EndDate (or null)` client-side.
- **808 current-looking enrollments belong to students who already left** (admins don't close them). So enrollment alone must never grant access or roster membership.
- Each enrollment has its own `Weekdays` mask — a student may attend only some of the group's days.
- `StudentAgents` (parents) present for ~69 % of active students; `WhoIs` is free text. Not synced until phase 5.

### 4.5 Attendance in HolliHop
- Per student per lesson day: `Pass` (absent), `StudentPayableMinutes`, `TeacherPayableMinutes`, `Description`, `Accepted`.
- **Attendance drives billing and teacher pay.** In a sample, 1 510 absences: 725 billed, 785 not — admins decide per case.
- Unmarked day defaults to **present & payable**.
- Write method `SetStudentPasses` (POST, JSON array): `Date`, `EdUnitId`, `StudentClientId`, `Pass`, `Payable`, `Description`, `OverwriteAcceptedManually` (default **true**). `null` fields are left unchanged. Errors come back in an `Errors[]` array per row; "The pass and payable state will not be changed" means no-op.

## 5. Identity & access rules

| Rule | Definition |
|---|---|
| **Active student** (may log in) | HolliHop status `Занимается` **and** valid, unique normalised phone. |
| **Group roster** | Enrollments current on that date (dates + weekday mask) **∩** active students. |
| **Active teacher** (may log in) | `Fired = false` **and** listed in `TeacherIds` of at least one current schedule item **and** valid unique phone. |
| **Attendance-enabled group** | `LearningType = GROUP` and office ≠ `ONLINE` (configurable). |
| **Phone normalisation** | Strip non-digits; 9 digits → prefix `998`; must be 12 digits starting `998`; stored as `+998XXXXXXXXX`. |
| **Conflicts** | A phone shared by two active people → **both** are excluded from login and listed in the admin *Data quality* page. Never silently pick one. |

A person who is both teacher and student (same phone) is a conflict too unless explicitly linked by an admin.

## 6. Sync

### 6.1 Schedule
| Job | Frequency | Method |
|---|---|---|
| Teachers (full) | 15 min | `GetTeachers` |
| Groups + schedule items (full) | 15 min | `GetEdUnits` (paged) |
| Students (incremental) | 15 min | `GetStudents?lastUpdatedFrom=<cursor>` — **no status filter** |
| Enrollments (full rebuild) | 15 min | `GetEdUnitStudents?statuses=Normal` (paged), filtered by date client-side |
| Lesson days, next 7 days | hourly | `GetEdUnits?queryDays=true&dateFrom&dateTo` |
| Students (full reconcile) | nightly | `GetStudents` (paged) |

Order within a run: **teachers → groups/schedules → students → enrollments**. A full
run is ~25 requests — far under the rate limit.

### 6.2 Rules
- Upsert by HolliHop id (`client_id` for students, `id` for teachers/groups/schedule items).
- Sync writes **only HolliHop-sourced columns** (names, phone, status, offices, group links). It never touches app-owned columns (password hash, username, email, flags).
- Leaving → `deactivated_at = now()`, all sessions revoked immediately. Returning → reactivated, password reset to default, `must_change_password = true`.
- Enrollments: rebuild into a temp table, diff, apply **in one transaction**.
- Cursor (`lastUpdatedFrom`) saved **only after the run commits**, using the response's `Now`.
- **Mass-change guard:** if a run would deactivate > 10 % of active students or teachers, abort, alert, and require an admin to approve the run in the admin panel.
- One run at a time: Postgres advisory lock.
- Every run recorded in `sync_runs` (start, end, counts per entity, errors). Failure → alert (Telegram message to an admin chat via bot API — outbound only).

## 7. Authentication

- **Login:** phone (or username once set) + password.
- **First login:** password = last 4 digits of phone; account has `must_change_password` → app forces a new password (min 8 chars, not equal to default) and offers an optional username before anything else is reachable.
- **Hashing:** argon2id.
- **Sessions:** opaque random token (32 bytes), stored **hashed** in `auth_sessions`, 30-day sliding expiry, checked on every request. Revoked on password change (other devices), deactivation, or admin action. Stored on device in `expo-secure-store`.
- **Brute force:** per-account lockout with backoff (5 failures → 15 min), per-IP rate limit. Matters because the default password is guessable by classmates.
- **Reset:** admin "reset to default" in admin panel (phase 1). Email-code reset (hashed code, 15 min expiry, 5 attempts) can follow; Telegram later.
- **Admins:** separate `admin_users` (email + password + TOTP), created via CLI. Roles: `admin`, `content_author` (main teachers writing practice content).

## 8. Attendance (phase 2)

### 8.1 Flow
1. Teacher opens **Today** in the app → sees their lesson days (from synced lesson days where they are on the schedule item and the group is attendance-enabled).
2. Taps **Start attendance** (allowed from 15 min before start until lesson end). Server creates `attendance_session` with a random secret and snapshots the roster (section 5).
3. Teacher's screen shows a QR that **rotates every 10 s**, plus a live list "18 / 22 present" (polls every 3 s).
4. Student scans in the app (or with the system camera — QR is a deep link `makonbook://att/<session>/<window>/<sig>` that opens the app).
5. Server validates and records the scan.
6. Teacher taps **Finish** (or it auto-finishes at lesson end + 15 min). Results are queued for HolliHop.

### 8.2 QR token
- `window = floor(unix_time / 10)`; `sig = base64url(HMAC-SHA256(session_secret, session_id ‖ window))[:16]`.
- Teacher device receives the secret at session start and computes codes **locally** (keeps working if the teacher's connection drops).
- Server accepts `window ∈ {current, current − 1}`.

### 8.3 Scan validation (all must pass)
signature valid · window fresh · session open · student in session roster · student not already marked · **this device hasn't checked in a different student in this session** · student account active.

Device id: random UUID generated on first launch, kept in secure storage.

### 8.4 States
`present` (scanned before start + 10 min), `late` (scanned after; app-only, HolliHop has no "late"), `absent` (no scan at finish). Teacher can override any student (dead phone etc.) until **24 h after the lesson**; every override is stored in `attendance_audit` (who, when, from→to) and visible to admins.

### 8.5 Anti-cheat stance
Rotation + device binding + teacher's live headcount. Geofencing only if abuse is observed. Teacher overrides are the main fraud vector → audit log + admin report "overrides per teacher". Parents' reports (phase 5) are a secondary check.

### 8.6 Write-back to HolliHop
- On finish (and on each later override), enqueue rows in `hollihop_outbox`. Worker sends `SetStudentPasses` batches:
  - present / late → `Pass=false`
  - absent → `Pass=true`
  - `Payable = null` → **unchanged; billing stays with admins**.
  - `Description = null` → admins' notes untouched.
  - `OverwriteAcceptedManually = false` → **an admin's manual entry always wins.**
- Retries with backoff; "state will not be changed" counts as success; "set manually" is recorded and shown in admin panel, not retried.
- Before go-live: verify key casing and behaviour on **one real lesson** with an admin watching in HolliHop.

## 9. Practice tests (phase 3)

### 9.1 Content model
- **Question** (row in Postgres): `id` (uuid), `subject` (`math`|`rw`), `type` (`mcq`|`grid_in`), `difficulty` (easy|medium|hard), `domain`, `skill`, `stimulus`, `stem`, `options[4]` or `null`, `correct` (letter) or `accepted[]` (strings, parsed as rationals), `explanation`, `images[]`, `status` (draft|review|published), author, timestamps. Text supports inline LaTeX `\( … \)`.
- **Test**: title, subject, format, and modules. Digital-SAT format per subject: **RW 2 × 27 questions, 32 min each; Math 2 × 22 questions, 35 min each**, i.e. `module_1`, `module_2_easy`, `module_2_hard` + routing threshold on module 1.
- **Score conversion table** per test (raw → estimated 200–800 per section). Shown as an **estimate** — College Board uses IRT; we can't reproduce it exactly.

### 9.2 Publishing
Publishing a test builds an **immutable package** `tests/<test_id>/v<N>.json` (all questions, answers, explanations, routing, conversion table) + images, uploaded to object storage. Editing a published question creates a new test version on next publish; old attempts stay tied to the version they used.

### 9.3 On device
- Catalog from API → download package + images to `FileSystem` → fully offline.
- Answers are in the package (needed for offline module-2 routing). Acceptable for practice; nothing graded on device is "official".
- Attempt uploaded when online with a client-generated UUID (idempotent): answers, time per question, module path, raw and estimated scores.
- Teachers see attempts of students in their groups.

### 9.4 Authoring
In the admin web app by users with `content_author` role: question editor with live LaTeX preview, image upload, test builder (pick questions into modules), conversion table, publish button.

**Copyright:** content must be original or licensed — no copies of College Board material.

## 10. Placement test changes (mobile, small)

Stays bundled, public, offline, unscored-on-server (D1, D9). Data model changes:
- add `type: "mcq" | "grid_in"` (stop inferring from `options: null`);
- grid-in `correct: number` → `accepted: string[]` (each parsed as decimal or fraction, compared with tolerance), to allow several correct answers;
- content guideline: rebalance tiers toward 17/17/16 (Math currently 26/15/9, English 16/20/14).

## 11. Vocabulary (phase 4)
Words and lists in Postgres; lists assignable to groups. Scheduling with **FSRS** (Anki's current default; Go port `go-fsrs`). Device stores an append-only review log and syncs it; card state is recomputed from the log → offline-safe, conflict-free.

## 12. Parents (phase 5)
Sync `StudentAgents` mobiles; one parent account ↔ many children; see attendance; "report a problem" to admins.

## 13. Admin web (React)

Vite + React + TypeScript + TanStack Query + client generated from `api/openapi.yaml`. Served as static files by Caddy. Lives in `web/admin/` in this repo so it shares the OpenAPI contract.

Phase 1 pages: login · sync status & run history (approve guarded runs) · **data quality** (blank/invalid/duplicate phones, teachers without phone) · students / teachers / groups (read-only mirror) · reset password · revoke sessions.
Phase 2: attendance by group/date · teacher override report · HolliHop write errors.
Phase 3: content authoring (9.4).

## 14. Backend architecture (Go)

### 14.1 Layout — modular monolith, packaged by feature
```
cmd/makonbook/main.go        subcommands: serve | worker | migrate | admin-create
internal/
  platform/                  config, postgres pool, http server, logging, clock, ids
  hollihop/                  API client + DTOs, behind an interface (fake in tests)
  sync/                      jobs, guard, run log, cursor
  people/                    students, teachers, groups, schedules, enrollments, rosters
  auth/                      login, sessions, password, lockout
  attendance/                lesson days, sessions, tokens, scans, overrides, outbox
  practice/                  questions, tests, publishing, attempts
  vocab/                     (phase 4)
  admin/                     admin users, admin-only handlers
db/
  migrations/                goose SQL migrations
  queries/                   sqlc query files
api/openapi.yaml             contract for mobile + admin web
web/admin/                   React admin app
```
Inside a feature: `handler.go` (HTTP ↔ service), `service.go` (rules), `store.go` (sqlc). Features depend on each other only through small interfaces; no cross-feature table access.

### 14.2 Libraries
`net/http` (Go 1.22+ routing) · `pgx/v5` · `sqlc` · `goose` · `log/slog` · `oapi-codegen` (server types) · `golang.org/x/crypto/argon2` · `testcontainers-go` for store tests.

### 14.3 Conventions
- OpenAPI is the source of truth; mobile uses `openapi-typescript` + `openapi-fetch`.
- Errors: JSON `{ "error": { "code", "message" } }`, stable codes.
- All times stored UTC; office timezone kept for lesson-day logic (HolliHop `OfficeTimeZone`).
- No secrets in repo; `.env.example` documents variables.
- CI: `go vet`, `golangci-lint`, tests with Postgres, OpenAPI lint, admin web typecheck/lint/build.

## 15. Data model (phase 1–2)

```
students(client_id PK, first_name, last_name, phone UNIQUE NULL, hh_status_id,
         offices text[], hh_updated_at, deactivated_at,
         username UNIQUE NULL, password_hash, must_change_password, email NULL,
         created_at, updated_at)
teachers(id PK, first_name, last_name, phone UNIQUE NULL, fired, deactivated_at,
         username, password_hash, must_change_password, created_at, updated_at)
groups(id PK, name, discipline, subject, level, learning_type, office_id, office_name,
       office_tz, attendance_enabled, synced_at)
schedule_items(id PK, group_id FK, weekdays smallint, begin_time, end_time,
               begin_date, end_date NULL, classroom, teacher_ids int[])
enrollments(group_id, student_client_id, begin_date, end_date NULL, weekdays, status,
            PRIMARY KEY(group_id, student_client_id, begin_date))
lesson_days(group_id, date, schedule_item_ids int[], minutes, PRIMARY KEY(group_id, date))
data_quality_issues(id, entity, entity_id, kind, detail, first_seen, resolved_at)
sync_runs(id, kind, started_at, finished_at, status, counts jsonb, error, needs_approval)
sync_cursors(name PK, value)
auth_sessions(id, token_hash UNIQUE, subject_type, subject_id, device_id, created_at,
              last_seen_at, expires_at, revoked_at)
login_attempts(...)
admin_users(id, email UNIQUE, password_hash, totp_secret, role, created_at)

attendance_sessions(id uuid PK, group_id, date, teacher_id, secret, opened_at,
                    finished_at NULL)
attendance_roster(session_id, student_client_id, PRIMARY KEY(...))
attendance_marks(session_id, student_client_id, state, scanned_at NULL, device_id NULL,
                 source (scan|teacher), PRIMARY KEY(session_id, student_client_id))
attendance_audit(id, session_id, student_client_id, actor_type, actor_id, from_state,
                 to_state, at)
hollihop_outbox(id, kind, payload jsonb, attempts, next_attempt_at, last_error,
                done_at NULL)
```

## 16. Hosting & data
- Hetzner VPS (DE), Docker Compose: `caddy`, `makonbook serve`, `makonbook worker`, `postgres`.
- Nightly `pg_dump` to Hetzner Object Storage / Storage Box, 14-day retention, **restore tested** once a month.
- Data minimisation: only fields listed above; no birthdays, no parent data before phase 5.
- **Legal note:** Uzbekistan's personal-data law requires citizens' personal data to be stored in Uzbekistan. Hosting in Germany was chosen knowingly (HolliHop itself is hosted abroad too). Revisit if the business needs to be compliant.

## 17. API outline (v1)

```
POST /v1/auth/login                    {login, password} → {token, must_change_password, role}
POST /v1/auth/change-password
POST /v1/auth/logout
GET  /v1/me                            profile, role, groups

# teacher
GET  /v1/teacher/lessons?date=         lesson days for the teacher
POST /v1/attendance/sessions           {group_id, date} → {session_id, secret, roster}
GET  /v1/attendance/sessions/{id}      live marks
POST /v1/attendance/sessions/{id}/finish
PUT  /v1/attendance/sessions/{id}/marks/{client_id}   override

# student
POST /v1/attendance/scan               {session_id, window, sig, device_id}
GET  /v1/me/attendance                 own history

# practice (phase 3)
GET  /v1/practice/tests                catalog
GET  /v1/practice/tests/{id}/package   → signed URL to versioned package
POST /v1/practice/attempts             idempotent by client UUID

# admin
/v1/admin/...                          sync runs, data quality, people, reset, attendance, content
```

## 18. Mobile app changes

- Routes: `(public)` — welcome, placement, login; `(student)`; `(teacher)`. Root layout picks by auth state/role. Placement offered on first launch and always reachable from the welcome screen.
- New deps (check Expo SDK 57 docs before adding, per `AGENTS.md`): `expo-secure-store`, `expo-camera` (QR scanning), `react-native-qrcode-svg` (teacher QR, uses existing `react-native-svg`), `@tanstack/react-query`, `openapi-fetch`, `expo-file-system`, `expo-image`.
- **Extract a generic test engine** from the placement flow (timer, navigator, eliminator, mark-for-review, resume, review). Placement and practice tests become two configurations of it.
- Development builds via EAS (`development` profile exists). iOS via EAS Build + TestFlight (Apple Developer account required).
- Agent feedback loop: run Claude Code **locally** on the computer the Android phone is plugged into; the agent installs/launches the dev build and takes screenshots via `adb`.

## 19. Phases

**Phase 0 — prerequisites (non-code)**
- Admins fill in the 39 missing teacher phones and fix student phone issues (4 blank, 5 invalid, 4 duplicates).
- Apple Developer account; domain + DNS for API and admin.
- Confirm `SetStudentPasses` behaviour on one real lesson (8.6).

**Phase 1 — foundation**
Go skeleton, DB, migrations, sync worker with guards, auth, OpenAPI; admin web (sync, data quality, people, reset); mobile: login, forced password change, role shells, profile; placement data changes (section 10). Deploy to Hetzner.

**Phase 2 — attendance**
Lesson days, sessions, QR, scanning, overrides, outbox write-back, admin attendance pages.

**Phase 3 — practice tests**
Authoring in admin, publishing, generic test engine on mobile, offline packages, attempts, teacher view.

**Phase 4 — vocabulary.** **Phase 5 — parents.** Later: Telegram login/reset, push notifications.

## 20. Open items
1. Substitute teacher not on the HolliHop schedule item: they can't open a session — admin must update HolliHop first. Acceptable?
2. "Late" threshold (proposed start + 10 min) and teacher override window (proposed 24 h).
3. Should one account be allowed on several devices at once? (Proposed: yes; device binding is per attendance session only.)
4. Minimum supported app version endpoint (force-update) — proposed for phase 1.
