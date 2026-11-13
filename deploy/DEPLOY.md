# Deploying makonbook-api to the VPS

Target: `https://api.makonbook.uz`, on the existing VPS (188.245.175.83) next
to the Django site, sat-makon.uz and school28.

```
internet ─► makonbook-nginx (:80/:443, TLS)  ─► makonbook-api:8080         (serve)
                                                makonbook-api-worker        (HolliHop sync)
                         sat_makon_network  ─► db (Postgres 16): database makonbook_api,
                                                role makonbook_api (not a superuser)
```

Run every command as root on the VPS unless it says otherwise. Steps 0–7 are
the first deploy; later releases only need "Updating".

---

## 0. Server state this guide assumes

Done during the October 2026 server clean-up:

- The shared Postgres runs from its own Compose project, `/opt/sat-makon/postgres`
  (container `db`, data volume `leaderboard_pgdata` — the name is historical),
  published on `127.0.0.1:5432` only. **Never** `docker compose down -v` there,
  never `docker volume prune`: the volume holds every app's database.
- The `postgres` superuser has a new random password (the old one had been
  under brute-force attack while the port was open).
- The leaderboard project and its `satmakon` database were removed.

Still recommended: a Hetzner Cloud Firewall allowing inbound TCP 22, 80, 443 only.

## 1. DNS

At Eskiz add an **A** record: `api.makonbook.uz` → `188.245.175.83`, TTL 3600.
Wait until the VPS sees it:

```bash
getent hosts api.makonbook.uz      # must print 188.245.175.83
```

## 2. Database and role

```bash
DB_PASSWORD=$(openssl rand -hex 24)
docker exec -i db psql -U postgres -v ON_ERROR_STOP=1 -v pw="$DB_PASSWORD" <<'SQL'
CREATE ROLE makonbook_api LOGIN PASSWORD :'pw';
CREATE DATABASE makonbook_api OWNER makonbook_api;
REVOKE ALL ON DATABASE makonbook_api FROM PUBLIC;
-- Django's database: no more "any role may connect". Django logs in as
-- postgres (superuser) and is unaffected.
REVOKE CONNECT ON DATABASE makonbook FROM PUBLIC;
SQL
echo "DATABASE_URL=postgres://makonbook_api:$DB_PASSWORD@db:5432/makonbook_api?sslmode=disable"
```

Keep the printed `DATABASE_URL` for step 3. The password is generated on the
server and never needs to leave it.

## 3. Files

```bash
mkdir -p /opt/makonbook-api && cd /opt/makonbook-api
```

Copy `deploy/docker-compose.yml` and `deploy/backup-postgres.sh` from this repo
into that directory — from your computer, in a checkout of the repo:

```bash
scp deploy/docker-compose.yml deploy/backup-postgres.sh root@makonbook.uz:/opt/makonbook-api/
```

Then on the VPS create `.env` from `deploy/env.production.example`:

```bash
cd /opt/makonbook-api
nano .env            # paste the template, fill DATABASE_URL (step 2) and HOLLIHOP_AUTHKEY
chmod 600 .env
docker compose config --quiet && echo compose OK
```

## 4. nginx and the certificate

The edge config is the single file `/opt/sat-makon/makonbook/nginx/default.conf`,
bind-mounted into `makonbook-nginx`.

