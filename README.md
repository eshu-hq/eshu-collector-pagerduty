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
| `retryable` | `rate_limited` (HTTP 408, 409, 425, 429), `retryable` (5xx, transport, malformed body), `claim_deadline_exceeded`, or `canceled`. The collector sends `retry_after_seconds`; the worker honors it on eshu `main` since eshu-hq/eshu#9008 (see Operator signals). |
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
| `api_base_url`, `source_uri` | Optional. Default API origin is `https://api.pagerduty.com`. Plain `http` is accepted only for a loopback host, because the token travels in a header. |
| `incident_lookback` | How far back to read incidents. Default `6h`. |
| `incident_limit`, `log_entry_limit`, `change_event_limit`, `config_resource_limit` | Page sizes, 0 to 100. |
| `pagination_max_pages`, `pagination_max_records` | Bounds per list (defaults 10 and 1000; ceilings 100 and 5000). |
| `allowed_service_ids` | Read only these services. |
| `config_validation_enabled` | Also read live service and integration configuration. |
| `max_result_bytes` | Fail terminal above this many encoded bytes (default 3500000, under the host's 4 MiB output limit). |
| `allow_empty_successor` | Parsed but **unsupported**: see Known limits. |

## Run it with Eshu

The component lifecycle, using the `eshu` CLI against a component home:

```sh
eshu component grant dev.eshu.collector.pagerduty --component-home "$HOME_DIR" \
  --version 0.1.4 --kind incident.record --schema-version 1.0.0 \
  --scope pagerduty --expires-in 8760h        # repeat for each kind in grant.example.json
eshu component install manifest.yaml --component-home "$HOME_DIR" \
  --trust-mode allowlist --allow-id dev.eshu.collector.pagerduty --allow-publisher eshu-hq
eshu component enable dev.eshu.collector.pagerduty --component-home "$HOME_DIR" \
  --instance pagerduty-example --mode scheduled --claims --config config.example.yaml
```

A grant's scope is the collector kind (`pagerduty`), not a scope ID.
`grant.example.json` lists the six grants.

### Trust mode

The release signs the image digest keyless with cosign, but it does a plain
`cosign sign`, so the signature carries no `eshu.component.*` annotations. That
suits **allowlist** trust, which the install command above uses. **Strict**
trust has Eshu run `cosign verify` with those annotations, and this workflow
does not produce them, so strict trust is not supported yet. Adding it is a
follow-up.

## Operator signals

The extension host passes the **failure class** (`rate_limited`,
`auth_denied`, `claim_deadline_exceeded`, ...) from a claim's result to the
work item. On eshu `main` since eshu-hq/eshu#9008 (a release carries it once
one is cut after that change), the worker also sets a status recorder: it
records every claim result as six `eshu_dp_component_extension_*` metrics and
one `component extension result` log line, and it honors
`retry_after_seconds`:

- **Recorded: fact count, warning count and source latency.** The collector
  fills them in, and the host records them in the metrics above.
- **Honored: `retry_after_seconds`.** The collector sends it on every
  retryable result. The worker retries after the larger of the poll interval
  and that request, capped at one hour, until the attempt budget is spent. The
  `eshu_dp_component_extension_retry_after_seconds` gauge and the log line hold
  the request, not the applied delay.
- **Dropped: the stderr health line.** The collector writes one
  `collector health: {...}` line per claim to stderr with the health,
  resource and claim summary. The host's process runner keeps stderr only as
  a byte count and drops the text. The line is for a local run
  (`collector --sdk-stdio < request.json`) and for tests.
- **No spans or metrics.** The manifest declares
  `telemetry.metricsPrefix: eshu_dp_pagerduty_collector_`, but nothing in this
  repository emits a metric under it.

The host records the failure class, not the cause behind it. Find the cause of
a failure by running the collector with the activation's config and a recorded
request.

## Image and seeding

The image is `ghcr.io/eshu-hq/eshu-collector-pagerduty`: one static binary at
`/collector` on a distroless base, running as `65532:65532`, with no shell. It
is also the delivery artifact. An init container runs:

```sh
/collector seed --dest DIR [--image-ref REPO@sha256:DIGEST]
```

which writes `DIR/collector` (a byte copy of the binary, mode 0755) and
`DIR/manifest.yaml` (mode 0644). The command is idempotent: a file that already
holds the right bytes is left alone, and a changed file is replaced atomically.
The host's `process.command` then points at the seeded `collector`.

The manifest is embedded in the image it describes, so it cannot hold that
image's digest. The deployer passes the image it runs instead:
`--image-ref REF`, or the `COLLECTOR_IMAGE_REF` environment variable (the flag
wins). After one trailing newline is trimmed, `REF` must match
`^[a-z0-9]+([._-][a-z0-9]+)*(:[0-9]+)?(/[a-z0-9]+([._-][a-z0-9]+)*)*@sha256:[0-9a-f]{64}$`,
so it carries no quote, `$`, whitespace, or other YAML-significant character.
Seed stamps it literally into the seeded manifest's `artifacts[].image` and
changes nothing else. A reference of any other shape exits non-zero without
writing anything or echoing the value.
Without `REF`, the repository placeholder (an all-zero digest) is written
unchanged. The `process` adapter runs the seeded binary and never pulls this
image, so the stamp is provenance for the installed manifest.

