# eshu-collector-pagerduty

Status: pre-release. This collector is not yet the live PagerDuty producer for Eshu; Eshu core still owns admission and storage.

This collector reads PagerDuty incident context for Eshu and returns it as facts (small, checked records Eshu stores as evidence).

## What it collects

For each work request, the collector reads one PagerDuty account through bounded REST calls: recent incidents, their timeline entries and related changes, and — when enabled — live service and integration configuration.

`manifest.yaml` declares 6 fact kinds. Each kind uses schema version `1.0.0` and source confidence `reported` (the provider itself reported the value).

| Fact kind | What it records | PagerDuty read behind it |
| --- | --- | --- |
| `incident.record` | One incident | `GET /incidents` |
| `incident.lifecycle_event` | One incident timeline entry | `GET /incidents/{id}/log_entries` |
| `change.record` | One change related to an incident | `GET /incidents/{id}/related_change_events` |
| `incident_routing.observed_pagerduty_service` | One live PagerDuty service | `GET /services`, `GET /services/{id}` |
| `incident_routing.observed_pagerduty_integration` | One live service integration | `GET /services/{id}/integrations` |
| `incident_routing.coverage_warning` | A gap: bound hit, hidden or missing service, unusable record | Any read above that could not finish cleanly |

No Result, status, or log line carries a provider response body, a token, or a URL with credentials.

Service and integration reads happen only when `config_validation_enabled` is set, or for services named in `allowed_service_ids`. Incident reads always run.

## How data flows

```mermaid
flowchart LR
    A[PagerDuty REST API] --> B[this collector]
    B --> C[Eshu extension host]
    C --> D[Eshu core]
```

Text version: PagerDuty REST API → this collector → Eshu extension host → Eshu core. The host hands the collector one claim (one bounded unit of work), validates the result, and owns claims, leases (timed work ownership), and commits. Core owns admission, projection, and persistence.

## When to use it

Use it when Eshu needs incident context — what incidents happened, how they moved, which changes were related, and how services route — as evidence next to code and deployment facts.

Do not use it to page people, ack or resolve incidents, or replace PagerDuty. It only reads; it writes nothing to PagerDuty.

## How Eshu runs it

The Eshu component-extension host runs this program with its `process` runner:

- The host writes one bounded `collector-sdk/v1alpha1` Request to standard input (stdin).
- The collector reads the PagerDuty REST API and writes one `collector-sdk/v1alpha1` Result to standard output (stdout).
- With no arguments, or with `--sdk-stdio`, the binary serves one claim this way. That is the only serving mode.

Local smoke run with a recorded request:

```bash
collector --sdk-stdio < request.json > result.json
```

### Image and seed

The release image is `ghcr.io/eshu-hq/eshu-collector-pagerduty`. It holds one static binary at `/collector` (note the path — not `/usr/local/bin/collector`) on a distroless base with no shell, running as user `65532:65532`.

The binary installs itself for the host. An init container (a setup container that runs before the main one) runs:

```bash
/collector seed --dest DIR [--image-ref REPO@sha256:DIGEST]
```

Seed writes `DIR/collector` (byte copy, mode 0755) and `DIR/manifest.yaml` (mode 0644). Re-running it is safe: correct files are left alone and changed files are replaced atomically. The host's `process.command` then points at the seeded `collector`.

`--image-ref`, or the `COLLECTOR_IMAGE_REF` environment variable (the flag wins), stamps the deployed digest into the seeded manifest, because an image cannot embed its own digest. The value must be `repository@sha256:<64 hex digits>` (one trailing newline is trimmed); anything else exits non-zero, writes nothing, and never echoes the value. Without it, the repo placeholder (all-zero digest in `manifest.yaml`) is written unchanged. The host runs the seeded binary; it does not pull this image per claim.

Manifest identity: `dev.eshu.collector.pagerduty`, version `0.1.4`, collector kind `pagerduty`, platform `linux/amd64`, compatible core `>=0.0.5 <0.2.0`.

### Grants

