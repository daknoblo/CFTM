# Cloudflare API token

CFTM needs a **dedicated read-only token**. Do not reuse a token that manages
DNS, Access or notifications: a monitor has no reason to hold write access.

## Create the token

1. Open **My Profile → API Tokens → Create Token → Create Custom Token** in the
   Cloudflare dashboard.
2. Name it something recognizable, for example `cftm-monitor`.
3. Add these permissions:

| Type | Scope | Permission |
| --- | --- | --- |
| Account | Cloudflare Tunnel | Read |
| Account | Access: Apps and Policies | Read |
| Account | Access: Service Tokens | Read |
| Account | Notifications | Read (optional) |

4. Restrict **Account Resources** to the single account CFTM should watch.
5. Optionally restrict the token by client IP.

`Cloudflare Tunnel : Read` alone is enough to run the dashboard; the two Access
scopes only enable the audit page. Without them, set
`CFTM_ACCESS_AUDIT_ENABLED=false`.

`Notifications : Read` lets CFTM check whether Cloudflare's own alerting would
tell you about a tunnel going down. Without it that check stays silent rather
than guessing — monitoring a tunnel and being told when it breaks are two
different things, and the gap is invisible until the outage nobody hears about.

## Account ID

The account ID is shown in the dashboard URL and on the account overview page.
It is not a secret, but CFTM reads it from the environment for symmetry with
the token.

## Access service token (optional)

End-to-end probing needs an Access **service token**, which is a separate
credential from the API token. One token has to be accepted by every
application you want probed, because a request can only carry one pair of
headers.

### Managed by Terraform

If the account is managed by
[cloudflare-config](https://github.com/daknoblo/cloudflare-config), the token
and its policy already exist. Switch it on and read the credentials:

```hcl
# infra/terraform.tfvars
monitor_service_token = true
```

```sh
cd infra
terraform output -json monitor_service_token \
  | jq -r '"CFTM_ACCESS_CLIENT_ID=" + .client_id,
           "CFTM_ACCESS_CLIENT_SECRET=" + .client_secret' \
  >> /path/to/cftm/.env
```

See `docs/monitoring.md` in that repository for the trade-off and the rotation
procedure.

### By hand

1. **Zero Trust → Access → Service Auth → Create Service Token**.
2. Copy the client ID and client secret; the secret is shown only once.
3. Set `CFTM_ACCESS_CLIENT_ID` and `CFTM_ACCESS_CLIENT_SECRET`.
4. For every application that should be probed end to end, add a policy with
   action **Service Auth** that includes the token.

> **Security note.** A service token admitted by every application can reach
> every origin behind those applications. Treat it like a production
> credential: use it only for CFTM, keep it in the environment, and rotate it
> before it expires. CFTM watches its own token's expiry on the audit page and
> raises a finding 30 days out.

## Rotation

Both credentials can be replaced without downtime: create the new one, update
the environment, restart the container, then delete the old one. Service tokens
expire one year after creation by default.
