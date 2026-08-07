# CFTM — Copilot instructions

CFTM is a Go web service that monitors Cloudflare Tunnels via the Cloudflare
API and renders a server-side dashboard.

## Language and runtime

- Go 1.26, module `github.com/daknoblo/CFTM` (the repository name is uppercase).
- `CGO_ENABLED=0` everywhere; SQLite is `modernc.org/sqlite`, never `mattn/go-sqlite3`.
- Blank-import `_ "time/tzdata"` so `TZ` works on distroless.

## Project structure

- `cmd/cftm/main.go` stays thin: logging, config, store, goroutines, HTTP server.
- Everything else lives under `internal/`. There is no `pkg/`.
- The collector writes to the store; the server only reads from it.

## Dependencies

Standard library first. The only third-party packages are `modernc.org/sqlite`
and `github.com/a-h/templ`. Do not add a router, logging, configuration or
assertion library.

- HTTP: `net/http.ServeMux` with Go 1.22 patterns (`"GET /partials/x"`, `{id}`).
- Logging: `log/slog` plus `internal/logbuf`.
- Config: `os.Getenv` helpers in `internal/config`, prefix `CFTM_`.

## Configuration and secrets

- Every setting comes from the environment. Secrets are read only there.
- `CLOUDFLARE_API_TOKEN`, `CLOUDFLARE_ACCESS_CLIENT_SECRET` and similar must
  never be stored, logged, rendered or serialized. `server_test.go` asserts this.

## HTTP layer

- Middleware chain: `logRequests(securityHeaders(limitRequestBody(csrf(mux))))`.
- CSP keeps `script-src 'self'`; vendor JavaScript into
  `internal/web/assets/static/`, never load from a CDN.
- `GET /healthz` returns 200 without touching any external dependency.
- The binary implements `-healthcheck` for the container HEALTHCHECK.

## Frontend

- `templ` components plus htmx partials with `hx-trigger="every 10s"`.
- Tailwind via the standalone CLI, component classes in `assets/input.css`.
- `internal/web/*_templ.go` and `assets/static/app.css` are committed; CI fails
  when they are stale. Run `make generate css` after editing `.templ` or CSS.

## Cloudflare API

- The quota is 1200 requests per five minutes and is shared with the dashboard
  and every other token. Keep new calls out of the per-cycle path where
  possible, and prefer data already embedded in the tunnel list response.
- Never retry a 429.

## Testing

- Standard library only: `testing` and `net/http/httptest`. No testify, no mocks.
- Tests live next to the code, in the same package.
- Failure messages use the `got, want` phrasing.
- Use `t.Context()`, `t.TempDir()` and `t.Setenv`.

## Definition of done

```sh
gofmt -l .          # empty
go vet ./...
golangci-lint run   # 0 issues
go test -race ./...
make generate css   # then `git diff --exit-code` on generated files
```

## Style

- Comments say what the code cannot; one short line is usually enough. Do not
  restate the next statement or explain the change to a reviewer.
- Do not add error handling for situations that cannot occur.
- Do not introduce abstractions for a single call site.