## Releasing

`.github/workflows/release.yml` runs on pushes to `main` and on `v*` tags,
and nothing else, so pull requests publish nothing. A push to `main` publishes
the moving `:main` and `:latest` tags plus an immutable `:sha-<long-sha>` tag;
a pushed tag publishes that tag. The workflow uses only the job's
`GITHUB_TOKEN`, pins every action by commit SHA, and has no deploy step.

Before the first tag, check the repository settings, because a workflow cannot
enforce them. Any pushed `v*` tag publishes a public image, so restrict `v*`
tag creation to maintainers (a repository ruleset for tags). The workflow
refuses a tag that does not point at the current head of `main`, and checks the
tag against `manifest.yaml`, so tag the merged commit at the head of `main`.

1. Merge the change to `main`. The tag must match `metadata.version` in
   `manifest.yaml` (`v0.1.0` for version `0.1.0`; a `-rc1` style suffix is
   allowed). The workflow first requires the tag to be
   `vMAJOR.MINOR.PATCH` with an optional `-prerelease` suffix, so a tag such
   as `v0.1.0-` or `v0.1.0-rc/a` fails before anything is built. Bump the
   manifest version in the same PR.
2. Tag the merged commit and push the tag: `git tag v0.1.0 && git push origin v0.1.0`.
3. The workflow builds `linux/amd64` only, pushes
   `ghcr.io/eshu-hq/eshu-collector-pagerduty:<tag>`, and writes the digest into
   the job summary. It signs the digest with cosign (keyless), attests SLSA
   build provenance, attests an SPDX SBOM, verifies all three, and fails if the
   package cannot be pulled anonymously.
4. Copy the digest from the job summary into the activation's `seed.image`
   (`ghcr.io/eshu-hq/eshu-collector-pagerduty@sha256:<digest>`). The init
   container passes the same reference to `collector seed` as
   `COLLECTOR_IMAGE_REF` (or `--image-ref`), which stamps it into the seeded
   manifest. No commit needs to carry the digest.

The package must be **public**: the activation pulls it without a token. A new
GHCR package starts private, so after the first release set its visibility to
public in the package settings and re-run the job; the last step checks it. The
step fails with "not publicly readable" only when the registry refuses the
anonymous pull. Any other failure reports its own message.

The image digest is not reproducible across attempts. The build passes the
tagged commit's time as `SOURCE_DATE_EPOCH`, which pins the image's created
time, but layer bytes can still differ. The re-run therefore pushes `:<tag>`
again with a new digest and signs and attests that one. Use the digest from the
job summary of the **last, successful attempt**: it is the one the tag points to
and the one the anonymous-pull check passed for. The first attempt's digest is
also signed and attested, but the tag no longer points to it.

Verify a release before using it:

```sh
cosign verify ghcr.io/eshu-hq/eshu-collector-pagerduty@sha256:<digest> \
  --certificate-identity https://github.com/eshu-hq/eshu-collector-pagerduty/.github/workflows/release.yml@refs/tags/v0.1.0 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
gh attestation verify oci://ghcr.io/eshu-hq/eshu-collector-pagerduty@sha256:<digest> \
  -R eshu-hq/eshu-collector-pagerduty
# The SPDX predicate version is the one in the SBOM's spdxVersion
# (SPDX-2.3 gives v2.3). The workflow reads it from the SBOM file.
gh attestation verify oci://ghcr.io/eshu-hq/eshu-collector-pagerduty@sha256:<digest> \
  -R eshu-hq/eshu-collector-pagerduty --predicate-type https://spdx.dev/Document/v2.3
```

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
  an empty or sharply shrunken one. Removal condition: a collector SDK
  release that contains `Generation.TrustedDeletion` (added in eshu-hq/eshu commit
  `49205b1214`, after the v0.2.0 tag). Then bump the SDK pin, map the option onto
  the Result generation, and replace the fail-closed test.
- **A claim has a hard time bound.** The host sets the claim deadline to the
  initial lease expiry and kills the process there. The default worker lease TTL
  is 60 s (`ESHU_COMPONENT_COLLECTOR_CLAIM_LEASE_TTL`), and heartbeats extend the
  database lease, not this deadline. The collector reads PagerDuty sequentially
  (two calls per incident, plus per-service calls when config validation is on),
  so a large account can need more than 60 s. It therefore stops starting
  provider calls `min(2 s, 5% of the remaining time)` before the deadline and
  returns `retryable` with `claim_deadline_exceeded` and no facts, never
  `partial`, so an incomplete read is not committed. If every attempt ends that
  way, raise the worker lease TTL. An activation that replaces the in-tree
  collector should set it to at least the lease TTL the in-tree collector ran
  with (5 m in the reference deployment). No timing proof against a
  production-sized account exists yet.
- **No OpenTelemetry.** The process runner has no metrics endpoint, and
  nothing in this repository emits a metric under the manifest's
  `eshu_dp_pagerduty_collector_` prefix. See Operator signals for what an
  operator can see today.
- **Copied helpers.** `internal/stableid` and `internal/httpx` are copies of
  in-tree helpers that the public SDK does not publish yet. Replace both with
  the published eshu-sdk helpers when eshu-hq/eshu#8851 lands.

## License

MIT. See `LICENSE`.
