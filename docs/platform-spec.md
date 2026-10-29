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
| D21 | **Every question that isn't green gets an AI fix** (even made up, labelled so). It is approved one of two ways: by an **editor** in the panel (a list of trusted teachers granted by super admins), or by a **vote of the teachers' Telegram group** on the original next to the fixed version. No blind solving. | Changed 2026-10-20 (was: reviewers solve blind first). See 9.4. |

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
- `StudentAgents` (parents) present for ~69 % of active students; `WhoIs` is free text. Not synced: parents use the child's code instead (section 12).

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
- **Demo account (app store review):** `demo.student` with the `DEMO_PASSWORD` setting (`internal/demo`, `makonbook demo [remove]`, refreshed daily by the worker): a fictional student, two fictional classes, eight weeks of attendance, practice results rising, with ups and downs, from 1240 (Day 1) to 1520 (Day 9). It lives in the mirror tables under **negative ids**, which the sync never touches; its groups have no lesson days, so no teacher or admin can take attendance there and nothing reaches HolliHop; its branch isn't listed in the admin panel and parents' reports about it skip Telegram.

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
  automatic approval back, so a person checks it. When a green question's review proposed a fix (a typo, a
  missing word: 25 questions), the import applies it as a version of its own (`ai_fix`, "Fixed by the AI
  review"), so the history shows it and an editor can undo it; a fix that can't be applied whole sends the
  question to review instead.
- **The fix step** (questionbank `tools/review.py fix`, after a test's review): Opus writes a corrected version
  of every question that isn't green, from the bank's original screenshots and the review's findings, labelled
  by kind: **corrected** (typo, transcription), **restored** (missing words or data, from context), **invented**
  (made up: a new distractor, a rewritten sentence; allowed, labelled), **redrawn** (a figure drawn again as
  SVG, stored as PNG in the questionbank's `figures/` and uploaded to MakonBook's bucket on import). It solves
  the fixed version again (`check`) and says when a person must step in (`needs_person`: a photo it can't
  redraw). The fix sits beside the reviewed question (`fix` in the record); nothing changes until a person or
  the group approves it. `practice import` brings fixes into tests already loaded.
- **In the editor:** the AI summary shows the fixed version with the changed words marked and **Apply fix**,
  which fills the form; the editor checks the student view, saves and approves. (Before the fix step, only the
  adjudicator's `proposed_fix`, when it had one.)
- **Approving in the editor** (beside the Telegram group vote): an editor approves the version they see
  (`POST …/questions/{id}/approval`, `409` if it changed meanwhile); it must pass the checks a save does, so
  a question without a key can't be approved. `approved_by` keeps who. An approval can be taken back.

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
- **The teachers' Telegram group** (built: phase 3, step 4.2, `internal/reviewbot`, `makonbook review-bot`;
  about 40 teachers). Questions an editor has already changed, or whose fix needs a person, stay with the
  editors; a question goes to the group only when its fix applies and passes the checks a save does
  (`practice.GroupReady`), so the group's approval can always be applied (`ApproveByGroup`: a version
  `group`, approved by "Teachers' group (N for, M against)"). Settings in `review_bot` (thresholds, open
  limit, pause); posts, votes and deny reasons in `group_posts`, `group_votes`, `group_deny_reasons`. A bot posts each question that
  isn't green as a pair of images: the bank's original screenshots, then the fixed version drawn like
  Bluebook (white, purple accent, our logo and "MakonBook" in the header; R&W: passage left, question and
  choices right; Math: centred; the correct choice marked; a grid-in shows its accepted answers separated
  by commas), with the AI's changes highlighted and its note as the caption. Two buttons, **Approve** and
  **Deny**: one vote per Telegram account, anyone in the group may vote, a vote can be changed until the
  decision. The decision: **red** 10 approvals or 5 denials; **yellow** 5 approvals or 5 denials (settings).
  Counts stay hidden until then, so votes don't follow each other. Approved: the fix is applied as a version
  of its own and the question is approved. Denied: it goes to the editors' queue, with the reasons the bot
  asks for (optional). Posted one an hour from 08:00 to 23:00 while fewer than `max_open` (20) are undecided; a question
  without enough votes in `vote_hours` (48) closes (`expired`) and stays for the editors, freeing its place.
  Each decided post's outcome line is kept (`group_posts.outcome`); an edit Telegram refuses is tried again at the
  next check, with a pause between edits (Telegram's limit for a group). An editor's
  decision in the panel comes first and closes the vote; a later edit sends the question back to review.
  The images are drawn on the server by a headless browser (its own container). When an update changes the
  picture page, the bot draws its open posts again on start and replaces their photos in place
  (`editMessageMedia`; each post keeps the page version it was drawn with, `group_posts.picture_version`).
- **Question pictures** (built: phase 3, step 4.1): a fixed template, not AI. The page (web/admin's
  `src/card`, built apart into `dist/card` with its fonts and logo inline) is opened whole in a headless
  Chrome (`renderer` container, internal network, no internet) by `internal/platform/render`; the API
  fills it at the moment it's needed (`practice.Service.Picture`) with the question and its images as
  data URLs, and takes a 2560 px wide picture (16:9 at least, taller when the question needs it). Three
  kinds: **original** (the bank's own question: its text from the questionbank's `original.jsonl`,
  imported into the review record, and its screenshots in place), **fix** (the AI's fixed version, its
  changes highlighted) and **question** (the editor's **Export image**: what is in the form, saved or
  not). Math in the picture is KaTeX's HTML with its fonts; answer choices draw math full size.

### 9.5 Publishing and on device (built: phase 3, step 5)
- **Publishing** (`practice.Service.Publish`; the worker every 5 minutes, or `makonbook practice publish`):
  a test whose questions are all approved is built into an **immutable package** (`practice_test_versions`:
  version 1, 2, …): its sections in Bluebook order (Reading and Writing, then Math), each module with its
  time (32 and 35 minutes) and questions (text in the stored format, keys, accepted answers, explanations,
  tags), each section's score scale, and the files of its images. A new version is made only when the
  content changed (a hash of it). Images are copied into MakonBook's bucket under `p/img/<sha-256>.<ext>`
  (`practice_published_images`, once per file). Editing a question later sends it back to review: students
  keep the last version until the test is approved again, then get the next one.
- **Scores:** each section's raw score (number right in both modules) → an **estimated 200–800**, from a
  table stretched from College Board's published practice tests (`practice.Scale`); built into each package,
  so the device and the server agree. Grid-ins are right when the answer is one of the accepted forms or the
  same number (.5 = 1/2).
- **Catalog** (`GET /v1/practice/tests`, students and teachers): every test; published ones open at their
  latest version with their download size, the rest locked with their progress.
- **Download** (`GET /v1/practice/tests/{code}/package[?version=]`): the package and a link to each image,
  signed (SigV4 query) for 6 hours, straight from R2. The device stores both and works fully offline.
- **Attempts** (`POST /v1/practice/attempts`): uploaded when online with the device's UUID (idempotent: a
  repeat returns the stored one; another student's id is `409`): the sections taken (one or both), times,
  answers with seconds per question. The server grades them again from that version's keys
  (`practice_attempts.scores`); nothing graded on the device is official. `GET /v1/me/practice/attempts`:
  the student's history. **Teachers** see the attempts of their groups' students
  (`GET /v1/teacher/groups/{id}/practice`).

## 10. Placement test changes (mobile, small)

Stays bundled, public, offline, unscored-on-server (D1, D9). Data model changes:
- add `type: "mcq" | "grid_in"` (stop inferring from `options: null`);
- grid-in `correct: number` → `accepted: string[]` (each parsed as decimal or fraction, compared with tolerance), to allow several correct answers;
- content guideline: rebalance tiers toward 17/17/16 (Math currently 26/15/9, English 16/20/14).

## 11. Vocabulary (phase 4, built)
SAT MAKON's word lists (~1,200 words), studied like Anki: spaced repetition with **FSRS** on the phone. **Every
student sees every list** (no assignment); a student turns on the lists they want. Teachers see their groups'
progress. The words are prepared outside MakonBook (fixed and adjusted in their own repository) and loaded
with an import, like the practice tests.

### 11.1 Import format
`makonbook vocab import <path>` (`internal/vocab`): a folder holding **`words.jsonl`** (one JSON object per line)
or **`words.csv`** (a header with the same names), and an optional **`images/`** folder. Fields:

| field | | |
|---|---|---|
| `list` | required | the list's id: lower-case letters, digits, dashes (`sat-1`) |
| `list_title` | required | e.g. `SAT Words 1`; the same for every word of a list |
| `word` | required | ≤ 100 characters; unique in its list (case ignored) |
| `part_of_speech` | optional | e.g. `verb` |
| `definition` | required | English, learner-friendly, ≤ 1000 characters |
| `example` | required | one sentence using the word, ≤ 1000 characters |
| `image` | optional | a file name in `images/`: PNG, JPEG or WebP, ≤ 2 MB, ≤ 4000 px a side |

Lists appear in the order of the file, words in their list in the order of the file. Each import makes the
bank what the file says: a word keeps its id (found by list and word, so its reviews follow it), changed text is
updated, words and lists the file no longer has are removed (hidden; their reviews stay). Nothing is changed if
any line is wrong; the command lists the problems. Images are copied to MakonBook's bucket under
`v/img/<sha-256>.<ext>` (`vocab_images`). Each import sets the **deck version** (a hash of what students get).

### 11.2 On the phone
- **Deck** (`GET /v1/vocab/deck?have=<version>`): every list and word; only `unchanged` when the phone's is
  current. **Images** per list (`GET /v1/vocab/lists/{slug}/images`, links signed for 6 hours), downloaded when a
  list is turned on. Everything is stored on the phone; studying works offline.
- **Scheduling:** FSRS (`ts-fsrs`, default parameters, desired retention 90 %, no fuzz so it is the same on every
  phone). A study session takes the cards due, then new ones (default 20 a day). Card front: the word; back:
  definition, example, image. Ratings Again / Hard / Good / Easy, each showing when the card comes back.
- **The log:** every review is appended to a log on the phone (`id` = UUID, word, rating, time, time taken, and
  the card's state after it). Card states are always worked out by replaying the log in time order, so reviews
  from two phones merge without conflicts. The phone uploads new entries (`POST /v1/vocab/reviews`, 200 at a time,
  idempotent by id) and fetches the ones it doesn't have (`GET /v1/me/vocab/reviews?after=<seq>`), e.g. on a new
  phone (`vocab_reviews`).
- **Settings** are per phone: the lists turned on and new words a day (10/20/30/50). A phone where the student
  never changed them (a new phone) counts the lists they already studied as on, so progress carries on.
- **Teachers** (`GET /v1/teacher/groups/{id}/vocab`): per student, words started, words learned (next review 21
  days or more away, Anki's "mature"), reviews in the last 7 days, last review.

### 11.3 Pictures for words (proposal, not built)
A picture of the example sentence helps a word stick, but it has to be made outside MakonBook, beside the
words: Claude can't draw pictures; an image model can (e.g. OpenAI's gpt-image-1, Google's Imagen, or
Stable Diffusion run on our own). Notes for that work: abstract words (*ubiquitous*, *candid*) need a scene
from the example sentence, not the word, and a misleading picture teaches the wrong meaning, so each picture
needs a person's look before it ships; one consistent style; no text in the picture (models misspell);
small files (512 px WebP, ~30–60 KB, so ~1,200 pictures are ~50 MB, downloaded per list). MakonBook already
takes them: the `image` field above.

## 12. Parents (phase 5, built)
**No parent accounts.** HolliHop's numbers don't say whose phone a number is (the same number is often on
the student and the parent), so a parent follows a child with the child's **parent code**: 5 digits, one per
student, permanent. Read only: the child's classes with schedule and teachers, and attendance. Parents can
report a problem to the office. Parents' screens are in **Uzbek (Latin) or Russian**, as the parent chooses;
the rest of the app stays English.

### 12.1 Codes
- `parent_codes`: one per student (`^[0-9]{5}$`, unique), from `crypto/rand`. The worker gives codes to
  students without one every hour; `GET /v1/me/parent-code` (students) gives one on first use. A student sees
  theirs in the app's Profile; admins look codes up in the panel (`GET /v1/admin/parent-codes?q=`, by name,
  students of their branches).
- A code works only while the student is active. A student who leaves: their parents' sessions stop working
  (`401`), and the app forgets that child.

### 12.2 Signing in
- `POST /v1/parent/login {code, device_id}` → a random 32-byte token (stored as its SHA-256 in
  `parent_sessions`) and the child. One token per child; the app keeps them in secure storage, several children
  per phone (at most 5). Sessions last 180 days after the last use; `POST /v1/parent/logout` ends one.
- Tokens open only `/v1/parent/*`; students' and teachers' tokens don't open it, and parents' tokens open
  nothing else.
- **Guessing** (100,000 codes, a few thousand used): 10 tries per network and 5 per phone in 15 minutes
  (`429 rate_limited`), and 50 wrong codes in an hour sends one Telegram alert naming the networks with 5 or
  more. Accepted risk: someone on many networks could still find *a* code over time; what it shows is a child's
  classes and attendance, not contact details. If a code leaks, changing it is a later addition.

### 12.3 What parents see
- `GET /v1/parent/child`: the child and their groups, as in `/v1/me` (schedule, rooms, teachers).
- `GET /v1/parent/attendance`: the table the student sees (`/v1/me/attendance`).
- The app: the next class, a card per group with its schedule and attendance (one tap: every lesson), the
  child switcher, the language, add or remove a child. A student can still sign in on a parent's phone (a
  shared phone); signed out, the phone goes back to the parent's screens.

### 12.4 Reports
- `POST /v1/parent/reports {message ≤ 2000, lang: uz|ru, group_id?}` (a class of the child, or general) →
  `parent_reports`, and a Telegram message to the admins' chat (student, class, branch, language, the text, a
  link to the panel). `GET /v1/parent/reports`: this phone's reports with status and answer (another phone of
  the same child doesn't see them).
