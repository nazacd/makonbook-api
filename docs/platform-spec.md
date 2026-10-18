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
| D12 | A substitute teacher who isn't on the HolliHop schedule item **cannot open attendance**; an admin updates the schedule in HolliHop first. | For now; revisit if it becomes a burden. |
| D13 | **Late** = scanned after lesson start + 10 min. Teachers may correct marks for **24 h** after the lesson. | |
| D14 | One account may be logged in on **several devices** at once; device binding applies only within an attendance session. | A stricter device policy may come later. |
| D15 | **Force update**: the API exposes a minimum supported app version; older apps are blocked with an "update the app" screen. | Phase 1. |
| D16 | Students see their own **attendance table** in the app. Marks come from **HolliHop** (synced read-only, so history before the app and admins' corrections are included), with **late** added from the app's scans. | Phase 2; see 8.7. |
| D17 | **Billing**: teachers only decide present/absent. An absence is **billed by default**; only admins make it unbilled (when a student informs them), in HolliHop. The app never sends "not billed". | See 8.6. |
| D18 | **Admin sign-in uses HolliHop credentials.** HolliHop checks the login and password (`GetMemberAuthKey`); MakonBook stores neither, nor the key HolliHop returns. HolliHop has no roles, so an **access list** in our DB says who may use the panel: **super admins** (every branch, manage the list) or **branch admins** limited to any set of offices — enforced in every admin query, not only in the page. Fired staff are refused at sign-in; removing someone from the list ends their sessions at once. | First grant is a server command (`makonbook admin grant`). |
| D19 | **Practice content = SAT MAKON's own bank**, cleaned by an AI review before loading. | The bank is the teachers' own material. See 9.1. |
| D20 | **Every active student sees every practice test**; a test stays **locked until all its questions are approved**. Scores are an **estimated 200–800**. | Tests open one by one as review finishes. |
| D21 | **Editors** are a list of trusted teachers (any subject) granted by super admins; **reviewers** are teachers in a Telegram bot / Mini App, solving blind first. | Planned, 9.4. |

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
- Active-student phone quality: 4 blank, 5 invalid (checked with libphonenumber; one foreign `+1` number is valid), 4 numbers shared by 2 students (3 of 4 are siblings on a parent's phone), 15 students whose number equals their parent's.
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
| **Active student** (may log in) | HolliHop status `Занимается` (StatusId 2) **and** valid, unique normalised phone. Only students who are or have been active are stored. |
| **Group roster** | Enrollments current on that date (dates + weekday mask) **∩** active students. |
| **Active teacher** (may log in) | `Fired = false` **and** listed in `TeacherIds` of a current schedule item **and** valid unique phone. "Current" allows a margin: the item starts within 14 days and ended no more than 7 days ago, so teachers aren't logged out between one group ending and the next starting. |
| **Attendance-enabled group** | `LearningType = GROUP` and office ≠ `ONLINE` (configurable). |
| **Phone normalisation** | Parse with libphonenumber (Go: `github.com/nyaruka/phonenumbers`), default region `UZ`, so bare 9-digit numbers become `+998…`. Must pass `IsValidNumber` (catches wrong length and non-existent operator codes). Foreign numbers are allowed (e.g. `+1…`). Stored in E.164. |
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
| Lesson days, next 7 days | hourly | `GetEdUnits?queryDays=true&dateFrom&dateTo` — added in phase 2 (attendance) |
| Students (full reconcile) | nightly | `GetStudents` (paged) |

A run first **fetches everything**, then applies it **in one transaction** (groups and
schedules → teachers → students → enrollments → login phones and data-quality issues),
so a failure part-way leaves the database untouched. A full run is ~25 requests — far
under the rate limit — and takes under a minute, almost all of it HolliHop response time.
The worker (`makonbook worker`) runs incremental syncs every `SYNC_INTERVAL` and a full
one once a day after `SYNC_FULL_AT` (school time zone `TIMEZONE`); `makonbook sync
full|incremental` runs one by hand.

### 6.2 Rules
- Upsert by HolliHop id (`client_id` for students, `id` for teachers/groups/schedule items).
- Sync writes **only HolliHop-sourced columns** (names, phone, status, offices, group links). It never touches app-owned columns (password hash, username, email, flags).
- Leaving → `deactivated_at = now()`, all sessions revoked immediately. Returning → reactivated, password reset to default, `must_change_password = true`.
- Schedule items and enrollments: deleted and re-inserted inside the run's transaction (readers never see the gap). Only current and future enrollments of stored students are kept.
- Cursor (`lastUpdatedFrom`) saved **only after the run commits**, using the response's `Now`.
- **Mass-change guard:** if a run would deactivate more than 10 % of active students or teachers (ignoring changes of fewer than 5 people), it is rolled back, recorded as `blocked`, and admins are alerted. After checking the change is real, an admin re-runs it with `makonbook sync full --allow-mass-change` (an admin-panel button comes later).
- A run where HolliHop returns an empty list of teachers, groups, enrollments or (full run) students is refused as a HolliHop fault.
- One run at a time: Postgres advisory lock.
- Every run recorded in `sync_runs` (start, end, counts, errors). Alerts go to a Telegram chat via bot API (outbound only; optional) or the error log, **only when the outcome changes** — first failure/block, then recovery — not on every failing run.
- The HolliHop auth key and the Telegram bot token travel in URLs; the code strips URLs from every error so neither reaches logs or `sync_runs`.

## 7. Authentication

- **Login:** phone number in any common format, or a username once set, + password. Identifiers made only of digits and phone punctuation are phone numbers; usernames are 3–30 characters (`a-z 0-9 _ .`, lower-cased) and start with a letter, so the two never collide. Username login works even if the person's phone later becomes unusable; phone login needs a `login_phone` (spec 5).
- **First login:** the default password is the last 4 digits of the person's `login_phone`. It is **derived, never stored**: if admins correct a number in HolliHop, the default follows it. While it is in use the API marks the session `must_change_password` and only allows `/v1/me`, `/v1/auth/password` and `/v1/auth/logout` (403 `password_change_required` elsewhere). New passwords: 8–128 characters, different from the current one, and must not contain the person's 9-digit phone number. Username is optional, set afterwards.
- **Own password valid only for the current active period:** `students/teachers.activated_at` is set again on reactivation; a password set before it no longer counts, so a returning student is back on the default password (spec 6.2) without the sync ever touching auth tables. Sessions created before `activated_at` are dead for the same reason.
- **Hashing:** argon2id (19 MiB, t=2, p=1 — OWASP minimum), PHC string format, concurrency capped so a classroom logging in at once can't exhaust memory. Paths with nothing to verify spend the same time, so timing doesn't reveal whether an account exists.
- **Sessions:** opaque random token (32 bytes), stored as SHA-256 in `auth_sessions`, 30-day expiry extended on use (at most one write per hour). Dead on logout, on password change (other devices), when the person is deactivated, after reset, and after 30 days unused. Stored on device in `expo-secure-store`.
- **Brute force:** per-account lockout — every 5th consecutive failure locks for 15 min × 2^(lockouts−1), capped at 24 h (so the 4-digit default can't be guessed in practice); a successful login resets the count. Wrong current passwords in a password change count too. Per-IP limit of 100 attempts / 10 min (generous: a classroom shares one IP). Behind the edge nginx, `TRUST_PROXY=true` takes the IP from `X-Forwarded-For`.
- **Reset:** `makonbook reset-password student|teacher <id>` (admin-panel button later) forgets the own password, unlocks and ends all sessions. Email-code reset (hashed code, 15 min expiry, 5 attempts) can follow; Telegram later.
- **Errors:** unknown account and wrong password both answer 401 `invalid_credentials`; a locked account answers 429 `account_locked` with `Retry-After`.
- **Admins:** HolliHop staff on our access list, signing in with their HolliHop login (D18). Practice-content editors: a list of trusted teachers (D21).

## 8. Attendance (phase 2)

### 8.1 Flow
1. Teacher opens **Today** in the app → sees their lesson days (from synced lesson days where they are on the schedule item and the group is attendance-enabled).
2. Taps **Start attendance** (allowed from 15 min before start until lesson end). Server creates `attendance_session` with a random secret and snapshots the roster (section 5).
3. Teacher's screen shows a QR that **rotates every 10 s**, plus a live list "18 / 22 present" (polls every 3 s).
4. Student scans in the app (or with the system camera — QR is a deep link `makonbook://att/<session>/<window>/<sig>` that opens the app).
5. Server validates and records the scan.
6. Teacher taps **Finish** (or it auto-finishes at lesson end + 15 min). Results are queued for HolliHop.

### 8.2 QR token
- `window = floor(unix_time / 10)`; `sig = base64url_nopad(HMAC-SHA256(session_secret, "<session_id>:<window>"))[:16]`, session id as a lowercase UUID string (exact format: `api/openapi.yaml`, `AttendanceQR`).
- Teacher device receives the secret at session start and computes codes **locally** (keeps working if the teacher's connection drops). It also gets `server_time`, to correct its own clock.
- Server accepts `window ∈ {current − 1, current, current + 1}` (the +1 absorbs a teacher clock a few seconds fast): a code is good for 20–30 s.

### 8.3 Scan validation (all must pass)
signature valid · window fresh · session open · student in session roster · student not already marked · **this device hasn't checked in a different student in this session** · student account active.

Device id: random UUID generated on first launch, kept in secure storage.

### 8.4 States
`present` (scanned before start + 10 min), `late` (scanned after; app-only, HolliHop has no "late"), `absent` (no scan at finish). Teacher can override any student (dead phone etc.) until **24 h after the lesson**; every override is stored in `attendance_audit` (who, when, from→to) and visible to admins. **Admins** correct marks of their branches from the panel at any time (source `admin`, audited with their name); a mark the office corrected can't be changed back by the teacher (`set_by_admin`).

### 8.5 Anti-cheat stance
Rotation + device binding + teacher's live headcount. Geofencing only if abuse is observed. Teacher overrides are the main fraud vector → audit log + admin report "overrides per teacher". Parents' reports (phase 5) are a secondary check.

### 8.6 Write-back to HolliHop
- On finish (and on each later override), enqueue rows in `hollihop_outbox`. Worker sends `SetStudentPasses` batches:
  - present / late → `Pass=false`
  - absent → `Pass=true`
  - Billing (D17): teachers never set it. An absence is billed by default; only admins make it unbilled, in HolliHop. The app sends `Payable = null` (unchanged) — an untouched day is "present & payable" in HolliHop, so an absence should stay billed. **To verify in the test lesson**; if `null` leaves an absence unbilled, the app sends `Payable = true` for absences instead. Never `false`.
  - `Description = null` → admins' notes untouched.
  - `OverwriteAcceptedManually = false` → **an admin's manual entry always wins.**
- Retries with backoff; "state will not be changed" counts as success; "set manually" is recorded and shown in admin panel, not retried.
- Before go-live: verify key casing and behaviour on **one real lesson** with an admin watching in HolliHop.
- Until then the writer runs in **dry-run** mode (`HOLLIHOP_WRITE_ENABLED=false`, the default): it logs exactly what it would send, marks the rows `dry_run`, and sends nothing. Dry-run rows are never sent later. `HOLLIHOP_ABSENT_PAYABLE=true` switches absences to `Payable=true` (D17), if the test lesson calls for it.
- Built (phase 2, step 3): `hollihop_outbox` rows per student on finish; a later correction that flips present↔absent supersedes the unsent row and queues a new one (present↔late is the same in HolliHop and queues nothing). The worker sends due rows every 30 s in batches of 200, leasing them for 5 min; per-row results: `sent`, `unchanged`, `set_manually` (not retried), or retry with backoff 1 min … 1 h, `failed` after 10 attempts.

### 8.7 Student attendance table (D16)
- In the app, per group: a summary (lessons, present, late, absent) and a list of lesson days, newest first, each `present` / `late` / `absent` / `not marked yet`.
- Source: HolliHop's per-student lesson days (`GetEdUnitStudents?queryDays`, `Pass`), synced read-only into `attendance_records` — 120 days back on the nightly full run, the last 7 days every 15 minutes. Past lessons use HolliHop's mark (a lesson nobody marked counts as attended there); the app adds `late`, and today's lessons come from the app until HolliHop has them. HolliHop wins when they disagree (an admin corrected it). Built (phase 2).
- Billing is not shown to students.
- `GET /v1/me/attendance?group_id=&from=&to=`.

## 9. Practice tests (phase 3)

### 9.1 Where the content comes from (D19)
SAT MAKON's own question bank (60 practice tests `DAY1`…`DAY60`, each with Reading and Writing and Math,
module 1 and module 2; CSV exports + screenshots in R2), made by its teachers. Much of it is broken (wrong
keys, typos, cropped screenshots, explanations of older versions), so every test goes through an **AI
review** before it is loaded: the private repository `nazacd/makonbook-questionbank`, procedure in its
`tools/REVIEW.md`. Per question: transcription into text/LaTeX (only graphs, charts, geometry and maps stay
images), two blind solves (Opus, Sonnet), explanation check (Math explanations rewritten), adjudication of
flagged questions, and a risk: **green / yellow / red**. A key changes only with written proof, and such a
question is red until a teacher confirms. New tests are written later in the admin panel (9.5).

### 9.2 Content model (built: phase 3, step 1)
- **Question** (`questions`): subject (`math`|`english`), type (`mcq`|`grid_in`), passage, stem, choices
  (A–D) or null, figures (`[{key, kind, alt}]`, key = R2 object), `correct` (letter) or `accepted[]` (every
  form a student may enter), explanation, domain, skill, difficulty (an estimate), status
  (`draft`|`review`|`approved`), `ai_risk` (worst of its AI reviews), version. Text format: paragraphs,
  `\( \)`/`\[ \]`, `*i*`, `**b**`, `<u>`, `- ` lists, pipe tables, `![alt](r2:key)`.
- **One row per distinct question.** The bank repeats questions across tests (~2 % of rows exactly, more
  in practice); they are found by a hash of subject, type and text (case, spacing, curly quotes and figure
  descriptions ignored; punctuation counts, because grammar questions differ by a comma). A repeated
  question is fixed and approved once.
- **Versions** (`question_versions`): every version's content, by import or by an editor.
- **Test** (`practice_tests`, code e.g. `DAY12`) with its slots (`practice_test_questions`: subject, module
  1|2, position → question). As in the bank: per subject, module 1 and module 2 (R&W 2 × 27, Math 2 × 22);
  no adaptive routing.
- **AI reviews** (`question_ai_reviews`): each review record as it came, per test position.
- Loading: `makonbook practice import <questionbank checkout>` (deploy/DEPLOY.md) loads new tests and
  skips loaded ones; `--replace` reloads a test while none of its questions is approved by a person or edited.
- **Auto-approval:** a question whose AI review is **green** (the key confirmed by both blind solves, a
  clean transcription, no issues) is approved on import (`auto_approved`; ~84 % of the bank). Teachers work
  on the yellow and red ones. A later review of a repeat that isn't green, or an editor's change, takes the
  automatic approval back, so a person checks it.

### 9.3 Access (D20)
Every active student sees every test. A test is **locked until all its questions are approved**, then it
opens; tests open one by one as their review finishes. Scores are shown as an **estimated 200–800** per
section (College Board uses IRT; we can't reproduce it exactly).

### 9.4 Editing and review by teachers (D21)
- **Editors** (built: phase 3, step 3): a list of trusted teachers or staff (any subject), granted by super
  admins in the panel (`admin_access.editor`; an editor needs no branch). In the admin panel: every test with
  its progress, a queue of questions red first, and a question editor — the form, a live student view
  (KaTeX as MathML, so the page's strict CSP stays), the AI review summed up (what it concluded, why it
  isn't green, what to fix; the full record — solves, decision, every issue, proposed fix — and the bank's
  original screenshots one click away), and the history. Each save is a new version (`409
  version_conflict` if someone saved meanwhile; `422 invalid_question` with a message per field: the content
  format, A–D and a key, numeric grid-in answers, figures placed, domain/skill from the taxonomy). Editing an
  approved question sends it back to review. Images come from SAT MAKON's R2 bucket through the API
  (`/v1/admin/practice/figures`, read only, signed with SigV4).
- **Visual editor** (no codes for teachers): every text field is a WYSIWYG editor (TipTap) that reads and
  writes the same stored text format, so the API, its checks and the apps don't change. Bold, italic,
  underline (buttons or Ctrl+B/I/U), lists, tables, a "blank" button; math in a Desmos-like field (MathLive:
  `/` makes a fraction, `^` a power, `sqrt` a root; click a formula to change it); images pasted, dropped or
  picked, then described. Fields grow with their text; a grip under each makes it taller (double-click fits
  it again). An extra blank line is kept as an empty line of space. A `</>` button shows the text behind it. Checked on all 9,300 real fields: 99.9 %
  come back byte-identical, the rest only respaced. The editor's libraries load only on the practice pages.
- **Uploaded images** (`POST /v1/admin/practice/images`): PNG/JPEG/GIF/WebP up to 5 MB, checked by the API,
  then put in **MakonBook's own R2 bucket** (`R2_UPLOADS_*`, read-write; SAT MAKON's bucket keeps a read-only
  key) under `mb/<sha-256>.<ext>`; `practice_images` records each one. Served like the bank's images. A save
  that uses an image never uploaded is refused. The same bucket will hold the published test packages (9.5). Because of the math editor, the panel's CSP allows
  inline styles (scripts stay `'self'` only).
- **Reviewers**: teachers review through a Telegram bot / Mini App. They solve each question blind first,
  then see the key, explanation and AI findings and approve or report. The number of approvals needed
  depends on the AI risk. Linked to MakonBook by phone number.

### 9.5 Publishing and on device (planned)
- Publishing a test builds an **immutable package** (all questions, answers, explanations, conversion table)
  + images, for offline download. Editing a question later creates a new test version on the next publish;
  attempts stay tied to the version they used.
- Catalog from API → download package + images → fully offline. Answers are in the package; nothing graded
  on device is "official".
- Attempt uploaded when online with a client-generated UUID (idempotent): answers, time per question, raw and
  estimated scores. Teachers see attempts of students in their groups.

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

Vite + React + TypeScript + TanStack Query + client generated from `api/openapi.yaml`. Lives in `web/admin/` in this repo so it shares the OpenAPI contract, and ships in the API's image: `admin.makonbook.uz` serves the page and passes `/v1/admin/*` to the API, so page and API share one origin (no CORS).

**Access (D18).** Sign-in with the HolliHop login and password; the session is an HttpOnly, SameSite=Strict cookie (`mb_admin`, 12 h) and every change also needs the `X-MakonBook-Admin` header, which another site's page can't add — so the cookie alone is never enough (CSRF). Sign-in is rate-limited per network and per login name. A branch admin sees and changes only lessons, groups and people of their offices; a super admin sees all and manages the access list. Super admins manage **branch admins** in the panel (`/v1/admin/access`, HolliHop staff search). **Super admins themselves are managed only on the server** (`makonbook admin find|offices|list|grant|revoke`): the panel can't make anyone a super admin, and no super admin — another one or themselves — can be changed or removed there (`super_admin_protected`). The server command refuses to remove or demote the last super admin.

Phase 1 pages: login · sync status & run history (approve guarded runs) · **data quality** (blank/invalid/duplicate phones, teachers without phone) · students / teachers / groups (read-only mirror) · reset password · revoke sessions.
Phase 2: attendance by group/date · teacher override report · HolliHop write errors.

**Built (API, phase 2 step 4)** — every endpoint filters by the admin's branches in SQL (`internal/platform/scope`); another branch's row is "not found":
- `GET /v1/admin/lessons?date=&office_id=` — every lesson that day, with teachers and attendance counts. Left out: lessons nobody can take attendance for (no teacher on the schedule in HolliHop, or only fired ones), and lessons with nobody on the list unless the group is **new** (schedule began within 30 days and nobody has attended yet; shown as "No students"). In practice the left-out ones are finished groups whose schedule was never closed in HolliHop. Online lessons (online branch or learning type) aren't in the panel at all; individual lessons show as "Individual".
- `GET /v1/admin/attendance/sessions/{id}` — marks, every change (`history`), and each mark's HolliHop write status. `PUT …/marks/{client_id}` — admin correction (8.4).
- `GET /v1/admin/reports/corrections?from=&to=&teacher_id=` — per teacher: lessons opened, corrections, `made_present` (absent or not scanned → present/late); plus the 500 newest changes.
- `GET /v1/admin/hollihop/writes?status=` — counts by status and failed / set-manually writes; `POST …/{id}/retry` re-queues a failed one. Shows whether writing is on (dry run).
- `GET /v1/admin/data-quality` — the panel's **HolliHop issues** page: groups to fix (`no_teacher`, `no_students`: the lessons left out above, with the last lesson anyone attended and what to do) and people who can't sign in (phone issues of their branches' students and teachers).
- Super admins: `GET /v1/admin/access`, `PUT|DELETE /v1/admin/access/{employee|teacher}/{id}` (branch admins only), `GET /v1/admin/staff?q=`.

**Built (web, phase 2 step 5)** — `web/admin`: sign-in; Lessons (a day's lessons by branch, attendance status and counts); a lesson's attendance with per-student correction, how each mark was set, HolliHop status and history; Teacher corrections (per teacher, "made present" first; all changes); HolliHop writes (dry-run notice, counts, failed / set by hand, retry); HolliHop issues (groups to fix, people who can't sign in); Admins (super admins: add branch admins from HolliHop staff search or by teacher id, change their branches, remove them; super admins are listed but managed on the server). Branch filter on every page for admins with several branches. Served at `ADMIN_HOST` with a strict CSP; the page never sees the session cookie.
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
`net/http` (Go 1.22+ routing) · `pgx/v5` · `sqlc` · `goose` · `log/slog` · `oapi-codegen` (server types) · `golang.org/x/crypto/argon2` · database tests run against a real Postgres, each in its own throwaway database (`internal/platform/testdb`); CI uses a Postgres service container.

### 14.3 Conventions
- OpenAPI is the source of truth; mobile uses `openapi-typescript` + `openapi-fetch`.
- Errors: JSON `{ "error": { "code", "message" } }`, stable codes.
- All times stored UTC; office timezone kept for lesson-day logic (HolliHop `OfficeTimeZone`).
- No secrets in repo; `.env.example` documents variables.
- CI: `go vet`, `golangci-lint`, tests with Postgres, OpenAPI lint, admin web typecheck/lint/build.

## 15. Data model (phase 1–2)

The authoritative schema is `db/migrations/`; this is the overview.

```
-- built (phase 1, step 2)
offices(id PK, name, utc_offset)
groups(id PK, name, discipline, level, learning_type, office_id FK, removed_at)
schedule_items(id PK, group_id FK, begin_date NULL, end_date NULL, begin_time, end_time,
               weekdays smallint, classroom, teacher_ids int[])
teachers(id PK, first_name, last_name, phone_raw, login_phone UNIQUE NULL, fired,
         deactivated_at)
students(client_id PK, hh_student_id, first_name, last_name, phone_raw,
         login_phone UNIQUE NULL, hh_status_id NULL, office_ids int[], deactivated_at)
enrollments(id PK, group_id FK, student_client_id FK, begin_date NULL, end_date NULL, weekdays)
data_quality_issues(id, person_kind, person_id, kind, detail, first_seen_at, last_seen_at,
                    resolved_at)                -- one open row per (person, kind)
sync_runs(id, mode, status, started_at, finished_at, stats jsonb, error)
sync_cursors(name PK, value)

-- built (phase 1, step 3)
students.activated_at, teachers.activated_at       -- start of the current active period
credentials(person_kind, person_id PK, username UNIQUE NULL, password_hash NULL,
            password_changed_at NULL, failed_attempts, locked_until NULL)
            -- no row / stale password = default password
auth_sessions(id, token_hash UNIQUE, person_kind, person_id, device_id, created_at,
              last_seen_at, expires_at, revoked_at)

-- built (phase 2, step 1)
lesson_days(group_id FK, date, minutes, schedule_item_ids int[], PRIMARY KEY(group_id, date))
            -- HolliHop's actual lesson dates (GetEdUnits?queryDays), today −7 … +14 days,
            -- refreshed by every sync run; an empty answer keeps the stored days

-- built (phase 2, step 2)
attendance_sessions(id uuid PK, group_id FK, date, teacher_id FK, secret bytea, starts_at, ends_at,
                    opened_at, finished_at NULL, finished_by (teacher|auto), UNIQUE(group_id, date))
attendance_marks(session_id FK, student_client_id FK, state (pending|present|late|absent),
                 source (scan|teacher|auto) NULL, scanned_at NULL, device_id NULL, updated_at,
                 PRIMARY KEY(...), UNIQUE(session_id, device_id))   -- one row per roster student
attendance_audit(id, session_id, student_client_id, actor_kind (student|teacher|system), actor_id NULL,
                 from_state, to_state, at)                         -- every change

-- planned
admin_users(id, email UNIQUE, password_hash, totp_secret, role, created_at)

attendance_records(group_id, student_client_id FK, date, pass bool,
                   PRIMARY KEY(student_client_id, group_id, date))   -- HolliHop's marks, read-only (built)
hollihop_outbox(id, session_id FK, group_id, student_client_id, date, pass,
                status (pending|sent|unchanged|set_manually|dry_run|superseded|failed),
                attempts, next_attempt_at, last_error, created_at, done_at)   -- built, step 3

-- built (phase 3, step 3): admin_access.editor (practice editors, D21)
-- built (phase 3, step 1): see 9.2
questions(id uuid PK, subject, type, content_hash UNIQUE, passage, stem, choices jsonb, figures jsonb,
          correct, accepted text[], explanation, domain, skill, difficulty, status, ai_risk, version, …)
question_versions(question_id FK, version, content jsonb, source (import|editor), created_by, created_at)
practice_tests(id uuid PK, code UNIQUE, title, created_at)
practice_test_questions(test_id FK, subject, module, position, question_id FK, PRIMARY KEY(test_id, subject, module, position))
question_ai_reviews(id, question_id FK, test_id FK, qid, csv_id, risk, verdict, record jsonb, UNIQUE(test_id, qid))
```

## 16. Hosting & data
- The existing MakonBook VPS (Hetzner, DE; 2 vCPU, 3.7 GB RAM), shared with the Django site, the leaderboard, sat-makon.uz and school28. Guide: `deploy/DEPLOY.md`.
- **Edge:** the existing `makonbook-nginx` container owns :80/:443 and terminates TLS; `api.makonbook.uz` gets its own server block and its own Let's Encrypt lineage (the shared SAN cert is not touched). The API trusts `X-Forwarded-For` from it (`TRUST_PROXY=true`).
- **Containers:** `makonbook-api` (serve) and `makonbook-api-worker` (sync) from `nazacd/makonbook-api`, built by GitHub Actions and pulled from Docker Hub like the other services; on the shared `sat_makon_network`, no published ports, read-only filesystem.
- **Database:** the shared `db` container (Postgres 16, own Compose project `/opt/sat-makon/postgres`). MakonBook has its own database `makonbook_api` and a **non-superuser** role `makonbook_api`; no access to the other apps' databases. Dev and CI use Postgres 16 to match.
- **Security:** the server audit (2026-10) found Postgres published to the internet with a weak superuser password under active brute force. Fixed: Postgres moved to its own Compose project bound to 127.0.0.1, superuser password rotated, no signs of intrusion. Still recommended: Hetzner Cloud Firewall (inbound 22/80/443).
- **Backups:** nightly `pg_dump` of every database in `db` (there were none before), 14-day retention on the server; copy off-site (Hetzner Storage Box) and **test a restore** once a month.
- Data minimisation: only fields listed above; no birthdays, no parent data before phase 5.
- **Legal note:** Uzbekistan's personal-data law requires citizens' personal data to be stored in Uzbekistan. Hosting in Germany was chosen knowingly (HolliHop itself is hosted abroad too). Revisit if the business needs to be compliant.

## 17. API outline (v1)

```
GET  /v1/app/config                   {min_app_version, …} — no auth; app blocks itself if older
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
GET  /v1/me/attendance                 own attendance table (8.7)

# practice (phase 3)
GET  /v1/practice/tests                catalog
GET  /v1/practice/tests/{id}/package   → signed URL to versioned package
POST /v1/practice/attempts             idempotent by client UUID

# admin (cookie session, D18)
POST /v1/admin/login                   {login, password} → HolliHop checks; access list; sets mb_admin
POST /v1/admin/logout
GET  /v1/admin/me                      name, super, branches they can see
GET  /v1/admin/lessons?date=           lessons of their branches, with attendance counts
GET  /v1/admin/attendance/sessions/{id}            marks, history, HolliHop write status
PUT  /v1/admin/attendance/sessions/{id}/marks/{client_id}   admin correction, any time
GET  /v1/admin/reports/corrections     teachers' corrections
GET  /v1/admin/hollihop/writes         write-back status; POST …/{id}/retry
GET  /v1/admin/data-quality            phone issues
GET|PUT|DELETE /v1/admin/access…       access list (super admins); GET /v1/admin/staff?q=
/v1/admin/...                          later: sync runs, people, reset, content
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
Go skeleton, DB, migrations, sync worker with guards, auth, OpenAPI; admin web (sync, data quality, people, reset); mobile: force-update check, login, forced password change, role shells, profile; placement data changes (section 10). Deploy to Hetzner.

**Phase 2 — attendance**
Lesson days, sessions, QR, scanning, overrides, outbox write-back (dry-run until the test lesson), student attendance table, admin attendance pages.

**Phase 3 — practice tests**
1. Question bank + importer of AI-reviewed tests (built). 2. AI review of all 60 tests (running). 3. Editor panel
(built). 4. Telegram review bot. 5. Approval rules and unlocking. 6. Mobile engine, Practice tab, teacher view.

**Phase 4 — vocabulary.** **Phase 5 — parents.** Later: Telegram login/reset, push notifications.

## 20. Open items
None at the moment — all earlier open items are recorded as D12–D17. Deferred on purpose: stricter device policy (D14), substitute-teacher handling without a HolliHop change (D12), Telegram, push notifications.
