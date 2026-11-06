# makonbook-api

Go backend for MakonBook: the API used by the mobile app and the admin panel,
plus the worker that syncs with HolliHop. What it does and why is in
[`docs/platform-spec.md`](docs/platform-spec.md) — read it before changing
behaviour.

**Status:** phase 1.
- Step 1 — project skeleton, configuration, database and migrations, HTTP
  server with health checks and `GET /v1/app/config`.
- Step 2 — HolliHop sync: students, teachers, groups, schedules and
  enrollments mirrored into Postgres, login phones, data-quality issues,
  mass-change guard, alerts.
- Step 3 — login: phone or username + password, default password (last 4
  digits of the phone) that must be replaced, escalating lockout, sessions,
  `GET /v1/me` with groups and schedules.

## Getting started

Requirements: Go 1.27+, Docker (for the local database), `make`.

```bash
cp .env.example .env
make db-up          # Postgres 16 in Docker (production's version), plus a makonbook_test database
make migrate-up
make run            # http://localhost:8080
curl localhost:8080/v1/app/config
```

`make help` lists every target.

## Commands

The project builds one binary, `makonbook`:

| Command | What it does |
|---|---|
| `makonbook serve` | Run the HTTP API. Refuses to start if migrations are pending. |
| `makonbook worker` | Run the HolliHop sync forever: incremental every `SYNC_INTERVAL`, full once a day after `SYNC_FULL_AT`. |
| `makonbook sync full\|incremental` | Run one sync now and print a summary. Add `--allow-mass-change` to apply a run the guard blocked, after checking the change is real. |
| `makonbook telegram webhook <public URL>` | Register the Telegram sign-in bot's webhook (after setting `TELEGRAM_LOGIN_*`). |
| `makonbook reset-password student\|teacher <id>` | Back to the default password, unlock, log out everywhere — for someone who forgot their password. `<id>` is the HolliHop client id (students) or teacher id. |
| `makonbook migrate up` | Apply pending migrations. Run this before `serve` on every deploy. |
| `makonbook migrate down` | Roll back the last migration. |
| `makonbook migrate status` | List migrations and whether each is applied. |

## Configuration

All configuration comes from environment variables (see `.env.example`).

| Variable | Default | Notes |
|---|---|---|
| `DATABASE_URL` | — (required) | `postgres://user:pass@host:5432/db` |
| `APP_ENV` | `development` | `development` or `production` |
| `HTTP_ADDR` | `:8080` | |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `LOG_FORMAT` | `text` (`json` in production) | |
| `SHUTDOWN_TIMEOUT` | `15s` | Grace period for in-flight requests |
| `TRUST_PROXY` | `false` | `true` behind the edge nginx: take the client IP from `X-Forwarded-For` (login rate limit). Never without a proxy. |
| `TIMEZONE` | `Asia/Tashkent` | School time zone; defines "today" for schedules |
| `HOLLIHOP_BASE_URL` | — (worker/sync) | e.g. `https://school.t8s.ru/Api/V2` |
| `HOLLIHOP_AUTHKEY` | — (worker/sync) | Secret. Never logged. |
| `SYNC_INTERVAL` | `15m` | Between incremental syncs |
| `SYNC_FULL_AT` | `03:00` | Local time of the nightly full sync |
| `TELEGRAM_BOT_TOKEN`, `TELEGRAM_ALERT_CHAT_ID` | — | Optional, set both: sync alerts, parents' reports and the parent-code guessing alarm go to this chat instead of only the log |
| `DEMO_PASSWORD` | — | Optional. Secret. The demo accounts for app store reviewers (`demo.student`, `demo.teacher`; see deploy/DEPLOY.md); without it there are none |
| `SMS_PROVIDER` | — (off) | Sign-up and password reset by SMS code (spec 21): `eskiz`, or `log` (development: the code goes to the log) |
| `ESKIZ_EMAIL`, `ESKIZ_PASSWORD` | — | With `SMS_PROVIDER=eskiz`: the Eskiz.uz account. The password is a secret |
| `ESKIZ_FROM` | `4546` | Eskiz sender name |
| `SMS_TEXT` | `SATMAKON: your code is {code}` | The SMS; must contain `{code}` and match a template Eskiz approved |
| `SMTP_HOST` | — (off) | Email codes, sign-up's first step (spec 21): the mail server, or `log` (development) |
| `SMTP_PORT` | `587` | 587 (STARTTLS) or 465 (TLS) |
| `SMTP_USERNAME`, `SMTP_PASSWORD` | — | If the server wants a login. The password is a secret |
| `MAIL_FROM` | — | Required with a mail server, e.g. `SATMAKON <no-reply@makonbook.uz>` |
| `TELEGRAM_LOGIN_BOT_TOKEN` | — (off) | The Telegram sign-in bot (its own bot): sign-in and phone checks (spec 21.5). Secret |
| `TELEGRAM_LOGIN_BOT_USERNAME` | — | Its username, without @ |
| `TELEGRAM_LOGIN_WEBHOOK_SECRET` | — | Secret header Telegram sends to `/v1/telegram/webhook`; register with `makonbook telegram webhook <public URL>` |
| `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET` | — (off) | Google sign-in; set both. The secret is a secret |
| `WEB_ORIGIN` | — | The web app's origin (`https://sat.makonbook.uz`), required with Google: the redirect URI is `${WEB_ORIGIN}/v1/auth/google/callback` |
| `TEST_DATABASE_URL` | — | Tests only; database tests are skipped without it |