> **Append, don't rewrite.** A single-file bind mount keeps pointing at the old
> file if an editor saves by replacing it (vim does; `cat >>` and nano don't),
> and nginx then silently keeps the old config. After every change, check what
> the container sees: `docker exec makonbook-nginx tail -5 /etc/nginx/conf.d/default.conf`.
>
> If `default.conf` is versioned in the Django repo, commit the same blocks
> there too, or the next Django deploy will drop them.

**4a. HTTP block.** Append PART 1 of `deploy/nginx-api.conf` (the `listen 80`
server block) to `default.conf`, then:

```bash
docker exec makonbook-nginx nginx -t && docker exec makonbook-nginx nginx -s reload
curl -sI http://api.makonbook.uz/ | head -1        # expect 301
```

**4b. Certificate** — a separate lineage, so the existing makonbook.uz
certificate is not touched:

```bash
cd /opt/sat-makon/makonbook
# The server has more than one Let's Encrypt account; use the one that renews makonbook.uz.
ACCOUNT=$(docker exec makonbook-nginx grep -h '^account' /etc/letsencrypt/renewal/makonbook.uz.conf | awk -F'= *' '{print $2}')
docker compose run --rm --entrypoint certbot certbot certonly \
  --webroot -w /var/www/certbot -d api.makonbook.uz \
  --account "$ACCOUNT" --non-interactive --agree-tos
```

The certbot container's 12-hourly `certbot renew` renews it with the others,
and nginx reloads every 6 hours.

**4c. HTTPS block.** Append PART 2 (the `listen 443` server block), then:

```bash
docker exec makonbook-nginx nginx -t && docker exec makonbook-nginx nginx -s reload
```

Until step 6, `https://api.makonbook.uz` answers 502 — expected.

## 5. Image

The GitHub workflow `.github/workflows/docker.yml` builds
`nazacd/makonbook-api` on every push to `main`. One-time setup in the GitHub
repo → Settings → Secrets and variables → Actions:

- `DOCKERHUB_USERNAME` — `nazacd`
- `DOCKERHUB_TOKEN` — Docker Hub → Account settings → Personal access tokens,
  read/write

After the merge to `main`, the Actions tab shows the build; it pushes
`:latest` and `:sha-<commit>`.

## 6. Start

```bash
cd /opt/makonbook-api
docker compose pull
docker compose run --rm api migrate up         # creates the tables
docker compose run --rm api sync full          # first HolliHop sync (~1 min), prints a summary
docker compose up -d
docker compose ps                              # api: healthy, worker: running
curl -s https://api.makonbook.uz/readyz        # {"status":"ok"}
curl -s https://api.makonbook.uz/v1/app/config
```

Check the sync summary: about 1 060 active students, 48 teachers, and the
phone issues admins already know about. Then `docker compose logs -f worker`
should show an incremental run every 15 minutes.

## 7. Backups

```bash
chmod 700 /opt/makonbook-api/backup-postgres.sh
/opt/makonbook-api/backup-postgres.sh            # first run by hand
ls -lh /opt/backups/postgres
crontab -e
# add:  30 2 * * * /opt/makonbook-api/backup-postgres.sh >> /var/log/pg-backup.log 2>&1
```

This backs up **every** database in `db` (Django's too — there were no backups
before). The copies are on the same disk; copy `/opt/backups/postgres` off the
server as well (Hetzner Storage Box + rsync is the cheap option).

Restore one database (example):
`docker exec -i db pg_restore -U postgres --clean --if-exists -d makonbook_api < /opt/backups/postgres/makonbook_api-<stamp>.dump`

---

## Updating

```bash
cd /opt/makonbook-api
docker compose pull
docker compose run --rm api migrate up
docker compose up -d
```

`serve` and `worker` refuse to start while migrations are pending, so a
forgotten `migrate up` shows up immediately as containers restarting.

**Pin a version** instead of `latest`: set `API_IMAGE=nazacd/makonbook-api:sha-<commit>`
in `.env`. **Roll back:** set the previous sha and `docker compose up -d`
(migrations only ever add; an older image runs fine on a newer schema unless
the release notes say otherwise). One exception: images from before migration
00038 (assignments) can't read custom homework (a roadmap assessment without a
lesson). To go back past it, run `migrate down` with the newer image first: it
deletes the assignments, their notifications and custom homework.

## Attendance write-back to HolliHop

The worker sends finished attendance to HolliHop (`SetStudentPasses`). It is
**off** until one real lesson has been checked with an admin watching
HolliHop: with `HOLLIHOP_WRITE_ENABLED=false` (the default) it only records
what it would send. What it would have sent:

```bash
docker compose logs worker | grep "would_send"
docker exec db psql -U postgres -d makonbook_api -c \
  "SELECT status, count(*) FROM hollihop_outbox GROUP BY status;"
```

**"HolliHop refused the API key"** in a sync alert: HolliHop no longer accepts
`HOLLIHOP_AUTHKEY` (regenerated, revoked or expired, or its HolliHop user was
changed); signing in to HolliHop's website still works, because a browser uses
a person's login, not the key. Ask SATMAKON's HolliHop administrator for the
current API key, replace it in `.env` and `docker compose up -d`. Until then the
apps run on the last good sync.

Going live: set `HOLLIHOP_WRITE_ENABLED=true` in `.env`, `docker compose up -d`.
Only lessons finished from then on are sent (earlier ones stay `dry_run`).
Rows HolliHop refuses because an admin set the day by hand end as
`set_manually` — the admin's entry stands. If absences turn out unbilled in
HolliHop, also set `HOLLIHOP_ABSENT_PAYABLE=true`.

To stop writing at once: `HOLLIHOP_WRITE_ENABLED=false`, `docker compose up -d`.

## Admin panel (admin.makonbook.uz)

The panel is built into the API image; the API serves it on the host named
in `ADMIN_HOST`, together with `/v1/admin/*`. Once:

1. **DNS:** an `A` record `admin.makonbook.uz` → the VPS's IP (same as
   `api.makonbook.uz`). Wait until `dig +short admin.makonbook.uz` shows it.
