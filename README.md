# startpage

[![ci](https://github.com/mgrubiyan/startpage/actions/workflows/ci.yml/badge.svg)](https://github.com/mgrubiyan/startpage/actions/workflows/ci.yml)

A self-hosted browser start page with a Go backend: search with live suggestions,
bookmarks, clock, weather, Central Bank of Russia exchange rates and an uptime
monitor for your own services (Telegram bots, APIs, anything with an HTTP endpoint).

The frontend is plain HTML/CSS/JS embedded into the binary. All data comes from
the Go JSON API; the browser doesn't call third-party APIs. The one exception is
bookmark icons, which load from Google's favicon service, so bookmark hostnames
are visible to Google.

![startpage in demo mode: clock, weather, CBR rates, search, bookmarks and the service monitor](docs/screenshot.png)

## Features

- **Search**: default engine plus prefixes (`g`, `y`, `d`, `gh`, `yt`, `w`), address detection,
  suggestions proxied from Google (DuckDuckGo fallback) with keyboard navigation.
- **Weather and rates**: Open-Meteo and the CBR daily feed, cached server-side.
- **Bookmarks and settings**: stored in Postgres.
- **Service monitor**: background prober with latency, last error and 24h uptime per service.
- **Single-user login**: password, server-side sessions, rate-limited.
- **Demo mode**: a public read-only instance with no login, for showing the project.
- **Chrome extension**: makes every new tab open the start page.

## Architecture

```
cmd/startpage            wiring, config, graceful shutdown, `healthcheck` subcommand
internal/config          env config with validation (all errors reported at once)
internal/httpapi         net/http ServeMux routes, middleware, handlers, DTOs
internal/auth            bcrypt password check, session tokens, login rate limiter
internal/cache           generic TTL cache: singleflight, stale-on-error, bounded size
internal/fetch           shared HTTP client + JSON helper with body size limit
internal/weather         Open-Meteo geocoding + forecast client
internal/rates           CBR daily rates client
internal/suggest         search suggestions client (Google → DuckDuckGo fallback)
internal/monitor         ticker-driven prober with a bounded worker pool
internal/demo            monitor targets seeded into an empty demo database
internal/store           domain types
internal/store/postgres  pgx/v5 store, embedded SQL migrations
web/static               frontend, embedded with go:embed
extension                MV3 new-tab extension that redirects to the server
deploy                   VPS setup: Caddy + shared Postgres, app compose, backups
```

Only the standard library is used for HTTP and logging (`net/http` 1.22+ method
patterns, `log/slog`). Third-party dependencies are `pgx/v5`,
`golang.org/x/sync/singleflight` and `golang.org/x/crypto/bcrypt`.

### Design notes

**Upstream caching.** `cache.Cache[V]` wraps every external API.
- Concurrent misses for the same key collapse into one upstream call (`singleflight`).
- The load runs on a context detached from the request (`context.WithoutCancel` plus its own
  timeout), so one impatient client can't cancel a fetch that other waiters share.
- Errors are never cached. If a refresh fails while an expired value exists, the stale value
  is served with `"stale": true` and the UI marks it.
- After a failed refresh, the stale value is served immediately for 30s, so a dead upstream
  doesn't make every request wait out the timeout.
- Suggestions give each provider its own timeout, so a hanging Google still leaves time for
  the DuckDuckGo fallback.
- Entry count is bounded; weather is keyed by city, suggestions by query.

**Auth.**
- The password from `STARTPAGE_PASSWORD` is stored only as a bcrypt hash. At startup the app
  compares it with the stored hash. If the password was changed, every session is deleted, so a
  leaked password stops working for anyone already logged in once it is rotated.
- A session is 32 random bytes sent as an `HttpOnly`, `SameSite=Lax` cookie (`Secure` is configurable).
  Only its SHA-256 is stored, so a database leak doesn't hand out live sessions.
  Expired sessions are pruned on login.
- Login is rate-limited per client IP (5/min). `X-Forwarded-For` is honoured only with
  `STARTPAGE_TRUST_PROXY=true`, and then only its last entry, which the proxy appended.
  Never enable it while the app port is reachable directly, because clients could then pick
  their own IP. The limiter tracks at most 10k clients; past that it refuses new ones rather
  than grow.
- API responses carry `Cache-Control: no-store`.
- Routes are declared in one table with an explicit `public` flag. A test walks that table and
  asserts that every private route returns 401 without a valid cookie, so a new endpoint can't
  ship unauthenticated by accident.

**CSRF.**
- Mutating requests must be `application/json`, which a cross-site form can't send without a
  CORS preflight.
- The session cookie is `SameSite=Lax`.

**Hardening.**
- A strict CSP: `script-src 'self'`, no inline scripts.
- JSON 404/405 for API routes.
- 64 KiB request body limit; unknown JSON fields rejected.
- Timeouts on the HTTP server and on every outbound call.

**Monitor.**
- Each round probes all services through a pool of 4 workers.
- Each probe has a 5s timeout, follows at most 3 redirects and reads at most 64 KiB of body.
- 2xx/3xx counts as up.
- Adding or removing a service triggers a round immediately.
- Results are written in one batch.
- Check history older than 7 days is pruned hourly.
- On shutdown, an interrupted round is dropped rather than recorded as a burst of false failures.

**Migrations.** Plain numbered `.sql` files embedded in the binary.
- Applied at startup under `pg_advisory_lock`, so several replicas can start at once.
- Each migration runs in its own transaction.

### Monitor and SSRF

The monitor fetches URLs that the user enters, which is server-side request forgery by
definition. It deliberately **allows** private and loopback addresses, because watching your
own bots on `localhost` or a LAN is the whole point. This is acceptable because:

- only the single authenticated owner can add services;
- probes are `GET` only, and the response body is discarded, never shown back. The UI gets
  just status, latency and a short error string.

If you expose startpage to other users, add an address allow-list in `internal/monitor`
before doing so.

### Demo mode

`STARTPAGE_DEMO=true` turns an instance into a public showcase:

- No login and no password. Every `GET` route is open, and every write, login included,
  answers `403`. The policy lives in one place (`guard` in `internal/httpapi/server.go`), and a
  test walks the route table to prove that no write gets through.
- Because nobody can add services, the SSRF concern above doesn't apply. On an empty database
  the demo seeds its own monitor targets (`internal/demo`), including one that is always down.
- Search suggestions are limited to 60 requests per minute per IP, so the demo can't be used as
  a free suggestions proxy. Behind a reverse proxy, set `STARTPAGE_TRUST_PROXY=true` (and keep
  the app port private), or every visitor shares the proxy's IP and one limit.
- `STARTPAGE_DEMO` together with `STARTPAGE_PASSWORD` is a config error, so a real instance
  can't be turned into a public one by flipping a flag.
- The frontend reads `GET /api/meta`, shows a banner and hides the edit controls.

Run the demo as a separate instance with its own database, never against your real data.

## API

All endpoints except login, logout and meta require the session cookie (in demo mode, reads
are open and writes are refused). JSON in, JSON out.

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/login` | `{"password": "..."}` → sets the session cookie (204) |
| `POST` | `/api/logout` | deletes the session |
| `GET` | `/api/meta` | `{"demo": false}`: how the server runs (public) |
| `GET` / `PUT` | `/api/settings` | `{"searchEngine": "g", "city": "Санкт-Петербург"}` |
| `GET` / `POST` | `/api/bookmarks` | list / add `{"title", "url"}` |
| `DELETE` | `/api/bookmarks/{id}` | |
| `GET` / `POST` | `/api/services` | list with last check and 24h uptime / add `{"name", "url"}` |
| `DELETE` | `/api/services/{id}` | |
| `GET` | `/api/weather` | current weather for the configured city |
| `GET` | `/api/rates` | USD and EUR from the CBR, with previous values |
| `GET` | `/api/suggest?q=` | search suggestions |
| `GET` | `/healthz` | liveness + database ping (public) |

## Running

### Docker Compose

```sh
cp .env.example .env    # set STARTPAGE_PASSWORD and POSTGRES_PASSWORD
docker compose up -d --build
open http://localhost:8080
```

The app is published on `127.0.0.1` only. To watch a service running on the Docker host, use
`http://host.docker.internal:PORT/...` as its URL.

### Locally

```sh
make test-db    # throwaway Postgres 17 in Docker on 127.0.0.1:55432
make run        # password: correct-horse (override with STARTPAGE_PASSWORD)
make run-demo   # the same, in read-only demo mode
```

### Configuration

| Variable | Default | |
|---|---|---|
| `DATABASE_URL` | (required) | Postgres connection string |
| `STARTPAGE_PASSWORD` | (required) | login password, 8 to 72 bytes; must be unset in demo mode |
| `STARTPAGE_DEMO` | `false` | public read-only demo, see [Demo mode](#demo-mode) |
| `STARTPAGE_ADDR` | `:8080` | listen address |
| `STARTPAGE_SECURE_COOKIE` | `false` | set `true` behind HTTPS |
| `STARTPAGE_TRUST_PROXY` | `false` | use the last `X-Forwarded-For` entry as client IP |
| `STARTPAGE_SESSION_TTL` | `720h` | session lifetime |
| `STARTPAGE_MONITOR_INTERVAL` | `1m` | probe interval (min `5s`) |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

### On a server

[deploy/README.md](deploy/README.md) covers a VPS:
- Caddy with automatic TLS;
- one shared Postgres with a role and database per app;
- the personal instance next to the public demo;
- daily backups;
- optional deploys from CI. Images are published to GHCR on every push to `main`.

### New-tab extension

1. Open `chrome://extensions` and enable **Developer mode**.
2. Click **Load unpacked** and pick the `extension/` folder.
3. If the server is not on `http://localhost:8080/`, edit `STARTPAGE_URL` in `extension/newtab.js`.

## Tests

```sh
make test               # unit tests with -race; Postgres tests skip
make test-db            # once
make test-integration   # everything, including Postgres
make lint               # go vet + golangci-lint
```

- Handlers are tested through `httptest` with in-memory fakes.
- Upstream clients are tested against `httptest.Server` fixtures.
- The cache and monitor tests exercise concurrency under `-race`: singleflight collapse,
  stale fallback, shutdown mid-round.
- Postgres tests run against a real database. Each test gets its own schema, so they run in
  parallel.

CI runs vet, the full suite with a Postgres service container, golangci-lint, and a Docker build.
On `main` it also publishes the image to GHCR, and deploys it when a server is configured.

## License

[MIT](LICENSE)
