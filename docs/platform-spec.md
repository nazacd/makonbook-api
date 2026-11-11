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

Access (except placement) is limited to people who are active in HolliHop, and to students who signed up
themselves on the web (section 21: they see some practice tests and the vocabulary, no classes, until they
enrol).

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
| D22 | **Sign-up is open to everyone, for student accounts only**; teachers and support teachers come only from HolliHop. A new account proves a phone number by **SMS code** (Eskiz.uz); **Google sign-in** exists too. Phone + password login is unchanged. | 2026-11. See 21. |
| D23 | Self-registered students are **students like the others**, in `students` (`source = 'self'`, ids −1000001 and down). When the same verified phone shows up in HolliHop as a **new** student, the sync **re-keys their row to the HolliHop ClientId**: one account, keeping everything they did. A person **already stored** from HolliHop who gets that phone is **not merged**: admins see a data-quality issue. | Merging two histories is left to people (an open item). |
| D24 | A **sign-up whose verified phone already signs in an enrolled person** (a HolliHop student or teacher) makes no account: the client shows whose it is ("Diyora A. — is this you?") and, on yes, sign-up signs into that account, sets the password chosen and links Google. A number a **registered** student already has refuses sign-up ("This phone number already exists": sign in or reset the password). | Proving the phone is proving the account (as a reset). Changed 2026-11-05 (was: any existing account was signed into without asking). |
| D25 | People may **set their own name**; the sync never overwrites it. They see it; teachers, admins and parents keep seeing HolliHop's. Groups, classes and attendance stay HolliHop-only. | |
| D26 | **Registered** (not yet enrolled) students see the mock tests **DAY1–DAY10** only; the rest of practice (statistics, Ask later, reports, goals) and the vocabulary are open to them. Enrolled students see everything, as before. | `practice_tests.open_to_all`. |
| D27 | Registered students **may sign in to the phone app** (no classes; DAY1–DAY10 open). | For now; **revisit** (the app was built for SAT MAKON's students). |
| D28 | **Sign-up starts with an identity**: an **email** proved by a 6-digit code sent to it, or **Google**. The **phone** (SMS code) comes second, verified *with* that identity, and only then is the account made or linked. Each identity can show the owner of **one phone at most** (`identity_spent` after). People can sign in with their email too. Phone-first sign-up is gone. | 2026-11-06. Stops a phone number from being looked up without an email or Google account behind it; only the web used phone-first sign-up. |
| D29 | **Telegram** is a third identity (the Login Widget) and a **free way to prove the phone** next to SMS: the sign-in bot (its own bot) asks for the person's **own contact**; only +998 numbers. | 2026-11-06. Saves SMS costs; Telegram is widespread in Uzbekistan. |
| D30 | **A phone shared through the bot links that Telegram account** to whoever signs up or resets with it (never taken from someone else active, never replacing another link). A sign-up whose Telegram identity is the **same Telegram account that shared the phone** signs straight into the phone's owner, registered or enrolled. | 2026-11-07, after a free account owner coming back with Telegram was told "already exists". |
| D31 | **Accounts reworked** (supersedes D24, D28–D30): sign in or sign up with an **email code**, **Google** or **Telegram** (its OpenID Connect flow, which can share a **verified phone**) — no phone step at sign-up. An account without a phone is **locked** from the content until it adds one (SMS code or Telegram). **Proving a number someone else has merges the phoneless account into theirs** (no "Is this you?", no "already exists"). Photos are copied, never hotlinked. Telegram's old hash widget and the "share my number" bot are gone. | 2026-11-08, decided by the product owner: proving the number is proof enough. |
| D32 | People **set or remove their own photo**; once they have, the Google/Telegram picture is **never copied over their choice**. | 2026-11-09. |
| D33 | **Roadmaps** (section 22): SAT MAKON's curriculum is **imported from files, never committed**, one version per change, progress **kept by lesson id**. Enrolled students get the courses their **groups' disciplines** teach; registered students choose through **onboarding** and get the **first country free**. | 2026-11-10, for the web. |
| D34 | A roadmap lesson is **done** when its **level check is passed (≥ 70 %)** and **every homework set is finished at any score**; lessons open **one after another in course order**. Level checks and homework are **drawn from the approved bank and graded on the server**, preferring questions the student hasn't had. | 2026-11-10. |
| D35 | Onboarding levels: a section score **below 450 → Foundation, 450–590 → Pre-SAT, 600+ → Advanced**; a total counts half for each section; no score → Foundation. | 2026-11-10. |
| D36 | **University logos** on the roadmap stops are used **with the universities' permission** (confirmed by SAT MAKON) and, as trademarks in a public repo, **never committed**: they are loaded from the server's curriculum folder into the database and served by the API. | 2026-11-10. |
| D37 | **Assignments** (section 23): teachers give their groups **homework, level checks and exams with deadlines**. Every student of the group gets the **same questions**; work after the deadline is taken and **marked late**; an exam is a mock test whose **first finished attempt** counts (retakes allowed); an assigned curriculum level check or homework **counts for the roadmap**. **Every current teacher of a group** may assign to it; content creation stays with the editors (9.4). | 2026-11-08, the product owner. |
| D38 | A group whose discipline names the subject but **no level** takes the level from **HolliHop's group level**; without a subject, no roadmap. | 2026-11-08. |
| D39 | **In-app notifications** (a table, the web's drawer): a new assignment notifies the group's students. Nothing outside the app yet; Telegram later. | 2026-11-08. |
| D42 | **Vocabulary, social** (section 11.4–11.6): word games (Pairs, Unscramble, Fill the gap) whose results are kept on the server and shown to teachers; a monthly **leaderboard** of everyone (names shortened) for all students, and of their own groups for enrolled students only; **students make decks**, private or shared with everyone, moderated by teachers and by students' reports. | 2026-11-11. |
| D41 | **The Library** (section 25): SAT MAKON's own books (PDFs), kept in the private `makonbook-library` repository and imported; **everyone signed in reads and downloads** every book; each reader's page is remembered, and a book shows how many people opened it. No uploads by students. | 2026-11-11. |
| D40 | **A dream university is the main goal** (section 24): about 1500 universities (QS's top 1500, every university in Uzbekistan, the roadmaps' 48), collected in the private `makonbook-universities` repository and imported; QS's rank is shown as QS gives it (the owner's decision, knowing QS's terms); the logos are kept in MakonBook's R2 bucket. | 2026-11-09. |

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
| **Registered student** (section 21) | Signed up themselves (`students.source = 'self'`, ids −1000001 and down), with a verified phone or none yet (then locked from the content, D31); not in HolliHop, no branch, no groups. Always active. Becomes the HolliHop student (**enrolled**) when the sync stores them with the same phone (D23). |
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
- Sync writes **only HolliHop-sourced columns** (names, phone, status, offices, group links). It never touches app-owned columns (password hash, username, email, flags, the names people chose: `first_name_custom`, `last_name_custom`).
- Self-registered students (D23, section 21) are never written, deactivated or counted (the mass-change guard counts HolliHop's students only). **Enrolment:** when a run is about to store a **new** student (ClientId not stored yet) who is active with a valid unique login phone, and a self-registered student has that login phone, the run re-keys that row to the ClientId instead (every foreign key to `students` cascades updates; `auth.MoveStudent` moves the password, username, sessions and Google link, which have none), fills the HolliHop columns and keeps its `activated_at`, so the password and sessions stay valid. A HolliHop person **already stored** (or a teacher) whose number is a self-registered student's isn't merged: the number stays with the self-registered account, the HolliHop person gets no login phone and an open `self_account_conflict` issue on the admins' HolliHop issues page.
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
- **Reset:** `makonbook reset-password student|teacher <id>` (admin-panel button later) forgets the own password, unlocks and ends all sessions. People reset it themselves with a **code sent by SMS** (section 21).
- **Sign-up, SMS codes, Google sign-in, own names:** section 21. Self-registered students have **no default password**: without their own they sign in with Google only.
- **Errors:** unknown account and wrong password both answer 401 `invalid_credentials`; a locked account answers 429 `account_locked` with `Retry-After`.
- **Admins:** HolliHop staff on our access list, signing in with their HolliHop login (D18). Practice-content editors: a list of trusted teachers (D21).
- **Demo accounts (app store review):** `demo.student` and `demo.teacher` with the `DEMO_PASSWORD` setting (`internal/demo`, `makonbook demo [remove]`, refreshed daily by the worker): a fictional student (two classes, eight weeks of attendance, practice results rising, with ups and downs, from 1240 to 1520) and a fictional teacher (the Math class and an "open all day" class, so attendance can be taken at any hour), with ten fictional classmates. It lives in the mirror tables under **negative ids** (−1 … −999999; further down are self-registered students, real people, which the demo's commands never touch), which the sync never touches; admins' lessons, reports and code search leave them out, attendance in demo groups is never queued for HolliHop, and parents' reports about the demo child skip Telegram.

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

### 9.3 Access (D20, D26)
Every active student sees every test; registered students (section 21) see DAY1–DAY10 only. A test is **locked until all its questions are approved**, then it
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

### 9.6 Around the tests (built, for the web app)
- **Question statistics** (`GET /v1/practice/tests/{code}/stats`, students and teachers): for each question of
  a test's latest published version, in the package's order, `answered` (uploaded attempts that took its
  section, in every test and version that has the question; a blank answer is answered and wrong) and
  `correct` (graded right with the keys of the version the attempt used, as its scores). The demo account's
  made-up attempts don't count; registered students' do. Counted in memory and again every 5 minutes at most (`practice.StatsTTL`);
  an unknown or locked test is `404 test_not_found`.
- **Ask later** (`parked_questions`): a question a student parks during a test to look at later. The device
  makes the id; `PUT /v1/me/parked/{id}` creates or replaces it (idempotent; another student's id is `404`):
  the test, version and place, and the question's id, which must be the one at that place in that published
  version; a folder (trimmed, 1–40 characters, `Unsorted` when blank), why they were stuck (`didnt_understand`,
  `didnt_know_start`, `lost_partway`, `ran_out_of_time` or none), a note (≤ 1000), "the fix in one line" once
  they understand it (≤ 300), and `waiting` or `sorted`; a wrong value is `422 invalid_parked`.
  `GET /v1/me/parked` lists them newest first, `DELETE /v1/me/parked/{id}` removes one. Teachers see their
  group's current students' ones (`GET /v1/teacher/groups/{id}/parked`, the same check as its practice).
- **Issue reports** (`practice_reports`): the test screen's "Report an issue" (`POST /v1/practice/reports`,
  students): the device's id (a repeat answers `200` with it, a new one `201`; another student's id is
  `409 report_id_taken`), the question by its place as above, `question` or `technical`, a reason
  (`answer_key_wrong`, `typo`, `unclear`, `image_broken`, `formatting`, `other`) and/or a text (≤ 2000); a
  wrong value is `422 invalid_report`. Any admin sees them (`GET /v1/admin/practice/reports?status=open|resolved`,
  newest first, 200 at most, with the student and the test's title) and resolves or reopens one
  (`PUT /v1/admin/practice/reports/{id} {status}`; resolving keeps who resolved it first); the panel has no page
  for them yet. No Telegram message: there is no editors' chat (the review bot's group is the teachers' vote).
- **Goals** (`student_goals`, one row per student), for the dashboard: a Math and a Reading and Writing target
  (200–800, steps of 10) and the exam date, each optional. `GET /v1/me/goals` (all null until set, with
  `target_total` when both targets are set); `PUT /v1/me/goals {target_math, target_rw, exam_date}` replaces
  them; out of range, or an exam date before today (Tashkent) other than the one stored, is `422 invalid_goals`.

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

### 11.4 Word games (web, built)
Practice on top of the flashcards, in the web app: **Pairs** (join five words to their meanings, two boards
against the clock, 5 s added for each wrong pair), **Unscramble** (the meaning shown, the word built from its
mixed letters, three hints a round), **Fill the gap** (the example sentence without the word, four choices).
They draw from the lists turned on, the shakiest words first, and **never write to the review log**: only the
flashcards schedule a word. Each finished round is sent (`POST /v1/vocab/games`, idempotent by id;
`vocab_game_results`) with its score, mistakes, time and the words missed; the student's bests come back from
`GET /v1/me/vocab/games` on every device. Teachers see each student's games this week and points this month in
the group's vocabulary, and the latest rounds with the words missed (`GET /v1/teacher/groups/{id}/vocab/games`).

### 11.5 Leaderboard (built)
A month's board (school time), worked out when asked: **a point for each word reviewed on a day** (however
often: rating a word Again ten times is still one point) and **2 points for each right answer in a game**, a
day's games adding at most 100. `GET /v1/vocab/leaderboard?scope=all` is everyone with points, names shortened
("Madina K.") with their photo; `scope=groups` is the students sharing a group with the viewer now, with full
names, for **enrolled students only** (free accounts have no group: 403 `enrolled_only`). The first 50 and the
viewer's own place.

### 11.6 Students' decks (built)
A student makes decks of their own words (`vocab_user_decks`, `vocab_user_words`; at most 30 decks, 200 words
each; word and meaning required, part of speech and example optional). A deck is **private until shared**; a
shared one (5 words or more) is in the **community** for anyone signed in to look inside and **add** to their own
decks (`vocab_deck_adds`: the owner's later edits reach them). Their words are studied like SAT MAKON's: their
ids go in the review log (`vocab_reviews.word_id` no longer has to be one of `vocab_words`; the server takes
reviews only of words the student may study). A word keeps its id when edited (matched by id or spelling), so
its reviews follow it; a word taken out is marked removed. **Moderation:** any student can report a community
deck with a reason; **three open reports hide it** until a teacher looks. Teachers see the hidden and reported
decks (`GET /v1/vocab/decks?moderation=1`, with the reasons), and **keep** one (closing its reports) or
**hide** one with a reason its owner sees. A hidden deck leaves the community and everyone's decks but its
owner's. The phone app shows neither games nor decks yet; reviews of deck words that reach it are ignored.

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

**Redesign (2026-11, the web's design system)**: the panel takes sat.makonbook.uz's tokens, type and building blocks (Nunito, light and dark with a toggle, the floating sidebar panel with grouped sections, cards, pills, segmented tabs, the reference's table style), copied into `web/admin` and kept in step with makonbook-web by hand; the question editor keeps its logic under the new skin. New page **Issue reports** (9.6): open and resolved reports, newest first, with the student, the test and place, kind, reason and text; resolve and reopen; editors open the question in the editor.

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
-- built: see 9.6
parked_questions(id uuid PK (the device's), student_client_id FK, test, version, subject, module, position,
                 question_id, folder, reason NULL, note, fix, status (waiting|sorted), created_at, updated_at)
practice_reports(id uuid PK (the device's), student_client_id FK, test, version, subject, module, position,
                 question_id, kind (question|technical), reason NULL, text, status (open|resolved), created_at,
                 resolved_at, resolved_by)
student_goals(student_client_id PK FK, target_math NULL, target_rw NULL, exam_date NULL, updated_at)
-- built (accounts): see 21
students.source ('hollihop' | 'self'), students/teachers.first_name_custom, last_name_custom NULL
            -- self-registered students' ids: sequence self_student_ids, -1000001 and down;
            -- every FK to students is ON UPDATE CASCADE (enrolment re-keys)
practice_tests.open_to_all                           -- DAY1–DAY10: registered students take them
phone_codes(id, phone, purpose (signup|reset), salt, code_hash NULL, ip, tries, created_at, expires_at, used_at)
email_codes(id, email, salt, code_hash, ip, tries, created_at, expires_at, used_at)
credentials.email UNIQUE NULL                        -- lower case; sign-in identifier
auth_tickets.identity (a phone ticket's identity ticket hash), revealed_phone (an identity's one shown phone)
telegram_links(telegram_id PK, person_kind, person_id, username, created_at, UNIQUE(person_kind, person_id))
telegram_phone_checks(…)                             -- unused since D31
photos(id PK, content_type, data, created_at); credentials.photo_id
credentials.photo_chosen                             -- the person set or removed their photo (D32)
oauth_states(state_hash PK, purpose (signin|verify), verifier, person_kind, person_id, return_path, created_at,
             expires_at, used_at)                    -- Telegram's OpenID Connect flow
google_links: several per person (D31)
auth_tickets(token_hash PK, purpose (signup|reset|google|google_signup), phone NULL, google_sub NULL, email,
             first_name, last_name, created_at, expires_at, used_at)
google_links(sub PK, person_kind, person_id, email, created_at, UNIQUE(person_kind, person_id))
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
PUT  /v1/me/profile                    {first_name, last_name} — own names (21)

# accounts (21)
POST /v1/auth/email/start              {email} → {resend_in, expires_in}; a code by email
POST /v1/auth/email/verify             {email, code} → {status: signed_in, token, expires_at, user} | {status: new, email_ticket}
POST /v1/auth/signup                   {email_ticket, first_name, last_name, password} → login (no phone yet)
GET  /v1/auth/google/start?return=     302 to Google (PKCE, state cookie)
GET  /v1/auth/google/callback          302 to WEB_ORIGIN/auth/google#ticket=…&return=… or #error=…
POST /v1/auth/google/ticket            {ticket} → login (linked, by email, or a new account)
POST /v1/auth/telegram/start           {purpose: signin|verify, return?} → {url} (verify: bearer)
GET  /v1/auth/telegram/callback        302 to WEB_ORIGIN/auth/telegram?ticket=…&purpose=…&return=… or ?error=…
POST /v1/auth/telegram/ticket          {ticket} → login (linked, by phone, or a new account)
POST /v1/me/phone/start                {phone} → {resend_in} (no phone yet)
POST /v1/me/phone/verify               {phone, code} → login (the phone set, or merged into its owner)
POST /v1/me/phone/telegram             {ticket (verify)} → login (as /v1/me/phone/verify)
POST /v1/auth/phone/start              {phone, purpose: reset} → {resend_in, expires_in}; SMS code
POST /v1/auth/phone/verify             {phone, code, purpose: reset} → {ticket}
POST /v1/auth/reset                    {ticket, password} → login
GET  /v1/photos/{id}                   a profile photo (no token, long cache)
PUT|DELETE /v1/me/photo                own photo (raw JPEG/PNG/WebP ≤ 512 KB) → profile

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
GET  /v1/practice/tests/{code}/stats   per question of the latest version: answered, correct (9.6)
POST /v1/practice/attempts             idempotent by client UUID; graded on the server
GET  /v1/me/practice/attempts          the student's attempts
GET  /v1/teacher/groups/{id}/practice  a group's students and their attempts
GET  /v1/me/parked                     the student's parked questions ("Ask later", 9.6)
PUT|DELETE /v1/me/parked/{id}          park or change one (idempotent by client UUID); remove one
GET  /v1/teacher/groups/{id}/parked    a group's students' parked questions
POST /v1/practice/reports              "Report an issue" on a question; idempotent by client UUID
GET|PUT /v1/me/goals                   the student's SAT targets and exam date (9.6)

# roadmaps (22)
GET|POST /v1/me/onboarding             a registered student's answers; POST sets goals and roadmaps
GET  /v1/me/roadmaps                   the student's roadmaps; GET …/{id} with units and lessons
GET  /v1/me/roadmaps/{id}/lessons/{lessonId}              a lesson, its level check and homework
POST /v1/me/roadmaps/{id}/lessons/{lessonId}/assessments  start or continue one
GET  /v1/me/assessments/{id}           PUT …/answers saves; POST …/submit grades
GET  /v1/logos/{slug}                  a stop's university logo (no sign-in)

# assignments and notifications (23)
GET  /v1/me/assignments                the student's homework, level checks and exams, with their standing
POST /v1/me/assignments/{id}/start     an assigned homework or level check: the open assessment, or a new one
GET  /v1/me/notifications              newest first, with the unread count; PUT …/read {ids} | {all}
GET|POST /v1/teacher/assignments       a teacher's groups' assignments (?group_id=); give one (any teacher of the group)
POST /v1/teacher/assignments/draw      questions for a set, not saved: the preview, and a set after a swap
GET|PUT|DELETE /v1/teacher/assignments/{id}   with its questions; change or close; delete while nobody started
GET  /v1/teacher/assignments/{id}/results     per student and per question; …/{client_id}: one student's work
GET  /v1/teacher/questions?subject=&skill=&difficulty=&q=   approved questions to swap in (previews)
GET  /v1/teacher/taxonomy              domains and skills with approved counts per difficulty
GET  /v1/teacher/curriculum            the imported courses, lessons and their level check and homework specs
GET  /v1/teacher/groups/{id}/roadmap   a group's course(s) and every student's progress

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
GET  /v1/admin/practice/reports?status=   students' issue reports (9.6); PUT …/{id} {status} resolves or reopens
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

**Phase 4 — vocabulary** (built: import, deck, FSRS on the phone, review log sync, teachers' view). **Phase 5 — parents** (built: codes, read-only child view in Uzbek/Russian, reports to the panel and Telegram). **Accounts** (built, for the web app: sign-up and password reset by SMS code, Google sign-in, own names, registered students linked at enrolment; section 21). Later: Telegram login/reset, push notifications.

## 20. Open items
- **Merging** a HolliHop person already stored with a self-registered account that has their phone
  (`self_account_conflict`, D23): there is no tool yet; for now admins correct the number in HolliHop, or a
  developer merges the two by hand.
- **Registered students in the phone app** (D27): allowed for now; decide whether the app should offer them
  anything, or send them to the web.

Deferred on purpose: stricter device policy (D14), substitute-teacher handling without a HolliHop change (D12), Telegram, push notifications.

## 21. Accounts (built; reworked by D31)

Sign-up is open to everyone, for **student accounts only** (D22); teachers and support teachers come only
from HolliHop. Phone + password login (section 7) is unchanged, and also takes a username or an email. Built
in `internal/auth` (codes, tickets, email, Google, Telegram, adding a phone and merging, photos, own names),
`internal/people` (registered students, the enrolment link, the merge's rows), `internal/platform/sms` (the SMS
gateway) and `internal/platform/mail` (SMTP).

**Ways in (D31):** an email code (`/v1/auth/email/*` → signed in, or `new` → `/v1/auth/signup`), Google
(`/v1/auth/google/*`) or Telegram (`/v1/auth/telegram/*`). Google and Telegram always answer a session: the
linked account, else a match (Google by verified email, Telegram by the shared phone), else a new registered
student. A phone is added afterwards (21.4) unless Telegram shared it.

### 21.1 Who is who
- **Registered** student (`Profile.status = "registered"`): signed up themselves; a row in `students` with
  `source = 'self'`, an id from the sequence `self_student_ids` (−1000001 and down, clear of HolliHop's
  positive ClientIds and of the demo's −1 … −999999), `phone_raw` = `login_phone` = the verified E.164 phone
  (or NULL until they add one), `hh_student_id` = the same negative id, no branch, no HolliHop status. Always
  active; no groups.
- **No phone yet** (`Profile.phone = null`): the content answers **`403 phone_required`** — practice catalog,
  packages, attempts, statistics, Ask later, reports, goals, the vocabulary deck and reviews
  (`auth.RequireContent`). `/v1/me`, profile, password, username, logout, adding a phone and the app config
  work. Enrolled people are never locked (a HolliHop person whose phone is unusable still has the app).
- **Enrolled**: everyone in HolliHop, teachers too. A registered student becomes enrolled at the first sync
  that stores them from HolliHop with the same verified phone (D23, 6.2): same account, same password,
  username, Google and Telegram links, sessions, attempts, vocabulary, Ask later, reports and goals.
- Practice for registered students with a phone (D26): `practice_tests.open_to_all` (DAY1–DAY10). The
  catalog shows the others locked with `enrolled_only: true`; their package and attempts answer `403
  enrolled_only`. Enrolled students and teachers: no change.
- They may sign in to the phone app (D27).

### 21.2 Codes by email and SMS
- **Rules** (both): 6 digits from `crypto/rand`, stored as SHA-256 with a random salt, good for **10 minutes
  and 5 tries**; only the latest code for an address or phone (and purpose) counts; per address or phone
  **one a minute and 5 a day**, per network **20 an hour** (`429 rate_limited`, `Retry-After`). Wrong: `422
  invalid_code` (tries left in the message); the 5th wrong one spends the code (`429 too_many_tries`);
  expired, used, replaced or never sent: `410 code_expired`.
- `POST /v1/auth/email/start {email}` → `{resend_in: 60, expires_in: 600}` (trimmed, lower-cased, a plain
  `name@domain.tld`, else `422 invalid_email`; SMTP: `SMTP_HOST` (`log` in development), `SMTP_PORT` 587/465,
  `SMTP_USERNAME`, `SMTP_PASSWORD`, `MAIL_FROM`; off: `503 email_unavailable`).
- `POST /v1/auth/email/verify {email, code}` → `{status: "signed_in", token, expires_at, user}` when someone
  active has the address (also the way back from a forgotten password), else `{status: "new", email_ticket}`
  (single-use, 30 minutes). The email is kept in `credentials.email` (unique, lower case).
- `POST /v1/auth/signup {email_ticket, first_name, last_name, password}` → a login: a registered student with
  the email, names (1–60 characters, `422 invalid_profile`) and password (section 7, `422 weak_password`), no
  phone. `409 email_taken` if someone took the address meanwhile.
- SMS (Eskiz.uz, `SMS_PROVIDER`, `SMS_TEXT` with `{code}`, `ESKIZ_*`): Uzbek numbers only (`422
  invalid_phone`). **Reset**: `POST /v1/auth/phone/start {phone, purpose: "reset"}` answers the same whether or
  not anyone signs in with the number (the code goes only to a number that has an account, in the
  background); `POST /v1/auth/phone/verify {phone, code, purpose: "reset"}` → `{ticket}` (15 minutes);
  `POST /v1/auth/reset {ticket, password}` sets the password, unlocks, ends other sessions and signs in (`404
  no_account` if nobody has the number any more). `purpose: "signup"` is gone (`400`).

### 21.3 Google and Telegram
- **Google** (OAuth code flow with PKCE, run by the API; `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET`,
  `WEB_ORIGIN`): `GET /v1/auth/google/start?return=` (state in a signed HttpOnly cookie) → Google →
  `GET /v1/auth/google/callback` (claims: issuer, audience, expiry, a verified email; the token comes straight
  from Google over TLS) → `${WEB_ORIGIN}/auth/google#ticket=…&return=…` (10 minutes) or `#error=google_denied |
  google_failed` → `POST /v1/auth/google/ticket {ticket}` → a login: the linked account; else whoever active has
  the email, with Google linked; else a new registered student with no phone (names, the email, the photo,
  Google linked). Several Google accounts per person (`google_links`).
- **Telegram** (OpenID Connect, https://core.telegram.org/bots/telegram-login; `TELEGRAM_CLIENT_ID`,
  `TELEGRAM_CLIENT_SECRET` from @BotFather → the bot → Login Widget, `WEB_ORIGIN`):
  - `POST /v1/auth/telegram/start {purpose: "signin"|"verify", return?}` → `{url}` at oauth.telegram.org
    (scope `openid profile phone`, PKCE S256). `verify` needs the bearer of a person without a phone (`409
    phone_already_set`). State, verifier, return path and (verify) the person are kept server-side
    (`oauth_states`, by the state's SHA-256, 10 minutes, single use).
  - `GET /v1/auth/telegram/callback`: trades the code at `oauth.telegram.org/token` (Basic auth with the
    client id and secret, the verifier), checks the ID token's signature with Telegram's JWKS (RS256 or ES256),
    issuer `https://oauth.telegram.org`, audience (the client id), expiry; reads `id` (the Telegram user id),
    names, `preferred_username`, `picture` and `phone_number` (digits → E.164, any country; absent when the
    person declined). The claims go under a single-use 5-minute ticket; 302 to
    `${WEB_ORIGIN}/auth/telegram?ticket=…&purpose=…&return=…`, or `?error=telegram_denied | telegram_failed`.
  - `POST /v1/auth/telegram/ticket {ticket}` (signin) → a login: (a) the linked account (and the shared phone
    becomes theirs if they have none and nobody else has it); (b) else whoever active has the shared phone
    (registered or enrolled, student or teacher), Telegram linked if they have none; (c) else a new registered
    student (names, the phone if shared, the photo, Telegram linked). `must_change_password` as at any login.
  - `POST /v1/me/phone/telegram {ticket}` (verify, the same person's) applies the shared phone (21.4); no phone
    shared: `422 telegram_no_phone`.
  - Links (`telegram_links`, one each way) are never taken from someone else active or replace another.

### 21.4 Adding a phone, and merging
- `POST /v1/me/phone/start {phone}` → `{resend_in}`; `POST /v1/me/phone/verify {phone, code}` → a login; or
  Telegram (21.3). Only a registered student without a phone; anyone else: `409 phone_already_set`.
- **Applying phone P for person U** (with Telegram account T for the Telegram way), in one transaction:
  - nobody else active has P → it becomes U's; T is linked to U if free and U has none;
  - an active person O ≠ U has P (registered or enrolled, student or teacher) → **U is merged into O**: U's
    Google accounts move to O; U's email replaces O's; U's Telegram link moves if O has none (else goes); T is
    linked to O if free and O has none; U's photo goes to O if O has none; U's attempts, reviews, parked
    questions, reports and (if O has none) goals move to O when O is a student (deleted when a teacher); U's
    credentials and sessions end and U is deleted. The answer is a login for O (`must_change_password` as at
    any login: an enrolled O still on the default password must replace it).
  - No "Is this you?" and no "already exists" (D31): proving the number is proof enough.
- **Passwords**: `POST /v1/auth/password` takes an empty or missing `current_password` when `has_password` is
  false (Google, Telegram, or never set), so passwordless people set one in Profile.

### 21.5 Photos (D32)
- **Their own**: `PUT /v1/me/photo` with the raw image as the body (everyone signed in: students and teachers,
  with or without a phone, even on the default password). JPEG, PNG or WebP by its bytes
  (`http.DetectContentType`, not the header), decoding (`image.DecodeConfig`, at most 4096 px a side; not
  necessarily square), at most 512 KB (`413 photo_too_large`; otherwise `422 invalid_photo`); 20 an hour per
  person (`429 rate_limited`). It is stored under a new random id and the previous photo row is deleted (old
  URLs are cached as immutable). `DELETE /v1/me/photo` removes it. Both answer the profile and set
  `credentials.photo_chosen`.
- **Copied**: on Google or Telegram sign-in or creation, and when Telegram verifies a phone (for whoever then
  has it, the owner after a merge), a person without a photo who never chose one (`photo_chosen` false) gets a
  copy of the picture (https, at most 512 KB, JPEG/PNG/WebP by its bytes; skipped and logged on any failure). The
  copy is made before the answer is built, so the answer's profile has it. In a merge, U's photo goes to O when
  O has none and never removed theirs (an uploaded photo stays chosen); otherwise it is deleted.
- Kept in `photos` (the database; R2 would still need the API to serve it, as pages load images from their own
  origin only) and served at `GET /v1/photos/{id}` (random id, no token, `Cache-Control: public,
  max-age=31536000, immutable`). `Profile.photo_url` is that relative path, or null. The providers' CDNs are
  never hotlinked.

### 21.6 Own names
- `PUT /v1/me/profile {first_name, last_name}` (students and teachers): the names the person sees for
  themselves from then on — `/v1/me` and every sign-in answer show `custom ?? HolliHop` — stored in
  `first_name_custom` / `last_name_custom`, which the sync never writes (D25). null clears one, back to
  HolliHop's — for HolliHop people only; a registered student needs both. Teachers', admins' and parents'
  views, and Telegram messages, keep HolliHop's names (staff work from HolliHop).
- `Profile` gains `status` (`enrolled` | `registered`), `phone` (the login phone, E.164, or null), `email`
  (theirs, or null), `google` and `telegram` (an account is linked), `has_password` (their own password, not
  the default one) and `photo_url`.
- `GET /v1/app/config` gains `signup` (email codes), `reset` (SMS), `email`, `google` and `telegram`
  (booleans); the web shows those options only when true.

## 22. Roadmaps (built)

SAT MAKON's curriculum as a route for each student: a **course** per subject and level (`math-foundation`,
`math-pre-sat`, `math-advanced`, `english-…`), each a run of **countries**, each country a few **units** (a
university stop), each unit a few **lessons**. A lesson has notes, examples and common mistakes to study, a
**level check** and **homework sets**, drawn from the approved question bank and graded on the server. Built in
`internal/roadmap` (the curriculum, assignment, progress, onboarding, assessments) with the bank's side in
`internal/practice/bank.go`. The web shows it; the phone app doesn't yet.

### 22.1 The curriculum
- It is **SAT MAKON's own material and is never committed** (the repo is public): one JSON file per course,
  `{subject, level, countries: [{id, name, units: [unit ids]}], units: [{id, title, domain, summary, stop:
  {university, city, country}, lessons: [{id, title, skills, prerequisites, minutes, objectives, notes,
  examples: [{problem, steps, answer}], common_mistakes, level_check: {questions, mix: {easy, medium, hard},
  skills}, homework: [{title, skills, difficulty, questions}]}]}]}`. Notes and examples are in the content
  format (9.2). Skills are the digital SAT's names (`practice.Taxonomy`); tests use a made-up course.
- `makonbook curriculum import <file or folder>…` checks everything before storing anything: unique unit and
  lesson ids, the countries listing every unit once and in the units' order, each level check's mix adding up
  to its questions, homework with a title, skills, a difficulty and questions, skills never empty, no course
  twice, and **no lesson id in two courses** (stored ones included). Each course whose content changed becomes
  a new row in `curriculum_versions` (jsonb, with its hash); the latest is live at once. **Progress is keyed
  by lesson id**, so a new version keeps it.
- Then (and on `makonbook curriculum coverage`) it prints the **coverage report**: for every level check and
  homework set, the approved questions of its skills at each difficulty asked (`have/want`), at any
  difficulty, `SHORT` where the bank can't fill it as asked, and skills no approved question has.
- Skills match the bank's in any case; a name the bank splits ("Command of Evidence") stands for each of its
  parts ("… (Textual)", "… (Quantitative)").
- Country ids map to ISO codes for flags: uzbekistan uz, south-korea kr, japan jp, singapore sg, turkey tr,
  germany de, switzerland ch, united-kingdom gb, canada ca, united-states and usa us; any other, null.

### 22.2 Who follows which course
- **Enrolled students**: one roadmap per course their **current groups' disciplines** teach (`source:
  group`, `group_name`): the subject from MATH or ENGLISH (or ENG), the level from FOUNDATION, PRE SAT
  (PRE-SAT, PRESAT) or ADV (ADVANCED), as words. A discipline that names the subject but no level (e.g. "SAT
  MATH") takes the level from HolliHop's group level (`groups.level`, the same words; D38); without a subject,
  or still without a level, none. Several groups of one course give it once. Every unit is theirs.
- **Registered students**: the roadmaps onboarding gave (`roadmap_choices`, `source: onboarding`). **Free**:
  only the **first country's units** are open (`free_units`); the rest are locked `enrolled_only` whatever
  the progress. Once they enrol, their groups decide (their progress comes with them).
- **Teachers**: none. A course never imported gives no roadmap until it is. The list is math first, then by level.
- Behind `auth.RequireContent`: a registered student without a phone gets `403 phone_required` from the
  roadmaps and assessments (onboarding works without one).

### 22.3 Onboarding (registered students)
- `POST /v1/me/onboarding {graduation ("2027" … "2034" | "graduated"), sat_dates: [dates] (empty: not
  decided), current: null | {total: 400–1600} | {math, rw: 200–800}, target: {total, math, rw}, focus: math |
  english | both, schedule: {days: [mon … sun] (≥ 1), minutes: 15 | 30 | 45 | 60}, university_id (optional: the
  dream university, section 24.3)}`. Scores are multiples of
  10 and `target.total = target.math + target.rw`; anything else is `422 invalid_onboarding` with a message.
  Anyone else gets `409 not_needed`.
- It stores the answers (`onboarding`), sets the goals (9.6: the two targets, and `exam_date` = the first SAT
  date from today on, or null) and gives a roadmap per subject in focus, at the level of that section's
  current score: **below 450 Foundation, 450–590 Pre-SAT, 600+ Advanced**; a total counts half for each; no
  score, Foundation. Sending again replaces the answers and those roadmaps; progress stays. It answers
  `{profile, roadmaps}`. `GET /v1/me/onboarding` reads the answers back (404 before).
- `Profile.onboarded`: registered students, whether they have sent it; always true for everyone else.

### 22.4 Progress, open and locked
- The **first lesson** is open; each later lesson opens once **the lesson before it in course order** is done
  (`lock: previous`). A lesson is **done** when its level check was **passed (best ≥ 70 %)** and **every
  homework set was finished, at any score**. A unit is done when its lessons are. Free accounts: units past the
  first country are `locked` / `enrolled_only` (22.2).
- `GET /v1/me/roadmaps` → summaries: counts of lessons and units done, `current` (the first lesson not done,
  even if locked; null when all are), `free_units`. `GET /v1/me/roadmaps/{id}` adds the countries (with
  codes) and the units and lessons with their status, lock, best level check and homework done. 404 when it
  isn't one of theirs.
- `GET /v1/me/roadmaps/{id}/lessons/{lessonId}` → the lesson's content, its level check (mix, pass percent,
  best, attempts newest first, the open one) and homework sets (done, the latest finished attempt, the open
  one). A locked lesson stays hidden: `403 lesson_locked`, or `enrolled_only` past the free limit.

### 22.5 Level checks and homework
- `POST …/lessons/{lessonId}/assessments {kind: level_check | homework, index}` returns the **unfinished** one
  of that kind and set if there is one, else makes one (`roadmap_assessments`, at most one open per set):
  approved questions of the set's skills, the level check's mix (homework: its difficulty), **preferring
  questions the student hasn't had in any roadmap assessment**. In order: unseen at the difficulty asked, at
  the neighbouring difficulty (easy ↔ medium ↔ hard), at any difficulty (unestimated too); then the same among
  those seen; still short, fewer questions (never none: `409 no_questions`). Random within each step;
  ordered easy to hard. A finished homework set can be practised again and stays done; a level check can be
  retaken, with new questions where possible. Locked lessons: 403 as above.
- An open assessment has no keys or explanations. Figures come as signed links (6 h) to the files practice
  publishes (`p/img/`), copied there on first use for questions in no published test (`figure_files`).
- `PUT /v1/me/assessments/{id}/answers {answers}` saves as the student goes (each replaces that question's;
  null clears); `POST …/submit {answers}` saves and **grades exactly like practice** (`PackageQuestion.Correct`):
  `percent` rounded down, `passed` = ≥ 70 for a level check (always for homework), `lesson_done`, and per
  question the answer given, right or not, the correct answer (letter or first accepted form) and the
  explanation; the questions then carry their keys. Either after submitting: `409 finished`. Other people's
  assessments are 404.
- A merge (21.4) moves the registered student's assessments and choices, and their onboarding when the other
  has none.

### 22.6 Tables
```
curriculum_versions (subject, level, version, content jsonb, content_hash, imported_at)   PK (subject, level, version)
onboarding          (student_client_id PK, answers jsonb, updated_at)
roadmap_choices     (student_client_id, roadmap_id, created_at)                           PK both
roadmap_assessments (id uuid, student_client_id, roadmap_id, lesson_id, kind, homework_index,
                     question_ids uuid[], answers jsonb, created_at, finished_at, correct, total, percent)
figure_files        (key PK, file → practice_published_images)                            practice's
university_logos    (slug PK, content_type, body bytea, sha256, updated_at)               22.7
```

### 22.7 University logos (D36)
- Used with the universities' permission; trademarks, so **never committed**. They sit in `logos/` beside the
  curriculum files on the server, one per university, named `<slug>.svg|.png|.webp`, where the slug
  (`roadmap.Slug`) is the stop's university name in lower case, accents folded to ASCII, each run of anything
  but letters and digits a `-`, trimmed ("Queen's University" → `queen-s-university`).
- `curriculum import` loads the logos whose slug is a university of the imported courses: the bytes decide (an
  SVG must be well-formed XML with an `<svg>` root; PNG and WebP must decode), at most 1 MB. A changed file
  replaces the stored one; the same one is left alone; anything else is skipped with its reason. It lists the
  universities still without a logo; `curriculum logos` lists every university's slug and whether it has one.
- `GET /v1/logos/{slug}` (no sign-in): the bytes with their type, `ETag` = the sha-256, `Cache-Control: public,
  max-age=86400`; every answer (404 too) carries `X-Content-Type-Options: nosniff` and `Content-Security-Policy:
  default-src 'none'; style-src 'unsafe-inline'; img-src data:; sandbox`, so an SVG opened on its own runs
  nothing.
- `stop.logo_url` on each unit of `GET /v1/me/roadmaps/{id}`, and `current.logo_url` in the summaries:
  `/v1/logos/{slug}?v=<first 8 of the sha>` (relative, the API's origin; a new logo is a new URL), or null.

## 23. Assignments and notifications (built, for the web)

Teachers give their groups work with deadlines (D37): the reference's Homework, Level Checks and Exams tabs,
fed by SAT MAKON's own bank, curriculum and mock tests. Built in `internal/assignments` (assignments, drawing,
results, notifications) on `internal/roadmap`'s assessments (it draws with `roadmap.Service.Draw`, starts a set
with `StartAssigned` and lays out a group's progress with `GroupProgress`) and `internal/practice`'s attempts and
bank (`ApprovedQuestions` for the swap list). The admin panel has no part in it: **every current teacher of a
group** (on its schedule now or later, as `TeachesGroup`) may give, change, close and delete its assignments and
see its results; another teacher gets `403 forbidden` for an assignment of that group (`404` when there is no
such assignment), and students get `403` from every teacher endpoint.

### 23.1 Kinds
- **Homework**: a fixed, ordered set of 1–60 approved questions, built from a curriculum lesson's homework set
  (`course`, `lesson_id`, `homework_index`) or custom (`subject`, `skills`, `mix`; or questions picked one by one).
- **Level check**: a curriculum lesson's level check (`course`, `lesson_id`), drawn once with the lesson's skills
  and mix.
- **Exam**: a published mock test (`test`), full or one section (`sections`: `english`, `math` or both, a subset
  of the test's).
- Every student of the group gets the **same questions**, so the class can go over them and per-question
  statistics mean something.
- Fields: `title` (1–120 characters; defaults to the lesson's, the set's or the test's), `instructions` (≤ 2000,
  plain text), `opens_at` (default now; may be later), `due_at` (after `opens_at`), `closed_at`.
- As built: the default titles are "<lesson> · Level check", the set's title, "Homework" (custom) and the test's
  title. `subject` is the course's (curriculum), the one sent (custom homework), or an exam's first section in the
  test's order (`english` for a whole test). `skills` and `mix` are kept for the record: the lesson's level check,
  a set's skills with `{its difficulty: its questions}`, or what custom homework sent. A wrong value is `422
  invalid_assignment` with `fields` naming each (`kind`, `title`, `instructions`, `due_at`, `course`, `lesson_id`,
  `homework_index`, `subject`, `skills`, `mix`, `question_ids`, `test`, `sections`, `group_id`); a group the
  teacher doesn't teach is `403`. `PUT` takes every field; `closed: true` keeps the first closing time.
- **Status**: `scheduled` (not open yet), `open`, `overdue` (the deadline passed; work is still taken and marked
  **late**), `closed` (the teacher closed it: no more starts; work in progress can still be finished). Students see
  an assignment once it opens: open and overdue ones, and closed ones they have work on.
- Deleting is allowed while nobody has started (`409 has_work` otherwise): no assessment of it, or for an exam no
  counting attempt by a student of the group now. Its notifications go with it.

### 23.2 Drawing questions (`POST /v1/teacher/assignments/draw`)
- From a lesson's level check, one of its homework sets, or `subject` + `skills` + `mix`: the approved questions
  of the subject with one of the skills (as 22.5 resolves them), as many of each difficulty as asked, from the
  neighbouring difficulty when short, then any; fewer when still short, never none (`409 no_questions`). With
  `group_id`, questions none of the group's students has had (in any roadmap assessment or assignment) come
  first: the questions in any roadmap assessment of the group's current students, and those given to the group
  before. Random within each step; easy to hard. A lesson that isn't in the course, or a set it hasn't, is `404`;
  a group the teacher doesn't teach `403`; a request that asks for nothing (or a mix outside 1–60) `422`.
- With `question_ids` it returns those questions in that order instead: the preview after a swap.
  `GET /v1/teacher/questions?subject=&skill=&difficulty=&q=` lists approved questions to swap in (previews, 30 at
  a time, `total`); `GET /v1/teacher/taxonomy` the domains and skills with approved counts per difficulty;
  `GET /v1/teacher/curriculum` the imported courses with every lesson's level check and homework specs.
- Teachers see questions whole: keys, accepted forms, explanations, figure links (6 h).
- `POST /v1/teacher/assignments` stores `question_ids` as sent (checked: approved, of the subject, no repeats,
  1–60). The set is never drawn again: it is the assignment.

### 23.3 Students (`/v1/me/assignments`)
- `GET /v1/me/assignments`: the assignments of the student's current groups, soonest deadline first, scheduled
  ones included (`status: scheduled` until `opens_at`: the apps show them locked, with when they open), each with its lesson (when from the curriculum), its test (exams) and `mine`: `status` (`new`,
  `in_progress`, `done`), the open or first finished assessment, `finished_at`, `late` (finished after the
  deadline), `percent`/`correct`/`total`, `passed` (a level check at ≥ 70 %; homework once done), the exam's
  `scores` and `total_score`, `tries` and `best`. Registered students and teachers get an empty list.
- `POST /v1/me/assignments/{id}/start` (homework, level check): the student's open assessment for it, else a
  new one with the assignment's questions, as a row of `roadmap_assessments` with `assignment_id` (and the
  lesson, set and course when from the curriculum; `roadmap_id` and `lesson_id` are null for custom homework).
  Then `GET /v1/me/assessments/{id}`, `PUT …/answers` and `POST …/submit` as in 22.5; the assessment carries
  `assignment` (id, title, kind, due date). A finished one may be started again for practice; **the first finished
  one is the result**, later ones count in `tries` and `best`. `409 not_open` before it opens, `409
  assignment_closed` after it is closed, `409 not_an_assessment` for an exam, `404` for a student not in the group.
  An open one is returned even after the assignment closed (work in progress can be finished); `409 no_questions`
  when every question of the set has gone from the bank. A student's `status` is `new`, `in_progress` (an open
  one, nothing finished) or `done`; `late` is against the deadline as it is now, so moving the deadline relabels
  work. Enrolled now means as `GroupStudents` (enrolment dates around today, the student active).
- **Exams** are taken as the mock test itself (the web preselects the sections): the student's attempts on the
  test, finished at or after `opens_at`, whose sections include the assignment's, count; the first finished is the
  result (late after `due_at`), `tries` how many, `best` the best total (one section: its score). Only the assigned
  sections' scores are given. Attempts are uploaded finished, so an exam is `new` or `done`, never in progress;
  attempts still count after the teacher closes it.
- **Roadmap credit** (D37): an assessment with a lesson counts in the student's roadmap like any other, so an
  assigned level check passed at 70 %+ passes the lesson and an assigned homework set finishes it. The roadmap's
  own start (22.5) continues an open assigned one for the same lesson and set rather than drawing another.
- **Times** (D37): `PUT …/answers` and `POST …/submit` take `times` (question id → seconds spent so far,
  0–21600, each replacing that question's) next to the answers; the assessment returns them. The web's runner
  counts active time per question. Whole seconds only; another question's id or a value out of range is `422
  invalid_answers`. The roadmap's own assessments take times too.

### 23.4 Results (`GET /v1/teacher/assignments/{id}/results`)
- Per student of the roster now: status, started and finished, late, the result as above, `tries`, `best`, and
  the assessment or attempt id. `GET …/results/{client_id}` gives the work itself: the first finished assessment
  with keys and explanations, or the exam attempt that counted, with its answers.
- Per question (the set's, or for an exam the assigned sections' questions of the test's latest version, in
  order): `answered`, `correct`, the `answers` given (`A`–`D` and `blank`; a grid-in: `correct`, `wrong`,
  `blank`), `avg_seconds` (null without times). `skills`: wrong and total per skill. Each assignment's `summary`:
  students, done, late, in progress, the average percent (exams: the average score).
- As built: a set's questions are graded with the bank's keys as they are now, and questions gone from the bank are
  left out; an exam's are matched to each counted attempt by their place (section, module, position) in the test's
  latest version and graded with its keys, numbered from 1 over the assigned sections; a question an attempt has
  no answer for counts as blank. Skills come most wrong first, then by name.

### 23.5 Notifications (D39)
- `notifications`: one row per person and event. When an assignment is created, every student enrolled in the
  group then gets one (`kind: assignment`, the title, the deadline, a link to the tab). Nothing is sent outside
  the app yet (Telegram later). As built: the title "New homework: <title>" ("New level check: …", "New exam:
  …"), the body "<group> · Due 12 November, 18:00" in Tashkent time ("<group> · Opens 10 November, 08:00 · Due
  …" for one that opens later), the link `/homework`, `/level-checks` or
  `/exams`. Deleting the assignment deletes them. A merge (21.4) moves a registered student's notifications
  (deletes them when merged into a teacher); there is no foreign key to people.
- `GET /v1/me/notifications`: newest first, 100 at most, with `unread`; `PUT /v1/me/notifications/read {ids}`
  or `{all: true}` marks them read and answers `unread` (another person's ids are ignored; neither is `400
  bad_request`). Teachers have none yet.

### 23.6 A group's roadmap, for its teachers
- `GET /v1/teacher/groups/{id}/roadmap`: the course(s) the group teaches (22.2, with D38's fallback to the
  group's HolliHop level) with every lesson in order, and each student's standing per lesson (`done`, `open`,
  `locked`, the level check passed and its best, homework sets done), as their own roadmap lays it out. A group
  that maps to no course gives an empty list of courses. The students' progress is read in one query.

### 23.7 Tables
```
assignments          (id uuid, group_id → groups, teacher_id → teachers, kind, title, instructions, subject,
                      course, lesson_id, homework_index, test_code, sections text[], question_ids uuid[],
                      skills text[], mix jsonb, opens_at, due_at, closed_at, created_at, updated_at)
roadmap_assessments  + assignment_id → assignments (null for the roadmap's own), + times jsonb;
                      roadmap_id and lesson_id nullable (custom homework); one open per student, lesson, kind,
                      set and assignment
notifications        (id bigserial, person_kind, person_id, kind, title, body, link,
                      assignment_id → assignments (deleted with it), created_at, read_at)
```

## 24. Universities and the dream university (built, for the web; D40)

Onboarding's main goal is a **dream university**, chosen on a globe; the SAT targets follow from it. The
universities also feed a university section later (its cards are already served).

### 24.1 The data
- Collected in the **private** repository `nazacd/makonbook-universities` (its README is the collectors'
  brief): `data/list.jsonl` (every university: id, name, country, city, location, QS place, logo),
  `data/universities.jsonl` (the whole record, collected batch by batch: tuition, living cost, aid,
  admissions with the SAT policy and percentiles, English tests, interview, deadlines, Uzbek students,
  sources), `data/countries.jsonl` (currency, study visa, refusal rate for Uzbek nationals, work rights,
  minimum wage, post-study work) and `logos/` (square, padded for a circle; SVG or PNG). Never copied here.
- An id is the **slug** of the English name, as `roadmap.Slug` makes it, so the roadmaps' universities match.

### 24.2 The import (`makonbook universities import <checkout>`)
- Checks everything first (ids, ISO country codes, list reasons, locations, QS ranks, logos: an SVG with an
  `<svg>` root or a square PNG, at most 1 MB) and stores nothing unless all of it passes, listing every
  problem. A whole record's fields win over the list's.
- Logos go to the **uploads bucket** at `universities/logos/<sha256>.<ext>`, only when changed (needs
  `R2_UPLOADS_*`); then the rows in one transaction. A university gone from the data stays, **unlisted**, for
  the students who chose it. `updated_at` moves only when something changed, so the list's version does too.

### 24.3 The API
- `GET /v1/universities` (signed in): every listed university in the data's order: id, name, short name,
  country, city, location, QS (`rank`, the lower end of a band, and `display` as QS shows it), `logo_url`,
  `sat` (the record's admissions.sat: policy, 25th–75th percentiles, a published minimum) and `has_details`.
  `ETag` is the list's version (304 when unchanged); the apps load it once.
- `GET /v1/universities/{id}` (signed in): the summary, `details` (the whole record without the collectors'
  `doubts`, or null) and `country_details`. `404 not_found`.
- `GET /v1/universities` also gives each university's `facts` for browsing (24.5).
- `GET /v1/universities/{id}/logo` (no sign-in, for `<img>`): from the bucket; `logo_url` carries `?v=` (the
  content's hash), and such a URL is cached for good; the SVG headers as 22.7's.
- The **dream university** is in the goals (9.6): `GET /v1/me/goals` gives `university_id` and `university`
  (its summary); `PUT` takes `university_id`: **left out, the stored one stays** (the phone doesn't send
  it), null clears it, an id must be listed (`422 invalid_goals`). Onboarding (22.3) takes `university_id`
  the same way (`422 invalid_onboarding` for an unknown one).
- The web suggests the target from the university's SAT percentiles (US universities have them) and ends
  the student's journey at it; the API stores only the choice.

### 24.4 Tables
```
universities          (id slug PK, name, short_name, country, city, lat, lng, qs_rank, qs_display, list_reason,
                       sat jsonb, details jsonb, logo_key, logo_type, logo_sha256, listed, position, updated_at)
university_countries  (code PK, details jsonb, updated_at)
student_goals         + university_id → universities (set null if deleted)
student_saved_universities (student_client_id, university_id, saved_at)
```

### 24.5 The university section (web, built)
Every listed university, to browse: search, filters (country and continent, cost a year, aid, acceptance
rate, SAT policy, English bachelor's, public or private, the student's list) and sorts (QS rank, cost,
acceptance rate, deadline, name). The list carries, per university, `facts` read from its record (type,
website, tuition and living cost in dollars a year, acceptance rate, English bachelor's, aid, the next
intake's deadlines, region, whether it was researched in full), worked out again only when the data
changes. A university's page shows its whole record: admissions (SAT policy and the admitted middle 50 %
against the student's own score, English tests, interview, how and when to apply), costs, aid and
scholarships, and its country's student visa, work rights and post-study work, each fact with its source.
Fields not collected yet say so; nothing is guessed. Students **save universities to a list**
(`GET /v1/me/universities`, `PUT`/`DELETE /v1/me/universities/{id}`) and set one as their dream university.
The visa difficulty level the collectors suggest is not shown until SATMAKON reviews it; the facts behind it are.


## 25. The Library (built, for the web; D41)

SAT MAKON's own books, read in the app: a shelf of covers and a reader.

### 25.1 The data
- In the **private** repository `nazacd/makonbook-library` (its README says how to add a book), never copied
  here. A book is three files with one name: `books/<name>.json` (`title`, `author`, `subject`: `math` or
  `english`, `description`, `pages`), `books/<name>.pdf` and `covers/<name>.png` (or `.jpg`, `.webp`; optional).
  The name without a leading `NN-`, which only orders the shelf, is the book's **id**.

### 25.2 The import (`makonbook library import <checkout>`)
- Checks everything first (names, the JSON's fields, a whole PDF of at most 40 MB, a cover that decodes, at
  most 2 MB) and stores nothing unless all of it passes, listing every problem.
- New or changed files go to the **uploads bucket** by content hash (`library/books/<sha256>.pdf`,
  `library/covers/<sha256>.<ext>`; needs `R2_UPLOADS_*`), then the rows in one transaction. A book gone from
  the folder leaves the shelf (**unlisted**; its readers' places are kept, and come back with it).

### 25.3 The API
- Behind `auth.RequireContent` (a registered student without a phone gets `403 phone_required`); students,
  registered or enrolled, and teachers alike.
- `GET /v1/library`: the listed books in order: id, title, author, subject, description, pages, `bytes`,
  `cover_url`, `views` and `progress` (`{page, updated_at}`, or null).
- `GET /v1/library/{id}/file`: the PDF from the bucket, inline, or an attachment named after the book with
  `?download=1`; ranges and `ETag` work. The API reads the file into memory (hence the 40 MB bound).
- `PUT /v1/library/{id}/progress {page}`: 1 to the book's pages, else `422 invalid_page`. It remembers the
  reader's page; the first time, it also makes them one of the book's **views** (a view is a person, not an
  opening).
- `GET /v1/library/{id}/cover` (no sign-in, for `<img>`): `cover_url` carries `?v=` (the content's hash), and
  such a URL is cached for good.
- A merge (21.4) moves a registered student's places to the other student (the further page where both read
  a book) and deletes them when merged into a teacher.

### 25.4 Tables
```
library_books     (id slug PK, title, author, subject, description, pages, pdf_key, pdf_sha256, pdf_bytes,
                   cover_key, cover_type, cover_sha256, listed, position, created_at, updated_at)
library_progress  (person_kind, person_id, book_id → library_books, page, opened_at, updated_at;
                   PK (person_kind, person_id, book_id); no foreign key to people)
```
