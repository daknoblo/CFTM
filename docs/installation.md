# Installation

## Docker Compose (recommended)

```sh
cp deploy/docker-compose.example.yml docker-compose.yml
cp deploy/.env.example .env
$EDITOR .env
docker compose up -d
docker compose logs -f cftm
```

That file is deliberately short: the two Cloudflare credentials and nothing
else. [docker-compose.full.example.yml](../deploy/docker-compose.full.example.yml)
lists every available setting with its default, for when you want to change
one.

The image is published to `ghcr.io/daknoblo/cftm`:

| Tag | Contents |
| --- | --- |
| `latest` | Current `main` |
| `X.Y.Z`, `X.Y` | Tagged releases |
| `sha-<short>` | A specific commit |

Images are built for `linux/amd64` and `linux/arm64`, signed with cosign
(keyless) and shipped with an SBOM and provenance attestation.

```sh
cosign verify ghcr.io/daknoblo/cftm:latest \
  --certificate-identity-regexp 'https://github.com/daknoblo/CFTM/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## Port binding

The example publishes on `127.0.0.1:8080`. Keep the loopback prefix. Without it
Docker binds `0.0.0.0` and writes its own DNAT rules that bypass the host
firewall, which would expose the dashboard directly to the internet.

The container always listens on `8080`; if that port is taken on the host, map
a different one on the left-hand side:

```yaml
ports:
  - "127.0.0.1:8100:8080"
```

## Exposing it through a tunnel

CFTM has no built-in authentication by design; it expects Cloudflare Access in
front of it. Add a service entry to the tunnel that serves the host, for
example in `sites.auto.tfvars`:

```hcl
cftm = {
  subdomain = "cftm"
  origin    = "http://127.0.0.1:8080"
}
```

Use `127.0.0.1`, never `localhost`: `cloudflared` tries `::1` first and Docker
publishes IPv4 only.

## Persistent data

The container stores `cftm.db` in the `/appdata` volume. Losing it costs the
uptime history and the event log; everything else is re-derived from the API on
the next poll.

## Running without Docker

```sh
make build
CLOUDFLARE_API_TOKEN=... CLOUDFLARE_ACCOUNT_ID=... CFTM_DATA_DIR=./appdata ./bin/cftm
```

## Upgrading

```sh
docker compose pull && docker compose up -d
```

Schema migrations run automatically at start-up and are idempotent.
