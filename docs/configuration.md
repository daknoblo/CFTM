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

## Access logins

| Variable | Default | Description |
| --- | --- | --- |
| `CFTM_ACCESS_LOGINS_ENABLED` | `false` | Summarize the Access authentication log |
| `CFTM_ACCESS_LOGINS_INTERVAL` | `1h` | How often the summary is rebuilt |
| `CFTM_ACCESS_LOGINS_WINDOW` | `24h` | How far back each collection looks |

Needs `Account : Access: Audit Logs : Read` on the token, which nothing else in
CFTM uses. It is off by default for that reason.

The raw log can hold tens of thousands of entries a day, so only a summary is
kept: granted and denied counts plus distinct users per application, replaced
on every collection. Distinct users rather than events, because one person
reloading a page all day is not heavy usage.

Denials are the interesting number. A policy doing its job and a policy that is
wrong look identical from the outside, and both are worth knowing about.

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

Hostnames **without** an Access application need no token at all. Responses
served from Cloudflare's cache do not verify the origin. The service token is deliberately withheld from
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
| `edge_cached` | Cloudflare served/revalidated a cached response; excluded from tunnel latency statistics |
| `tunnel_down` | No connector available (Cloudflare error 1033) |
| `origin_error` | Tunnel up, origin unreachable (502, 504, 520–524) |
| `timeout`, `dns_error`, `tls_error`, `http_error` | Transport failures |

A 404 or 500 from the application counts as `ok`: the tunnel delivered the
request, what the application answers is not the monitor's business.

### Response time and stability

Each tunnel detail page has a 24-hour chart below the availability monitor.
Selecting an HTTP/HTTPS hostname switches the chart immediately; hostnames are
never averaged together. Without JavaScript, use the fallback **Apply** button.
The panel refreshes from SQLite every ten seconds without making
Cloudflare API calls or triggering probes.

Enable `CFTM_PROBE_ENABLED=true` to collect measurements. The default interval
remains `5m`; `CFTM_PROBE_INTERVAL=60s` gives finer sampling at the cost of more
requests to each application. No historical samples can be reconstructed when
probing was disabled.

- Response time runs from request creation until response headers arrive,
  including connection setup when needed and application processing.
- Median and nearest-rank P95 use all `ok` samples in the last 24 hours.
- Mean variation is the average absolute change between consecutive `ok`
  samples, not network jitter. Failures, excluded responses and gaps longer
  than 1.5 configured probe intervals break pairs and chart connections.
- The chart uses at most 288 five-minute buckets. Lines show means and vertical
  whiskers preserve minimum/maximum spikes. Short interruptions inside a bucket
  are marked and prevent connections to adjacent buckets.
  Curves round the corners without changing values, overshooting peaks or
  bridging gaps. The fine grid marks hours and ten vertical scale divisions.
  Hover over a bucket to see its time range in your browser's local timezone,
  response mean/min/max, variation and check counts. Keyboard users can focus
  the chart and inspect buckets with the arrow keys; Escape hides the tooltip.
- Failed-check percentage is failures / (`ok` + failures). Access challenges,
  denials, known cache responses and unknown classes are excluded. Timeouts are
  markers, not latency values. Missing checks do not count as packet loss.
- No data is shown as a gap or an unavailable statistic, never as zero.
  A last sample older than 1.5 intervals is marked stale. Changing the interval
  also changes the gap threshold used to display existing history.

`CF-Cache-Status` values `HIT`, `STALE`, `UPDATING` and `REVALIDATED` are classified
as `edge_cached`, not `ok`. Older stored `ok` samples cannot retrospectively be
checked for cache hits. Responses without recognizable cache/Access headers
may still be served by an intermediary; this is an HTTP observation, not proof
of physical-link quality. History follows the hostname's current tunnel
assignment; it cannot attribute a request to a particular connector.

The measurement includes the CFTM host's route to Cloudflare. Real link RTT,
network jitter and packet loss require a separate measurement source at the
tunnel site.

## Request origins

| Variable | Default | Description |
| --- | --- | --- |
| `CFTM_ORIGINS_ENABLED` | `false` | Collect which countries requests came from |
| `CFTM_ORIGINS_INTERVAL` | `1h` | How often the summary is rebuilt |
| `CFTM_ORIGINS_WINDOW` | `24h` | How far back each collection looks |
| `CFTM_EXPECTED_COUNTRIES` | — | Comma-separated country codes that are normal here |

Only aggregated country codes are stored. Client IP addresses are personal data
and answer no question the country does not, so they are discarded.

Two sources are read, because they see different traffic. The zone-scoped HTTP
analytics covers every proxied request, including traffic an Access **bypass**
policy waves through — bypass is never logged as an Access event. The Access
login analytics covers authentication attempts, including the non-identity ones
a country policy produces. Either may be unavailable without stopping the other.

`CFTM_ORIGINS_WINDOW` is an upper bound. Cloudflare limits how far back a plan
may be queried, and a Free zone keeps only a few days, so the window is
shortened to what the plan allows and the effective period is shown alongside
the figures.

Cloudflare samples these datasets once the request volume grows. Sampled
figures are estimates and are marked as such rather than scaled up, which would
invent precision the API did not provide.

`CFTM_EXPECTED_COUNTRIES` decides what counts as normal. A country outside that
list appearing for the first time is recorded in the event log at `info`, below
the default notification threshold, so it is visible without paging anyone.
