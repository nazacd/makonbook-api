# MakonBook admin panel

React + TypeScript + TanStack Query, built with Vite. The API embeds the
build (`embed.go`) and serves it on `ADMIN_HOST` with `/v1/admin/*`, so
page and API share one origin (spec 13, `deploy/nginx-admin.conf`).

```bash
npm ci
npm run dev        # http://localhost:5173, proxies /v1 to the API on :8080
npm run build      # into dist/app (what the API embeds)
npm run gen        # after changing api/openapi.yaml: regenerate src/api/schema.d.ts
npm run lint && npm run typecheck
```

Signing in needs `HOLLIHOP_BASE_URL` and `HOLLIHOP_AUTHKEY` on the local API
(HolliHop checks the password). Or serve the built panel from the API itself:
`ADMIN_HOST=localhost make run`, then http://localhost:8080.

- API calls go through `src/api/hooks.ts`; types come from the OpenAPI file,
  so a contract change shows up as a type error here.
- Every request carries `X-MakonBook-Admin` (CSRF; see `src/api/client.ts`).
- Times are shown in the school's time zone (`src/lib/dates.ts`).
