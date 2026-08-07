# Development

## Prerequisites

- Go 1.26
- `templ` CLI and the standalone Tailwind CLI, both installed by `make tools`
- `golangci-lint` v2 (`brew install golangci-lint`)

```sh
make tools                     # installs the templ CLI
make tailwind                  # downloads the Tailwind CLI into ./bin
```

## Everyday commands

```sh
make all        # templ generate + Tailwind + build
make generate   # regenerate *_templ.go from *.templ
make css        # recompile internal/web/assets/static/app.css
make build      # static binary at bin/cftm
make test       # go test -race ./...
make lint       # golangci-lint run
make vet
make docker
```

`internal/web/*_templ.go` and `internal/web/assets/static/app.css` are
**committed**, and CI fails if they are stale. Run `make generate css` and
commit the result after touching a `.templ` file or `input.css`.

## Running locally

```sh
CLOUDFLARE_API_TOKEN=... \
CLOUDFLARE_ACCOUNT_ID=... \
CFTM_DATA_DIR=./appdata \
CFTM_ADDR=127.0.0.1:8080 \
./bin/cftm
```

`CFTM_DATA_DIR` and `CFTM_ADDR` exist for this case only. In a container both
are fixed by the image (`/appdata` and `:8080`), so they are not part of the
documented configuration surface — remap the host port instead.

The app starts even when the API is unreachable: the dashboard renders with an
error banner instead of crashing, which is also covered by a test.

## Conventions

- Standard library first: `net/http.ServeMux` with Go 1.22 routing patterns,
  `log/slog`, `os.Getenv`. No router, logging or configuration framework.
- SQLite via `modernc.org/sqlite`, so `CGO_ENABLED=0` keeps working.
- Tests use only `testing` and `net/http/httptest`. No testify, no mocks —
  packages are exercised through real HTTP stubs and a temporary database.
- Tests live next to the code in the same package.
- Comments explain what the code cannot; one line is usually enough.

## Testing approach

| Package | How it is tested |
| --- | --- |
| `cloudflare` | `httptest` server with real API fixtures: pagination, 429, budget guard, envelope errors |
| `store` | Temporary SQLite file; uptime is verified against a hand-computed figure |
| `collector` | Fake Cloudflare API; asserts persisted snapshots, emitted events and that the configuration fetch is skipped when nothing changed |
| `prober` | `httptest` with a proxy transport so any hostname resolves locally; covers classification, redirect handling, hostname validation and secret redaction |
| `server` | Full handler stack: every route, security headers, ETag revalidation, JSON payloads and a credential-leak check |

## Adding a health signal

1. Add the code constant and the check to `internal/collector/health.go`.
2. Add a case to `internal/collector/health_test.go`.
3. Document it in the table in `docs/architecture.md`.

`Evaluate` is pure, so no plumbing is required — the dashboard and the tunnel
page pick the finding up automatically.

## Releasing

Push to `main` to publish `:latest`. Tag `vX.Y.Z` to publish the semver tags.
There is no changelog automation; commit messages loosely follow Conventional
Commits.

## Demo site and screenshots

`cmd/cftm-demo` renders the UI from fabricated data. It drives the real
collector against an in-process stub of the Cloudflare API, so the demo cannot
show anything the production code path would not produce.

```sh
go run ./cmd/cftm-demo -addr 127.0.0.1:8099   # browse it locally
go run ./cmd/cftm-demo -out ./site -base /CFTM # export a static site
```

The export walks every page through the real handler, writes `index.html` per
route, rewrites root-absolute links onto the base path and drops the htmx
bundle and the forms — without a server there is nothing to poll or post to.

The `Demo` workflow publishes the export to GitHub Pages and regenerates
`docs/screenshots/` with Playwright, committing them back so the README always
matches the current UI.