- Admin panel → **Parents** (`GET /v1/admin/parent-reports?status=open|handled&office_id=`): reports about
  students of the admin's branches, newest first; *Mark handled* with an optional answer the parent sees
  (`POST /v1/admin/parent-reports/{id}/handled {reply ≤ 1000}`).

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
-- built (phase 3, step 5): see 9.5
practice_test_versions(test_id FK, version, content_hash, package jsonb, bytes, published_at, PRIMARY KEY(test_id, version))
practice_published_images(file PK, content_type, bytes, width, height)
practice_attempts(id uuid PK (the device's), student_client_id FK, (test_id, test_version) FK, sections text[],
                  started_at, finished_at, answers jsonb, scores jsonb, received_at)
-- built (phase 4): see 11
vocab_lists(id uuid PK, slug UNIQUE, title, position, removed_at)
vocab_words(id uuid PK, list_id FK, position, word, part_of_speech, definition, example, image, removed_at,
            UNIQUE(list_id, lower(word)))
vocab_images(file PK, content_type, bytes, width, height)
vocab_deck(version, built_at)                          -- one row
vocab_reviews(seq PK, id uuid UNIQUE (the phone's), student_client_id FK, word_id FK, rating, reviewed_at,
              duration_ms, state, due, stability, difficulty, scheduled_days, received_at)
```

## 16. Hosting & data
- The existing MakonBook VPS (Hetzner, DE; 2 vCPU, 3.7 GB RAM), shared with the Django site, the leaderboard, sat-makon.uz and school28. Guide: `deploy/DEPLOY.md`.
- **Edge:** the existing `makonbook-nginx` container owns :80/:443 and terminates TLS; `api.makonbook.uz` gets its own server block and its own Let's Encrypt lineage (the shared SAN cert is not touched). The API trusts `X-Forwarded-For` from it (`TRUST_PROXY=true`).
- **Containers:** `makonbook-api` (serve) and `makonbook-api-worker` (sync) from `nazacd/makonbook-api`, built by GitHub Actions and pulled from Docker Hub like the other services; on the shared `sat_makon_network`, no published ports, read-only filesystem.
- **Database:** the shared `db` container (Postgres 16, own Compose project `/opt/sat-makon/postgres`). MakonBook has its own database `makonbook_api` and a **non-superuser** role `makonbook_api`; no access to the other apps' databases. Dev and CI use Postgres 16 to match.
- **Security:** the server audit (2026-10) found Postgres published to the internet with a weak superuser password under active brute force. Fixed: Postgres moved to its own Compose project bound to 127.0.0.1, superuser password rotated, no signs of intrusion. Still recommended: Hetzner Cloud Firewall (inbound 22/80/443).
- **Backups:** nightly `pg_dump` of every database in `db` (there were none before), 14-day retention on the server; copy off-site (Hetzner Storage Box) and **test a restore** once a month.
- Data minimisation: only fields listed above; no birthdays, no parent data (parents are anonymous: a code and a device id).
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
GET  /v1/practice/tests                catalog: open (published version) or locked (progress)
GET  /v1/practice/tests/{code}/package package of a version + signed image links (6 h)
POST /v1/practice/attempts             idempotent by client UUID; graded on the server
GET  /v1/me/practice/attempts          the student's attempts
GET  /v1/teacher/groups/{id}/practice  a group's students and their attempts

# vocabulary (phase 4)
GET  /v1/vocab/deck?have=              every list and word (or "unchanged")
GET  /v1/vocab/lists/{slug}/images     signed image links (6 h)
POST /v1/vocab/reviews                 the phone's review log, 200 at a time, idempotent by id
GET  /v1/me/vocab/reviews?after=       the student's reviews, to rebuild a phone
GET  /v1/teacher/groups/{id}/vocab     a group's progress

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
- **Practice tests** (built: phase 3, step 6; the mobile repo's AGENTS.md): a Practice tab — students download a
  published version (package + images, stored on the phone) and take it offline: the full test or one section,
  modules in order with Bluebook timing (32/35 min), mark for review, strike out choices, the Desmos calculator in
  Math; progress is saved as they go and the clock stops while they're away. Finished attempts are graded on the
  phone with the package's keys and scales (the same rules as the server), kept per person, and uploaded when
  online; results show the estimated scores and every question with its explanation. Teachers get the Practice tab
  with their groups and each student's attempts. The runner reuses the placement test's components (choices,
  grid-in, timer, navigator, calculator) rather than one shared engine; the placement flow is unchanged.
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
(built). Auto-approval of green questions, editors' Approve, the fix step (built). 4. The Telegram group vote
(Bluebook-style images, Approve/Deny) (built). 5. Unlocking tests: publishing, catalog, download, attempts
(built). 6. Mobile: the Practice tab, offline test runner, results and review, teachers' group view (built).

**Phase 4 — vocabulary** (built: import, deck, FSRS on the phone, review log sync, teachers' view). **Phase 5 — parents** (built: codes, read-only child view in Uzbek/Russian, reports to the panel and Telegram). Later: Telegram login/reset, push notifications.

## 20. Open items
None at the moment — all earlier open items are recorded as D12–D17. Deferred on purpose: stricter device policy (D14), substitute-teacher handling without a HolliHop change (D12), Telegram, push notifications.
