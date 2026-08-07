# Configuration

Every setting is read from the environment at start-up. Secrets are never read
from a file, never written to the database and never rendered in the UI or the
JSON API.

> A `.env` file next to `docker-compose.yml` is only used by Compose to
> substitute `${...}`. A variable that is not also listed under `environment:`
> in the compose file never reaches the container. Check what actually arrived
> with:
>
> ```sh
> docker inspect cftm --format '{{range .Config.Env}}{{println .}}{{end}}' | grep CFTM_
> ```

## Required

| Variable | Description |
| --- | --- |
| `CLOUDFLARE_API_TOKEN` | Read-only API token, see [cloudflare-token.md](cloudflare-token.md) |
| `CLOUDFLARE_ACCOUNT_ID` | Cloudflare account identifier |

## Server

| Variable | Default | Description |
| --- | --- | --- |
| `CFTM_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |
| `TZ` | UTC | Time zone for log output |

The listen port is fixed at `8080` and the database lives in the `/appdata`
volume. Map the container port somewhere else if 8080 is taken on the host:

```yaml
ports:
  - "127.0.0.1:8100:8080"
```

## Collection

| Variable | Default | Description |
| --- | --- | --- |
| `CFTM_POLL_INTERVAL` | `30s` | Interval between collection cycles, minimum `10s` |
| `CFTM_CONFIG_REFRESH_EVERY` | `10` | Fetch the ingress configuration every N cycles; it is also fetched immediately whenever a connector reports a different configuration version |
| `CFTM_RETENTION_DAYS` | `90` | Age at which history, probe results, events and poll runs are pruned |

The minimum interval is enforced because the Cloudflare quota of 1200 requests
per five minutes is shared with the dashboard and every other token of the same
user. See [architecture.md](architecture.md#api-budget) for the arithmetic.

## Access audit

| Variable | Default | Description |
| --- | --- | --- |
| `CFTM_ACCESS_AUDIT_ENABLED` | `true` | Fetch Access applications, policies and service tokens |
| `CFTM_ACCESS_AUDIT_INTERVAL` | `1h` | How often the inventory is refreshed |
| `CFTM_EXPECTED_PUBLIC` | — | Comma-separated hostnames that are published without Access on purpose |

Without a successful audit the coverage check stays silent rather than
reporting every hostname as unprotected.

`CFTM_EXPECTED_PUBLIC` declares the exceptions. Some clients cannot complete a
browser login — Jellyfin apps on a TV, an RSS reader — so their hostnames are
published with no Access application and the origin's own accounts are the only
thing guarding them. Those are deliberate decisions, and a permanent warning
about them would bury the day a hostname loses its application by accident.

```env
CFTM_EXPECTED_PUBLIC=public-app.example.com,feed.example.com
```

Declared hostnames are **still monitored**: they stay in the ingress inventory,
are still probed, and appear in their own section on the audit page. They just
stop counting as a gap. Anything not listed keeps raising a warning.

## Version drift

| Variable | Default | Description |
| --- | --- | --- |
| `CFTM_RELEASE_CHECK_ENABLED` | `true` | Resolve the newest `cloudflared` release from GitHub |
| `CFTM_RELEASE_CHECK_INTERVAL` | `12h` | How often to check |

The GitHub endpoint is unauthenticated and allows 60 requests per hour per IP,
so the default interval is far below the limit. The result is cached in the
database.

## Notifications

| Variable | Default | Description |
| --- | --- | --- |
| `CFTM_NOTIFY_ENABLED` | `false` | Evaluate events and record them in the outbox |
| `CFTM_NOTIFY_MIN_SEVERITY` | `warning` | `info`, `warning` or `critical` |
| `CFTM_NOTIFY_COOLDOWN` | `1h` | How long the same condition stays quiet after being reported |

**No delivery channel is wired up yet.** With `CFTM_NOTIFY_ENABLED=true` the
dispatcher decides what would be sent, suppresses repeats and writes every
decision to the outbox on the **Notifications** page. That makes the stream
reviewable before committing to a transport — turn it on for a week and see
whether the volume and the choice of events are right.

Config changes, connectors coming up and new tunnels never notify: they are
normal operations and would train you to ignore the channel. Deduplication is
keyed on the condition, not the event, so a flapping tunnel reports once per
cooldown. A recovery has a different target state and therefore always gets
through immediately.

## Origin probing

| Variable | Default | Description |
| --- | --- | --- |
| `CFTM_PROBE_ENABLED` | `false` | Send HTTP requests to the public hostnames |
| `CFTM_PROBE_INTERVAL` | `5m` | Interval between probe rounds |
| `CFTM_PROBE_TIMEOUT` | `10s` | Per-request timeout |
| `CFTM_PROBE_CONCURRENCY` | `4` | Maximum parallel probes |
| `CFTM_ACCESS_CLIENT_ID` | — | Access service token client ID |
| `CFTM_ACCESS_CLIENT_SECRET` | — | Access service token client secret |

Probes only ever request `/` over HTTPS, never follow redirects and only target
hostnames taken from the tunnel's own ingress configuration. `ssh://`, `rdp://`,
`tcp://` and `http_status:` rules are skipped because they cannot be checked
with an HTTP request.

Without a service token, protected hostnames report `access_challenge`: the
Cloudflare edge answered, but the request never traversed the tunnel. That
still proves DNS and the edge work, it just says nothing about the origin.

Hostnames **without** an Access application need no token at all: nothing
intercepts the request, so the origin answers directly and the result is a real
end-to-end check either way. The service token is deliberately withheld from
them — with no Access at the edge to consume it, cloudflared would forward the
credential to the origin like any other header. This only takes effect once an
Access audit has succeeded; until then every target carries the token, because
an empty inventory cannot be told apart from an account with no applications.

### Probe classes

| Class | Meaning |
| --- | --- |
| `ok` | The origin answered; the whole path works |
| `access_challenge` | Access intercepted at the edge, origin untested |
| `access_denied` | The service token is not authorized for this application |
| `tunnel_down` | No connector available (Cloudflare error 1033) |
| `origin_error` | Tunnel up, origin unreachable (502, 504, 520–524) |
| `timeout`, `dns_error`, `tls_error`, `http_error` | Transport failures |

A 404 or 500 from the application counts as `ok`: the tunnel delivered the
request, what the application answers is not the monitor's business.