2. **nginx and certificate:** as in step 4, with `deploy/nginx-admin.conf`
   and `-d admin.makonbook.uz`: append PART 1, reload, issue the
   certificate, append PART 2, reload. Check with
   `curl -sI http://admin.makonbook.uz/ | head -1` (301) after PART 1.
3. **Settings:** add `ADMIN_HOST=admin.makonbook.uz` to `.env`, then
   `docker compose up -d`.
4. Open https://admin.makonbook.uz and sign in with a HolliHop login that has
   access (below).

`curl -s https://admin.makonbook.uz/v1/admin/me` answers 401 (not signed in),
and only the admin API is reachable on this host.

## Admin panel access

Admins sign in with their **HolliHop login and password** (HolliHop checks
them; MakonBook stores neither). Who may sign in is our own list — HolliHop
has no roles. Super admins see every branch; branch admins only theirs.

```bash
docker compose run --rm api admin find Nodira          # HolliHop staff → employee id
docker compose run --rm api admin offices              # branch ids and names
docker compose run --rm api admin grant employee 123 --super
docker compose run --rm api admin grant employee 456 --offices NOVZA,MINOR
docker compose run --rm api admin grant teacher 789 --offices CHILONZOR
docker compose run --rm api admin list
docker compose run --rm api admin revoke employee 456  # their sessions end at once
```

Once a super admin can sign in, they manage **branch admins** and **practice
editors** in the panel (Admins → Add admin or editor; an editor needs no
branch).
**Super admins are managed only here, with these commands**: the panel can't
make, change or remove a super admin (not even yourself). The commands refuse
to remove or demote the last super admin.
`grant` again with other options changes someone's access. A fired employee
or teacher is refused at sign-in even while still on the list.