All 6 kinds are core-owned, so the host refuses to activate the component without a live producer grant (permission to emit a core-owned kind) per kind. `grant.example.json` lists the 6 grants (scope `pagerduty`, version `0.1.4`, schema `1.0.0`). A grant's scope is the collector kind (`pagerduty`), not an account scope ID.

The install flow uses allowlist trust today: the release does a plain keyless `cosign sign` with no Eshu trust annotations, so strict trust (which verifies those annotations) is not supported yet.

## Configuration

The host reads an activation file. `host` and `process` configure the host; everything under `config` reaches this collector. `config.example.yaml` is a complete example for one account with placeholder values.

| Key | Meaning |
| --- | --- |
| `scope_id` | Required. Must equal `host.scope.id`; a claim for another scope fails terminal. |
| `account_id` | Required. PagerDuty account identifier for this activation. |
| `token_env` | Required. **Name** of the environment variable holding the API token (example: `ESHU_PAGERDUTY_API_TOKEN`). The token itself is never in the file. |
| `api_base_url` | Optional. Default `https://api.pagerduty.com`. Plain `http` is accepted only for a loopback host. |
| `source_uri` | Optional credential-free fallback source reference. |
| `incident_lookback` | How far back to read incidents (example and default: `6h`). |
| `incident_limit`, `log_entry_limit`, `change_event_limit` | Page sizes, `0` to `100` (`0` selects the provider default). |
| `pagination_max_pages`, `pagination_max_records` | Bounds per list (example: `10` pages, `1000` records). Hitting a bound with more available emits a truncation warning. |
| `allowed_service_ids` | Optional. Read only these services instead of every service. |
| `config_validation_enabled` | Also read live service and integration configuration. |
| `config_resource_limit` | Page size for configuration reads (example: `100`). |
| `max_result_bytes` | Fail terminal with `result_too_large` above this many encoded bytes (example and default: `3500000`). |
| `allow_empty_successor` | Parsed but **unsupported**: setting it fails closed with `unsupported_option`. See Known limits. |

`host.sourceSystem` is `pagerduty` and `host.scope.kind` is `pagerduty_account` in the example. Scope IDs are part of stable fact keys, so choose them once and keep them.

### Credentials

The API token travels by environment-variable name only: `token_env` names the variable the collector process reads at claim time. A name that resolves to no value fails as `credential_missing`. The token travels in a request header and never appears in facts, logs, or statuses.

Give the token only the read access the `GET` endpoints above need. This repo does not name PagerDuty ability scopes or API key types, because the code does not define them.

## Claim budget and operator signals

One claim has a hard deadline: the host kills the process at the initial lease expiry (worker lease setting `ESHU_COMPONENT_COLLECTOR_CLAIM_LEASE_TTL`, named in `config.example.yaml`). The collector stops starting provider calls `min(2s, 5% of remaining time)` before that deadline and returns retryable `claim_deadline_exceeded` with no facts, never partial, so an incomplete read is not committed. Reads are sequential (two calls per incident, plus per-service calls when config validation is on), so a large account can need a lease longer than the 60s worker default the example warns about. The example suggests at least the 5m the reference deployment used. No timing proof against a production-sized account exists yet.

Result states an operator will see:

| State | Meaning |
| --- | --- |
| `complete` | Every list ended on its own. |
| `partial` | Facts plus at least one coverage warning (bound hit, hidden or missing service, unusable record). Facts are still committed. |
| `retryable` | Try again later: `rate_limited` (HTTP 408, 409, 425, 429), `retryable` (5xx, transport, malformed body), `claim_deadline_exceeded`, `canceled`. Carries `retry_after_seconds`. |
| `terminal` | Stop: `auth_denied` (401, 403), `not_found` (404), other 4xx, or config problems such as `config_invalid`, `credential_missing`, `unsupported_option`, `scope_not_configured`, `invalid_claim`, `result_too_large`, `fact_invalid`. |

The collector fills in fact count, warning count, and source latency; the host records the failure class from the result. The collector also writes one `collector health: {...}` line per claim to stderr for local runs and tests; the host process runner keeps only a byte count of stderr and drops the text. To find the cause behind a failure class, run the collector locally with the activation's config and a recorded request.

