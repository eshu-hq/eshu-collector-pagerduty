# Getting started

The extension host normally runs this binary. A smoke run, a seed, and one activation per PagerDuty account get you there.

## 1. Smoke-test it

```bash
collector --sdk-stdio < request.json > result.json
```

One recorded request in, one result out — also the only way the host talks to it.

## 2. Seed the release image

Releases publish `ghcr.io/eshu-hq/eshu-collector-pagerduty`, with the binary at `/collector` (mind the path — it is not `/usr/local/bin/collector` here):

```bash
/collector seed --dest DIR [--image-ref REPO@sha256:DIGEST]
```

The seed contract lives in the [operations guide](../guides/operations.md).

## 3. Configure one account

Copy [config.example.yaml](../../config.example.yaml). `scope_id` must equal `host.scope.id`, `token_env` names the environment variable holding the API token, and service/integration reads stay off until you opt in. Every key is in [Configuration](../reference/configuration.md).

Keep the cost model in your head: two API calls per incident inside your lookback window (6 hours by default). Size the worker lease for a busy account before you widen anything — the [operations guide](../guides/operations.md) does the math with you.

## 4. Grants before activation

All six kinds belong to core, so the host wants a live producer grant per kind. [grant.example.json](../../grant.example.json) lists all six (grant scope is the collector kind, `pagerduty`, not your account scope ID).

## Where next

- How incidents become facts: [Architecture](../architecture/README.md).
- Sizing, signals, and known limits: the [operations guide](../guides/operations.md).