Check a sign-in (needs `jq`: `apt install jq`; the `X-MakonBook-Admin` header is required; type the
password at the prompt, don't put it in shell history):

```bash
read -rp "HolliHop login: " L; read -rsp "Password: " P; echo
curl -s -c /tmp/mb -H 'X-MakonBook-Admin: 1' -H 'Content-Type: application/json' \
  -d "$(jq -n --arg l "$L" --arg p "$P" '{login:$l,password:$p}')" \
  https://api.makonbook.uz/v1/admin/login; unset P; echo
curl -s -b /tmp/mb https://api.makonbook.uz/v1/admin/me; rm -f /tmp/mb
```

## Practice tests: loading reviewed tests

Reviewed tests live in the private repository `nazacd/makonbook-questionbank`,
one folder per test. The server reads them from a read-only checkout. Once:

```bash
ssh-keygen -t ed25519 -f ~/.ssh/questionbank -N "" -C "makonbook server"
cat ~/.ssh/questionbank.pub
```

On GitHub, open the repository's **Settings → Deploy keys → Add deploy key**,
paste that key, and leave "Allow write access" **off**. Then:

```bash
GIT_SSH_COMMAND="ssh -i ~/.ssh/questionbank" \
  git clone git@github.com:nazacd/makonbook-questionbank.git /opt/makonbook-questionbank
```

Each time new tests have been reviewed, run `deploy/import-practice.sh`
(copy it once: `scp deploy/import-practice.sh root@makonbook.uz:/opt/makonbook-api/`,
then `chmod 700 /opt/makonbook-api/import-practice.sh`). It does:

```bash
GIT_SSH_COMMAND="ssh -i ~/.ssh/questionbank" git -C /opt/makonbook-questionbank pull
docker compose run --rm -v /opt/makonbook-questionbank:/questionbank:ro api practice import /questionbank
docker compose run --rm api practice list
```

To load new tests every night instead, add to `crontab -e`:
`15 4 * * * /opt/makonbook-api/import-practice.sh >> /var/log/practice-import.log 2>&1`

`practice import` loads the tests that aren't loaded yet and skips the rest.
Questions the AI review rated green are approved as they load; if the review
proposed a small fix for one (a typo, a missing word), the import applies it
as a version of its own ("Fixed by the AI review" in the question's history).
When the fix step has run on tests already loaded, `practice import` adds the
fixes to them ("already loaded; N AI reviews updated") and uploads the figures
it redrew (`figures/` in the questionbank) to MakonBook's bucket, so it needs
the `R2_UPLOADS_*` settings.
Editors then work on them in the admin panel (Practice tests). To show the
questions' figures and the bank's original screenshots there, set the `R2_*`
variables in `.env` (see `env.production.example`; a **read-only** R2 token)
and `docker compose up -d`.

Images editors upload go to **MakonBook's own R2 bucket**, never SAT MAKON's.
Once, in Cloudflare: **R2 → Create bucket** (e.g. `makonbook`, private), then
**Manage R2 API Tokens → Create API token** with **Object Read & Write**,
applied to that bucket only. Put it in `.env` as `R2_UPLOADS_BUCKET`,
`R2_UPLOADS_ACCESS_KEY_ID` and `R2_UPLOADS_SECRET_ACCESS_KEY` (same account, so
the endpoint is `R2_ENDPOINT_URL`) and `docker compose up -d`. Without them the
upload button answers "image uploads aren't set up on the server".
A test in the pilot's old format (DAY1) is refused until its review is re-run.
Every test stays locked until a teacher has approved all its questions.

**Questions written outside the AI review** (a teacher's, or drafts written
for teachers to check, e.g. for a skill the roadmaps need and the bank lacks)
are added from a file with one JSON object per line: `subject`, `type` (`mcq`
or `grid_in`), an optional `passage`, `stem`, `choices` and `correct` (or
`accepted`), `explanation`, `domain`, `skill` (spelled as in the editor's
list) and `difficulty`; no figures. The file is test content: like the
curriculum, it is never committed. Give it to uid 65532 and load it:

```bash
docker compose run --rm -v /opt/drafts:/drafts:ro api practice add /drafts/questions.jsonl
```

Every line is checked as an editor's save is, and nothing is stored unless all
pass. The questions wait **in review** (Questions to fix, at the end of the
list; "Added as a draft" in their history) until an editor approves them; the
command prints each one's admin link. Questions already stored are skipped, so
loading a file twice is harmless. Once approved, the roadmaps' level checks
and homework draw them at once (`curriculum coverage` shows the new counts).

**Publishing to students.** The `worker` publishes every test whose questions
are all approved, within 5 minutes ("practice: published a test" in its log):
it builds the test's package (a numbered version that never changes), copies
its images into MakonBook's bucket under `p/img/`, and the apps download both
(the images through links the API signs, valid for 6 hours, straight from R2).
A later edit makes a new version once the test is approved again; until then
students keep the last one. It needs both `R2_*` (to read the bank's images)
and `R2_UPLOADS_*`. To publish at once: `docker compose run --rm api practice
publish`. `practice list` shows `OPEN v2` for a test students have.

## Roadmaps: loading the curriculum

The roadmaps (spec 22) are built on SAT MAKON's curriculum: one JSON file per
course (`math-foundation.json` … `english-advanced.json`). It is SAT MAKON's own
material, so it is **never committed**; copy the files to the server instead:

```bash
ssh root@makonbook.uz 'mkdir -p /opt/curriculum && chmod 700 /opt/curriculum'
scp math-*.json english-*.json root@makonbook.uz:/opt/curriculum/
ssh root@makonbook.uz 'chown -R 65532:65532 /opt/curriculum'
```

The container runs as distroless's nonroot user (uid 65532), so the folder
belongs to it: root and the import can read the files, nobody else can.

Then, in `/opt/makonbook-api` (after `migrate up` has applied 00036 and 00037):

```bash
docker compose run --rm -v /opt/curriculum:/curriculum:ro api curriculum import /curriculum
```

The import checks every file first and stores nothing unless all of them pass
(it lists the problems otherwise). Each course that changed becomes a new
version, live at once; students keep their progress (it is kept by lesson id).
It then prints the coverage report: for each level check and homework set, the
approved questions at each difficulty (`have/want`), with `SHORT` where the bank
can't fill it as asked (students then get questions of a neighbouring
difficulty, or fewer). `curriculum coverage` prints it again at any time, e.g.
after more questions are approved. Run the import again whenever the files change.

**University logos.** The stops show their university's logo, used with the
universities' permission. Logos are trademarks, so they are **never committed**
either: they go in `/opt/curriculum/logos/`, one file per university, named by
its slug with `.svg`, `.png` or `.webp` (at most 1 MB; the import checks the
bytes, not the name). An SVG must be UTF-8: one whose first line declares
another encoding (`encoding="iso-8859-1"`, as some editors save) is skipped as
not a valid SVG; change the declaration to `encoding="UTF-8"` if the file
has only plain ASCII text, or re-save it as UTF-8. To see which file names are wanted:

```bash
docker compose run --rm api curriculum logos
```

Copy the files, give them to uid 65532 like the JSON, and import again:

```bash
ssh root@makonbook.uz 'mkdir -p /opt/curriculum/logos'
scp logos/*.svg logos/*.png root@makonbook.uz:/opt/curriculum/logos/
ssh root@makonbook.uz 'chown -R 65532:65532 /opt/curriculum'
docker compose run --rm -v /opt/curriculum:/curriculum:ro api curriculum import /curriculum
```

The import prints the logos loaded, unchanged and skipped (with the reason: an
unknown name, not a valid SVG/PNG/WebP, over 1 MB, a second file for one
university) and the universities still without one. A changed file replaces
the stored logo; students' browsers fetch the new one at once (its URL changes).

## Universities (onboarding's dream university)

The universities the onboarding's globe shows (spec 24) live in the private
repository `nazacd/makonbook-universities`: `data/` (the list, the records
collected so far, the countries) and `logos/`. The server reads a read-only
checkout, like the question bank's. Once, a deploy key of its own (GitHub
allows a key on one repository only):

```bash
ssh-keygen -t ed25519 -f ~/.ssh/universities -N "" -C "makonbook server"
cat ~/.ssh/universities.pub
```

On GitHub: **makonbook-universities → Settings → Deploy keys → Add deploy
key**, paste it, "Allow write access" **off**. Then:

```bash
GIT_SSH_COMMAND="ssh -i ~/.ssh/universities" \
  git clone git@github.com:nazacd/makonbook-universities.git /opt/makonbook-universities
```

After `migrate up` has applied 00040, and each time the data changes:

```bash
GIT_SSH_COMMAND="ssh -i ~/.ssh/universities" git -C /opt/makonbook-universities pull
docker compose run --rm -v /opt/makonbook-universities:/universities:ro api universities import /universities
```

The import checks everything first and stores nothing unless all of it
passes (it lists the problems otherwise). It uploads the logos that changed
to MakonBook's bucket (`R2_UPLOADS_*`, as for editors' images: about 25 MB the
first time, nothing after when unchanged) and prints what it did:
`1698 universities listed (… new, … changed, … unlisted); … with their whole
record, … with a logo`. A university gone from the data stays, unlisted, for
the students who chose it. Students see changes at once (the list's ETag
moves only when something changed).