## Layout

```
cmd/makonbook/         entry point and subcommands
api/openapi.yaml       API contract for the mobile app and admin panel
db/migrations/         goose SQL migrations, embedded in the binary
internal/
  app/                 wires features into one HTTP handler
  platform/            shared plumbing: config, logging, postgres, migrate, httpx, alert, sms, testdb
  auth/                login, password rules, lockout, sessions, /v1/me; sign-up, SMS codes,
                       password reset, Google sign-in, own names (spec 21)
  hollihop/            read-only HolliHop API client
  people/              mirror of HolliHop people and groups; login phones; data-quality issues
  sync/                sync runs: fetch → people.Apply in one transaction → cursor, guard, alerts
  system/              health checks and public app config
    queries.sql        each feature's SQL
    systemdb/          sqlc-generated code — do not edit
```

Code is organised **by feature**, not by layer. Each feature owns its HTTP
handlers, its rules and its SQL (`queries.sql` → generated `<feature>db`
package). A feature never queries another feature's tables directly.

## Working on the code

- **Changing the schema:** add a new file in `db/migrations/` (next number,
  `-- +goose Up` and `-- +goose Down` sections). Never edit a migration that has
  been deployed. Tests check that every migration rolls back cleanly.
- **Changing SQL:** edit the feature's `queries.sql`, then `make generate`.
  Generated code is committed; CI fails if it is stale.
- **Adding an endpoint:** describe it in `api/openapi.yaml` in the same change.
- **Errors:** respond with `httpx.WriteError`, which produces
  `{"error": {"code", "message", "request_id"}}`. Clients rely on `code`.
- **Tests:** `make test`. Database tests get their own freshly migrated
  database via `testdb.New(t)`, so they can run in parallel.
- **Checks:** `make lint` and `make test` must pass; CI runs both plus the
  generated-code check.

## HolliHop sync in one paragraph

A run fetches teachers, groups, students and enrollments from HolliHop, then
applies everything in one transaction. Students are stored only once they have
been "Занимается"; people who leave are **deactivated, never deleted**. Each
active person gets a `login_phone` only if their HolliHop mobile is valid and
not shared with anyone else active; otherwise a row in `data_quality_issues`
says what admins must fix. A run that would deactivate more than 10 % of
students or teachers is rolled back and alerted (`blocked`). Details: spec
section 6.

## Login in one paragraph

People log in with their HolliHop phone number (any format) or a username they
chose, plus a password. Until they choose one, the password is the last 4
digits of their login phone — derived, never stored — and the API only allows
`/v1/me`, `/v1/auth/password` and `/v1/auth/logout`. Five wrong passwords in a
row lock the account for 15 minutes, doubling with each further lockout up to
24 hours. Sessions are random bearer tokens stored as hashes; they end on
logout, on a password change (other devices), when the person stops being
active in HolliHop, and after 30 days unused. Someone who leaves and returns
starts again from the default password. Details: spec section 7.

## Accounts in one paragraph

Anyone can sign up on the web as a student: first an email, proved by a
6-digit code sent to it, Google or Telegram; then an Uzbek phone, proved by a
code by SMS (Eskiz.uz) or by sharing it with the Telegram sign-in bot. They can sign in with the phone or the email afterwards. A
SAT MAKON student's or teacher's phone signs into their account instead (once
they confirm it is them); a phone someone already signed up with is refused. These
"registered" students are rows of `students` with `source = 'self'` and ids
from −1000001 down; the sync never touches them, except that a new HolliHop
student with the same verified phone takes over the row (re-keyed to the
ClientId, everything kept). Registered students see the practice tests DAY1–DAY10
only. Everyone can set their own name, which the sync never overwrites.
Details: spec section 21.

## Deploying

Production runs on the MakonBook VPS behind the existing edge nginx, with its
own database in the shared Postgres. Step-by-step guide, compose file, nginx
blocks and backup script: [`deploy/`](deploy/DEPLOY.md). Images are built by
`.github/workflows/docker.yml` and pushed to Docker Hub as
`nazacd/makonbook-api`.

## Secrets

Never commit `.env`, the HolliHop auth key, the Telegram bot token or database
passwords. Production values live only in the server's environment.