The manifest declares `telemetry.metricsPrefix: eshu_dp_pagerduty_collector_`, but nothing in this repo emits a metric under that prefix and there is no metrics endpoint. The claim result's failure class is the operator signal today.

### Known limits

- `allow_empty_successor` fails closed until the collector SDK carries a trusted-deletion signal. Without it, the ingestion store refuses to replace a content-bearing generation with an empty or sharply smaller one.
- `internal/stableid` and `internal/httpx` are copies of helpers the public SDK does not publish yet; they will be replaced with published SDK helpers when available.

## Eshu dependencies

Exact pins from `go.mod` in this repo. No `replace` directives.

| Module | Pin |
| --- | --- |
| `github.com/eshu-hq/eshu-sdk/collector` | `v0.2.0-rc.1` |
| `github.com/eshu-hq/eshu-sdk/factschema` | `v0.1.0-rc.5` |
| `gopkg.in/yaml.v3` | `v3.0.1` |

Go version in `go.mod`: `1.26.10`.

## What it never does

- It never writes to PagerDuty: no paging, no ack, no resolve.
- It has no Postgres, graph, queue, or lease handle, and never imports Eshu core internals (`boundary_test.go` enforces this).
- It never builds the durable fact envelope or commits facts; the host does.
- It never decides claim fate beyond the bounded failure class it reports.
- It never emits kinds outside the 6 in `manifest.yaml`.

## Build, test, and release

```bash
make ci     # lint + test + race
make lint   # vet, golangci-lint, format diff, actionlint, tidy diff, 500-line cap
make test   # go build ./... and go test ./... -count=1
make race   # race tests plus coverage floor (90.8%)
docker build -t eshu-collector-pagerduty:local .
```

This Makefile has no separate `actionlint` target; `make lint` runs it. `testdata/README.md` explains the parity fixtures; `go test -run TestResultFixturesAreCurrent -update .` rewrites `testdata/fixtures` after a deliberate change.

Releases run from `.github/workflows/release.yml` on pushes to `main` and on `v*` tags. Nothing publishes from a pull request and the workflow deploys nothing. A `main` push publishes moving `:main` and `:latest` plus immutable `:sha-<long-sha>`; a version tag must match `metadata.version` in `manifest.yaml`, be `vMAJOR.MINOR.PATCH` with an optional pre-release suffix, and point at the current head of `main`. The workflow builds `linux/amd64`, pushes `ghcr.io/eshu-hq/eshu-collector-pagerduty:<tag>`, signs the digest with cosign (keyless), attests SLSA provenance and an SPDX SBOM (signed build records), verifies all three, and checks the package can be pulled anonymously. The GHCR package must be public. Image digests are not reproducible across attempts, so use the digest from the last successful job attempt.

Verify a release before using it:

```bash
cosign verify ghcr.io/eshu-hq/eshu-collector-pagerduty@sha256:DIGEST \
  --certificate-identity https://github.com/eshu-hq/eshu-collector-pagerduty/.github/workflows/release.yml@refs/tags/vX.Y.Z \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
gh attestation verify oci://ghcr.io/eshu-hq/eshu-collector-pagerduty@sha256:DIGEST \
  -R eshu-hq/eshu-collector-pagerduty
```

## Docs and related repos

- [eshu](https://github.com/eshu-hq/eshu) — Eshu core and the extension host that runs this collector.
- [eshu-sdk](https://github.com/eshu-hq/eshu-sdk) — the collector and fact-schema modules pinned above.
- [eshu-deploy](https://github.com/eshu-hq/eshu-deploy) — deployment for Eshu components.

Local files: [config.example.yaml](config.example.yaml), [grant.example.json](grant.example.json), [manifest.yaml](manifest.yaml), [Dockerfile](Dockerfile), [testdata/README.md](testdata/README.md), and [CONTRIBUTING.md](CONTRIBUTING.md).

## Contributing and license

See [CONTRIBUTING.md](CONTRIBUTING.md). License: MIT — see [LICENSE](LICENSE). This repo has no `SECURITY.md` file.