## The question bank's answers (migration 00045, once)

After `migrate up` has applied 00045, fill the question bank's answers from
the level checks, homework and practice sets finished before it, so students'
solved counts and analytics include them (safe to run again):

```bash
docker compose run --rm api practice backfill-answers
```

## The Library's books

SAT MAKON's books (spec 25) live in the private repository
`nazacd/makonbook-library`: `books/` (a `.json` and a `.pdf` per book) and
`covers/`. The server reads a read-only checkout. Once, a deploy key of its
own:

```bash
ssh-keygen -t ed25519 -f ~/.ssh/library -N "" -C "makonbook server"
cat ~/.ssh/library.pub
```

On GitHub: **makonbook-library → Settings → Deploy keys → Add deploy key**,
paste it, "Allow write access" **off**. Then:

```bash
GIT_SSH_COMMAND="ssh -i ~/.ssh/library" \
  git clone git@github.com:nazacd/makonbook-library.git /opt/makonbook-library
```

After `migrate up` has applied 00041, and each time a book is added or changed:

```bash
GIT_SSH_COMMAND="ssh -i ~/.ssh/library" git -C /opt/makonbook-library pull
docker compose run --rm -v /opt/makonbook-library:/library:ro api library import /library
```

The import checks everything first and stores nothing unless all of it
passes. It uploads new or changed PDFs and covers to MakonBook's bucket
(`R2_UPLOADS_*`) and prints `9 books on the shelf (… new, … changed, … taken
off); … files uploaded`. A book removed from the repository leaves the shelf;
its readers' places are kept in case it returns.

## Question pictures (renderer)

Question pictures (the editors' **Export image**, and later the teachers'
Telegram group) are drawn by a headless Chrome, the `renderer` service in
`docker-compose.yml` (`chromedp/headless-shell`, pinned; about 535 MB of disk,
up to 700 MB of memory while drawing, about 90 MB idle). It is on an internal
network only the API reaches, with no internet: the API hands it each page
whole. Compose sets `RENDERER_URL` for the API, so after copying the new
`docker-compose.yml`:

```bash
docker compose pull renderer && docker compose up -d
```

To check it, draw one question into `/tmp` on the server:

```bash
docker compose run --rm -v /tmp:/out api practice picture DAY3 english-m2-04 original /out/q.png
```

`original` is the bank's own question (its text from the questionbank's
`original.jsonl`, its screenshots); `fix` the AI's fixed version with the
changes highlighted; `question` the question as it is now.

## The teachers' group review bot

The `bot` service (`makonbook review-bot`) posts each yellow or red question
that has an AI fix to the teachers' Telegram group: the bank's original and the
AI's fix as pictures, then Approve / Deny. One vote per Telegram account,
hidden totals until the decision: red needs 10 approvals or 5 denials, yellow
5 and 5. Approved: the fix is applied (a version "Teachers' group") and the
question approved. Denied: it stays for the editors, with the reasons the bot
asks each teacher who denied in a private chat. It posts one question an hour
from 08:00 to 23:00 (Tashkent) while fewer than 20 are undecided; nothing at
night. A question that doesn't get enough votes in 48 hours closes ("not
enough votes", buttons removed) and stays for the editors, so the next one
takes its place. To change the numbers: `docker exec -i db psql -U postgres -d
makonbook_api -c "UPDATE review_bot SET max_open = 20, vote_hours = 48"`. Questions an editor has already changed, or that need a person (a photo
to replace), are left to the editors.

