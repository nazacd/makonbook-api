# Deploying makonbook-api to the VPS

Target: `https://api.makonbook.uz`, on the existing VPS (188.245.175.83) next
to the Django site, the leaderboard and school28.

```
internet ─► makonbook-nginx (:80/:443, TLS)  ─► makonbook-api:8080         (serve)
                                                makonbook-api-worker        (HolliHop sync)
                         sat_makon_network  ─► db (Postgres 16): database makonbook_api,
                                                role makonbook_api (not a superuser)
```

Run every command as root on the VPS unless it says otherwise. Steps 0–7 are
the first deploy; later releases only need "Updating".

---

## 0. Security first: close Postgres to the internet

The `db` container publishes `0.0.0.0:5432`, and Docker's port publishing
bypasses `ufw`. Without a cloud firewall, the database is reachable from
anywhere. Fix **before** student data goes in.

1. **Hetzner Cloud Firewall** (no downtime): Console → Firewalls → Create.
   Inbound: TCP 22, 80, 443. If the leaderboard is opened as
   `http://<ip>:8080` / `:3000`, allow those two as well for now. **Never 5432.**
   Apply to the server.
2. Check: from your own computer (not the VPS)
   `nc -vz -w 5 188.245.175.83 5432` must time out or be refused.
3. Optional, also recommended: in `/opt/sat-makon/leaderboard/docker-compose.yaml`
   change `"5432:5432"` to `"127.0.0.1:5432:5432"`, then
   `cd /opt/sat-makon/leaderboard && docker compose up -d db`
   (the database restarts — about 10 s of errors on every site).

To see whether anyone tried to get in: `docker logs db 2>&1 | grep -c "password authentication failed"`.

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
-- Other apps' databases: no more "anyone may connect". Their apps log in as
-- postgres (superuser) and are unaffected.
REVOKE CONNECT ON DATABASE makonbook, satmakon FROM PUBLIC;
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
docker compose run --rm --entrypoint certbot certbot certonly \
  --webroot -w /var/www/certbot -d api.makonbook.uz \
  --non-interactive --agree-tos
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

## Everyday operations

| Task | Command (in `/opt/makonbook-api`) |
|---|---|
| Logs | `docker compose logs -f api` / `worker` |
| Someone forgot their password | `docker compose run --rm api reset-password student <client id>` |
| Sync now | `docker compose run --rm api sync incremental` |
| Sync blocked by the mass-change guard, change is real | `docker compose run --rm api sync full --allow-mass-change` |
| Migration state | `docker compose run --rm api migrate status` |
| Stop / start | `docker compose stop` / `docker compose up -d` |

The shared `db` container belongs to the **leaderboard** Compose project:
`docker compose down` in `/opt/sat-makon/leaderboard` stops the database for
every site. Never use `down -v` there — it deletes the database volume.
