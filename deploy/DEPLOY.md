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
the release notes say otherwise).

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

## Everyday operations

| Task | Command (in `/opt/makonbook-api`) |
|---|---|
| Logs | `docker compose logs -f api` / `worker` |
| Someone forgot their password | `docker compose run --rm api reset-password student <client id>` |
| Who can use the admin panel | `docker compose run --rm api admin list` |
| Sync now | `docker compose run --rm api sync incremental` |
| Sync blocked by the mass-change guard, change is real | `docker compose run --rm api sync full --allow-mass-change` |
| Migration state | `docker compose run --rm api migrate status` |
| Practice tests and their review progress | `docker compose run --rm api practice list` |
| Stop / start | `docker compose stop` / `docker compose up -d` |

The shared `db` container is its own Compose project, `/opt/sat-makon/postgres`.
Restarting it briefly interrupts every site; `down -v` there would delete every
database.