Once:

1. In Telegram, **@BotFather → /newbot**; keep the token. Then **/mybots → the
   bot → Bot Settings → Allow Groups → on**.
2. Add the bot to the teachers' group and make it an **admin** (it only needs
   to post messages).
3. On the server, in `.env`: `TELEGRAM_REVIEW_TOKEN=<the token>`, leave
   `TELEGRAM_REVIEW_CHAT_ID` empty, and `docker compose up -d`.
4. `docker compose logs bot`: the line `added to or removed from a chat` shows
   the group's ID (a negative number like `-1001234567890`; Telegram keeps the
   news of the bot being added for a day). If it isn't there, write
   `/start@<the bot's username>` in the group and look again (a line `a
   group's ID`). Put it in `.env` as `TELEGRAM_REVIEW_CHAT_ID` and
   `docker compose up -d` again.

The bot starts posting at the next full hour between 08:00 and 23:00. To stop
it for a while: `docker compose stop bot`.

When an update changes how question pictures look, the bot replaces the photos
of the questions still open in the group when it restarts (a few seconds per
picture; the log says `redrew open posts`). Decided ones keep their pictures.

## Demo accounts for app store review

Google Play (and the App Store) ask for accounts the reviewer can sign in
with. MakonBook has two, both with the password `DEMO_PASSWORD`:

- **`demo.student`**, a fictional "Demo Student" in two fictional classes (SAT
  Math and SAT English, branch "SAT MAKON (demo)"), with their schedule, eight
  weeks of attendance, practice results over the last six weeks that go up and
  down but rise (Day 1 1240, Day 2 1290 and 1340, Days 3–7 1310–1390, Day 8
  1380, Day 9 1410 and 1520) and a parent code.
- **`demo.teacher`**, the fictional teacher "Nodira Aliyeva": the SAT Math
  class and "SAT Practice (demo, open all day)", a class every day from 00:00
  to 23:59 so the reviewer can open attendance (QR, marks) whenever they look.
  The classes have ten fictional classmates with practice results.

None of it is in HolliHop, and it stays apart from the school's data: the sync
never touches it, admins' lessons, reports and code search leave it out,
attendance taken in it is never written to HolliHop, and a parent's report
about the demo child doesn't go to Telegram.

Once:

1. Choose a password (8+ characters) and put it in `.env`:
   `DEMO_PASSWORD=<the password>`, then `docker compose up -d`.
2. `docker compose run --rm api demo`: it makes the accounts and prints the
   parent code and the practice results.
3. In Play Console → **App content → App access**: "All or some functionality
   is restricted", then add the logins (`demo.student` and `demo.teacher`, the
   password) and a note such as: *Sign in on the "Sign in" screen. The student
   account shows classes, attendance and practice results; the teacher account
   can take attendance in "SAT Practice (demo, open all day)" at any time.
   Parent mode: on the first screen tap "Ota-onalar uchun" and enter the parent
   code shown in the student's Profile.*

The worker brings them up to date every day: the attendance and the practice
dates move along with the calendar, the teacher's attendance from earlier days
is cleared, and a password or username a reviewer changed goes back. A result
needs its test published; until it is, the command says so and skips it. To
delete the accounts: remove `DEMO_PASSWORD` from `.env`, `docker compose up -d`,
then `docker compose run --rm api demo remove`.

## Parents

