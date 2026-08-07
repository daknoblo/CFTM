# CFTM — Cloudflare Tunnel Monitor

[![CI](https://github.com/daknoblo/CFTM/actions/workflows/ci.yml/badge.svg)](https://github.com/daknoblo/CFTM/actions/workflows/ci.yml)
[![Release](https://github.com/daknoblo/CFTM/actions/workflows/release.yml/badge.svg)](https://github.com/daknoblo/CFTM/actions/workflows/release.yml)
[![Go](https://img.shields.io/badge/go-1.26-00ADD8)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A self-hosted dashboard for Cloudflare Tunnels. It polls the Cloudflare API,
keeps its own history in SQLite and shows what the Zero Trust dashboard does
not: uptime over time, connector detail, configuration drift and which public
hostnames are actually protected by Access.

## What it shows

- **Tunnel health** — status (`healthy` / `degraded` / `down` / `inactive`),
  uptime over 24 hours, 7 days and 30 days, plus a heartbeat bar that keeps
  short outages visible instead of averaging them away.
- **Connectors** — every running `cloudflared` instance with its version,
  architecture, feature flags and the configuration version it has applied.
- **Edge connections** — the four QUIC connections a healthy tunnel maintains,
  including the Cloudflare data center and origin IP of each.
- **Ingress inventory** — every public hostname with its origin service and
  `originRequest` settings.
- **Access audit** — which hostnames have no Access application, which policies
  allow bypass or a service token, and when service tokens expire.
- **Event log** — status transitions, connectors coming and going,
  configuration changes and `cloudflared` version changes.

Beyond the raw API status it derives nine health signals: fewer than four edge
connections, all connections in a single data center, outdated `cloudflared`,
stale configuration version, flapping, `noTLSVerify`, `localhost` origins,
unprotected hostnames and expiring service tokens.

## Quick start

```sh
cp deploy/docker-compose.example.yml docker-compose.yml
cp deploy/.env.example .env
$EDITOR .env          # CLOUDFLARE_API_TOKEN and CLOUDFLARE_ACCOUNT_ID
docker compose up -d
```

The dashboard listens on `127.0.0.1:8080`. Put it behind Cloudflare Access
rather than exposing it — it displays tunnel configuration and origin IPs.

`deploy/docker-compose.full.example.yml` lists every available setting.

## API token

Create a **dedicated read-only token**; do not reuse a token that can write DNS
or Access configuration.

| Scope | Permission |
| --- | --- |
| Account : Cloudflare Tunnel | Read |
| Account : Access: Apps and Policies | Read |
| Account : Access: Service Tokens | Read |

See [docs/cloudflare-token.md](docs/cloudflare-token.md).

## Documentation

- [Installation](docs/installation.md)
- [Configuration](docs/configuration.md)
- [Architecture](docs/architecture.md)
- [Development](docs/development.md)
- [Cloudflare API token](docs/cloudflare-token.md)

## API budget

Cloudflare allows 1200 requests per five minutes per user, counted cumulatively
across the dashboard and every token. At the default 30-second interval CFTM
uses roughly 3.7 % of that budget, leaving room for Terraform pipelines and
normal dashboard use. The client reads the `Ratelimit` response headers and
stops sending once a configurable reserve is reached.

## Disclaimer

This is a personal side project, provided as is under the MIT license. It is
not affiliated with or endorsed by Cloudflare.
