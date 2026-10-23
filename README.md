# eshu-collector-pagerduty

Eshu PagerDuty incident-context collector, packaged as an Eshu component
extension.

Pre-release. Holds no producer authority until its reviewed cutover (eshu-hq/eshu#8863).

## What it does

The Eshu component-extension host runs this program with its `process`
runner. For each claim the host writes one bounded `collector-sdk/v1alpha1`
Request to standard input; the program reads PagerDuty's REST API and writes
one `collector-sdk/v1alpha1` Result to standard output. The host builds the
durable envelope, validates the Result, and owns claims, leases, and commits.
This program has no Postgres, graph, queue, or lease handle and never imports
Eshu internals (`boundary_test.go` enforces it).

It emits the same six core fact kinds, with the same stable keys and payloads,
as the in-tree collector it replaces (`go/internal/collector/pagerduty` in
`eshu-hq/eshu`). `testdata/README.md` explains the parity proof.

| Fact kind | Source |
| --- | --- |
| `incident.record` | `GET /incidents` |
| `incident.lifecycle_event` | `GET /incidents/{id}/log_entries` |
| `change.record` | `GET /incidents/{id}/related_change_events` |
| `incident_routing.observed_pagerduty_service` | `GET /services`, `GET /services/{id}` |
| `incident_routing.observed_pagerduty_integration` | `GET /services/{id}/integrations` |
| `incident_routing.coverage_warning` | A bound, permission, or missing-resource gap |

Every kind is schema `1.0.0`, source confidence `reported`, and core-owned: the
host refuses to activate the component without a producer grant for each one.

## Result states

| State | When |
| --- | --- |
| `complete` | Every list ended on its own. |
| `partial` | At least one coverage warning was emitted (a page or record bound was hit while PagerDuty had more, a service was hidden by permissions or missing, or a record was unusable). The facts are still committed. |
| `unchanged` | `previous_freshness_hint` equals the freshness hint of this collection. No facts. |
| `retryable` | `rate_limited` (HTTP 408, 409, 425, 429), `retryable` (5xx, transport, malformed body), `claim_deadline_exceeded`, or `canceled`. The status carries `retry_after_seconds`. |
| `terminal` | `auth_denied` (401, 403), `not_found` (404), `terminal` (other 4xx, redirects), or a config problem: `config_invalid`, `credential_missing`, `unsupported_option`, `scope_not_configured`, `invalid_claim`, `result_too_large`, `fact_invalid`. |

A status holds a failure class and counts only. No Result, status, or log line
carries a provider response body, a token, or a URL with credentials.

## Configuration

An activation file configures the host (`host`, `process`) and this program
(`config`). `config.example.yaml` is a complete example with placeholder
values. The `config` keys mirror the in-tree collector's target settings.

| Key | Meaning |
| --- | --- |
| `scope_id`, `account_id` | Required. `scope_id` must equal `host.scope.id`; a claim for another scope fails terminal. |
| `token_env` | Required. The **name** of the environment variable that holds the API token. The token itself is never in the config. |
| `api_base_url`, `source_uri` | Optional. Default API origin is `https://api.pagerduty.com`. |
| `incident_lookback` | How far back to read incidents. Default `6h`. |
| `incident_limit`, `log_entry_limit`, `change_event_limit`, `config_resource_limit` | Page sizes, 0 to 100. |
| `pagination_max_pages`, `pagination_max_records` | Bounds per list (defaults 10 and 1000; ceilings 100 and 5000). |
| `allowed_service_ids` | Read only these services. |
| `config_validation_enabled` | Also read live service and integration configuration. |
| `previous_freshness_hint` | Optional. Return `unchanged` when the collection matches this hint. |
| `max_result_bytes` | Fail terminal above this many encoded bytes (default 3500000, under the host's 4 MiB output limit). |
| `allow_empty_successor` | Parsed but **unsupported**: see Known limits. |

## Run it with Eshu

The component lifecycle, using the `eshu` CLI against a component home:

```sh
eshu component grant dev.eshu.collector.pagerduty --component-home "$HOME_DIR" \
  --version 0.1.0 --kind incident.record --schema-version 1.0.0 \
  --scope pagerduty --expires-in 8760h        # repeat for each kind in grant.example.json
eshu component install manifest.yaml --component-home "$HOME_DIR" \
  --trust-mode allowlist --allow-id dev.eshu.collector.pagerduty --allow-publisher eshu-hq
eshu component enable dev.eshu.collector.pagerduty --component-home "$HOME_DIR" \
  --instance pagerduty-example --mode scheduled --claims --config config.example.yaml
```

A grant's scope is the collector kind (`pagerduty`), not a scope ID.
`grant.example.json` lists the six grants.

## Image and seeding

The image is `ghcr.io/eshu-hq/eshu-collector-pagerduty`: one static binary at
`/collector` on a distroless base, running as `65532:65532`, with no shell. It
is also the delivery artifact. An init container runs:

```sh
/collector seed --dest DIR
```

which writes `DIR/collector` (a byte copy of the binary, mode 0755) and
`DIR/manifest.yaml` (mode 0644). The command is idempotent: a file that already
holds the right bytes is left alone, and a changed file is replaced atomically.
The host's `process.command` then points at the seeded `collector`.

## Release

The manifest is embedded in the image it describes, so a build cannot carry its
own digest. `manifest.yaml` ships an all-zero placeholder digest in source. After
a release is pushed, `scripts/pin-digest.sh <repo:tag> --write` records the real
digest, and the next release commit carries it. The `process` adapter executes
the seeded binary and never pulls this image. Nothing in this repository pushes
an image or tags a release.

## Develop

```sh
go vet ./...
go test -race -count=1 ./...
docker build -t eshu-collector-pagerduty:local .
```

`go test -run TestResultFixturesAreCurrent -update .` rewrites
`testdata/fixtures` after a deliberate change.

## Known limits

- **`allow_empty_successor` fails closed.** The published collector SDK
  (`sdk/go/collector` v0.2.0) has no `Generation.TrustedDeletion`, so an
  attestation that an empty or sharply smaller successor is legitimate cannot
  reach the host. Setting it returns a terminal `unsupported_option`. Without
  it, the ingestion store refuses to replace a content-bearing generation with
  an empty or sharply shrunken one. This lifts when a collector SDK release
  carries the field.
- **No OpenTelemetry.** The process runner has no metrics endpoint. Operators
  read the host's recorded statuses (failure class, fact count, warning count,
  source latency) and the one health line this program writes to standard error.
- **Copied helpers.** `internal/stableid` and `internal/httpx` are copies of
  in-tree helpers that the public SDK does not publish yet. Replace both with
  the published eshu-sdk helpers when eshu-hq/eshu#8851 lands.

## License

MIT. See `LICENSE`.