Parents have no accounts. Every student has a permanent **5-digit parent code**
(the worker gives codes to new students every hour; a student sees theirs in the
app's Profile). In the app, **Ota-onalar uchun** on the first
screen takes the code and shows that child read only, in Uzbek or Russian:
classes, schedule, attendance. A parent can add several children.

**Reports.** "Report a problem" in the app goes to the admin panel
(**Parents**, for the admins of the student's branches: answer, then *Mark
handled*; the parent sees the answer in the app) and to the Telegram chat of
`TELEGRAM_BOT_TOKEN` / `TELEGRAM_ALERT_CHAT_ID` (the sync alerts chat), with a
link to the panel. Without those two settings reports are only in the panel.

**A parent asks the office for the code.** Admin panel → Parents → *Find a
parent code* (by the student's name). Give it only to the student's own parent:
it shows where the child is and when.

**Guessing.** Five digits can be guessed, so tries are limited: 10 per network
and 5 per phone in 15 minutes, and 50 wrong codes in an hour from anywhere sends
one alert to the Telegram chat with the networks they came from. If that alert
comes, look at `docker compose logs api | grep parent/login`. A code can't be
changed yet; ask for that if a code leaks.

## Accounts: sign-up, SMS codes, Google (spec 21)

The web app (sat.makonbook.uz) lets anyone sign up as a student (first an
email code or Google, then an SMS code for the phone), reset a password with a
code sent by SMS, and sign in with Google, a phone or an email. Each part is off
until its settings are in `.env`; `/v1/app/config` tells the web which are on
(`signup`, `google`). Phone + password login works as before either way.

**SMS (Eskiz.uz).** Sign-up and password reset need it.

1. Eskiz account (my.eskiz.uz): a company account with a balance. Ask Eskiz
   for a **sender name** (alpha name, e.g. `SATMAKON`; until it is approved
   the account sends as `4546`) and get the **message template** approved:
   Eskiz sends only approved texts, so the exact text of `SMS_TEXT` (with the
   code where `{code}` is) must be the approved one.
2. In `.env`: `SMS_PROVIDER=eskiz`, `ESKIZ_EMAIL`, `ESKIZ_PASSWORD` (the
   account's; a secret), `ESKIZ_FROM` (the approved sender name, or `4546`),
   `SMS_TEXT` (default `SATMAKON: your code is {code}`). `docker compose up -d`.
3. Try it: sign up on the web with your own number. A failed send shows in
   `docker compose logs api | grep -i eskiz`; the person is told to try again.

Limits: one code a minute and 5 a day per number, 20 an hour per network
(behind the edge nginx, so `TRUST_PROXY=true` must stay). Each code costs an
SMS; a reset for a number nobody signs in with sends nothing.

**Email (SMTP).** Sign-up starts with an email code (or Google): with neither,
there is no sign-up (`signup: false`), only password reset.

- **Google Workspace** (if makonbook.uz mail is on Workspace): make a mailbox
  such as `no-reply@makonbook.uz`, turn on 2-step verification for it and
  create an **app password**; then `SMTP_HOST=smtp.gmail.com`,
  `SMTP_PORT=587`, `SMTP_USERNAME=no-reply@makonbook.uz`,
  `SMTP_PASSWORD=<the app password>`. Gmail sends about 2,000 messages a day
  per mailbox. For more, or without a mailbox password, use the Workspace
  **SMTP relay** service (Admin console → Apps → Google Workspace → Gmail →
  Routing → SMTP relay service; allow the VPS's IP address, require TLS):
  `SMTP_HOST=smtp-relay.gmail.com`, `SMTP_PORT=587`, no username.
- Any other provider works the same: port 587 needs STARTTLS, 465 is TLS from
  the start.
- `MAIL_FROM="SATMAKON <no-reply@makonbook.uz>"` (the address must be one the
  server may send as). Make sure makonbook.uz has SPF and DKIM for the
  provider, or the codes land in spam.
- `docker compose up -d`, then sign up on the web with your own address.
  Failures show in `docker compose logs api | grep -i smtp`.

**Google sign-in.**

1. Google Cloud console, a project for SATMAKON → **Google Auth Platform**
   (formerly APIs & Services → OAuth consent screen). **Branding:** app name
   **SATMAKON**, support email, logo if wanted, home page
   `https://sat.makonbook.uz`, privacy policy (and terms) links on that
   domain, authorized domain `makonbook.uz`. **Audience:** External, then
   *Publish app* (In production; while "Testing" only listed test users can
   sign in). The scopes are only `openid`, `email` and `profile`, which are
   non-sensitive: no scope verification; Google may still ask to verify the
   brand before the consent screen shows the name and logo.
2. **Clients → Create client → Web application**: authorized JavaScript
   origin `https://sat.makonbook.uz`, **authorized redirect URI
   `https://sat.makonbook.uz/v1/auth/google/callback`** (exactly). Copy the
   client ID and secret.
3. In `.env`: `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET` (a secret),
   `WEB_ORIGIN=https://sat.makonbook.uz`. `docker compose up -d`.

The edge nginx block of sat.makonbook.uz (in makonbook-web's `deploy/`) sends
`/v1/` to this API, so the callback is on the web's own origin. To turn Google
off, empty the two `GOOGLE_*` settings.

**Telegram sign-in** (Telegram's OpenID Connect login; it can share the
person's verified phone, so no SMS is needed). A bot of its own, not the
alerts' bot (`TELEGRAM_BOT_TOKEN`) or the review bot.

1. In Telegram, @BotFather → `/newbot`: name **SATMAKON**, a username such as
   `SatMakonBot`; give it the SATMAKON logo (`/setuserpic`): people see the
   bot's name and picture when they approve the login.
2. Open the **@BotFather mini app** → the bot → **Login Widget**. Add the
   **Allowed URLs** `https://sat.makonbook.uz` and
   `https://sat.makonbook.uz/v1/auth/telegram/callback` (exactly). Copy the
   **Client ID** and **Client Secret** shown there. Leave the signing
   algorithm at RS256 (ES256 works too; EdDSA/ES256K don't allow the phone).
3. In `.env`: `TELEGRAM_CLIENT_ID=<client id>`,
   `TELEGRAM_CLIENT_SECRET=<client secret>` (a secret), and `WEB_ORIGIN`
   (`https://sat.makonbook.uz`, as for Google). `docker compose up -d`.
4. Try it: *Continue with Telegram* on the web.

The old settings `TELEGRAM_LOGIN_BOT_TOKEN`, `TELEGRAM_LOGIN_BOT_USERNAME` and
`TELEGRAM_LOGIN_WEBHOOK_SECRET` (the hash widget and the "share my number"
bot) aren't read any more: remove them from `.env`. If a webhook was
registered for that bot, remove it once with
`curl "https://api.telegram.org/bot<token>/deleteWebhook"`.

**Accounts without a phone.** People who sign up with an email or Google, or
decline sharing the phone in Telegram, have no phone until they add one (SMS
code or Telegram); until then practice and vocabulary answer
`phone_required`. Adding a number someone else has merges the two accounts.

**Who signed up.** Self-registered students are rows of `students` with
`source = 'self'` (ids −1000001 and down). When the office adds one of them in
HolliHop with the same phone, the next sync makes their account the HolliHop
student's (the run's stats list it under `linked`); nothing to do. If the
office gives that phone to someone **already** in HolliHop, the HolliHop issues
page shows *Phone number of a web account*: correct the number in HolliHop if
it is wrong; if it really is the same person, the two accounts must be merged
by hand (ask a developer).

```bash
docker exec db psql -U postgres -d makonbook_api -c \
  "SELECT count(*) FILTER (WHERE source = 'self') AS registered FROM students;"
```

## Everyday operations

| Task | Command (in `/opt/makonbook-api`) |
|---|---|
| Logs | `docker compose logs -f api` / `worker` |
| Someone forgot their password | On the web: *Forgot your password?* (a code by SMS). Or back to the default: `docker compose run --rm api reset-password student <client id>` |
| Who can use the admin panel | `docker compose run --rm api admin list` |
| Sync now | `docker compose run --rm api sync incremental` |
| Sync blocked by the mass-change guard, change is real | `docker compose run --rm api sync full --allow-mass-change` |
| Migration state | `docker compose run --rm api migrate status` |
| Practice tests and their review progress | `docker compose run --rm api practice list` |
| Publish approved tests now (the worker does it every 5 minutes) | `docker compose run --rm api practice publish` |
| Load the word lists (folder with `words.jsonl` or `words.csv`, and `images/`) | `docker compose run --rm -v /opt/vocab:/vocab:ro api vocab import /vocab` |
| Load or update the roadmaps' curriculum (below) | `docker compose run --rm -v /opt/curriculum:/curriculum:ro api curriculum import /curriculum` |
| How well the bank covers the roadmaps | `docker compose run --rm api curriculum coverage` |
| Which universities have a logo, and the file name each takes | `docker compose run --rm api curriculum logos` |
| Review bot: what it did | `docker compose logs --tail 100 bot` |
| Draw a question picture | `docker compose run --rm -v /tmp:/out api practice picture DAY3 english-m2-04 fix /out/q.png` |
| Test the app as a real teacher and student | `/opt/makonbook-api/test-accounts.sh` (below) |
| Stop / start | `docker compose stop` / `docker compose up -d` |

**Testing with real accounts.** `deploy/test-accounts.sh` (copy it once:
`scp deploy/test-accounts.sh root@makonbook.uz:/opt/makonbook-api/`, then
`chmod 700` it) picks a teacher who teaches a group now and a student of that
group, both free (never used the app, or put back with `reset-password`), and prints their phone numbers and default
passwords. Test in the app, then type `done` (or press Ctrl+C): it deletes the
password, username and sessions the test gave them, the student's practice
attempts and roadmap level checks and homework, and parent sign-ins and reports made with the student's code during
the test, so both are as they were. (The student's parent code itself stays: it
never changes.) Attendance marks made while
testing are real and stay; the script says if there were any. To borrow
particular people: `TEST_TEACHER_ID=… TEST_STUDENT_ID=… ./test-accounts.sh`.

The shared `db` container is its own Compose project, `/opt/sat-makon/postgres`.
Restarting it briefly interrupts every site; `down -v` there would delete every
database.
