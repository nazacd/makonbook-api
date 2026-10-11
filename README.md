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

## Getting started

Requirements: Go 1.27+, Docker (for the local database), `make`.

```bash
cp .env.example .env
make db-up          # Postgres 18 in Docker, plus a makonbook_test database
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
| `TIMEZONE` | `Asia/Tashkent` | School time zone; defines "today" for schedules |
| `HOLLIHOP_BASE_URL` | — (worker/sync) | e.g. `https://school.t8s.ru/Api/V2` |
| `HOLLIHOP_AUTHKEY` | — (worker/sync) | Secret. Never logged. |
| `SYNC_INTERVAL` | `15m` | Between incremental syncs |
| `SYNC_FULL_AT` | `03:00` | Local time of the nightly full sync |
| `TELEGRAM_BOT_TOKEN`, `TELEGRAM_ALERT_CHAT_ID` | — | Optional, set both: sync alerts go to this chat instead of only the log |
| `TEST_DATABASE_URL` | — | Tests only; database tests are skipped without it |

## Layout

```
cmd/makonbook/         entry point and subcommands
api/openapi.yaml       API contract for the mobile app and admin panel
db/migrations/         goose SQL migrations, embedded in the binary
internal/
  app/                 wires features into one HTTP handler
  platform/            shared plumbing: config, logging, postgres, migrate, httpx, alert, testdb
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

## Secrets

Never commit `.env`, the HolliHop auth key, the Telegram bot token or database
passwords. Production values live only in the server's environment.
