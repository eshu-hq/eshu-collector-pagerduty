# Operating the PagerDuty collector

In steady state this collector is quiet: the host feeds it claims and files away results. Your job is mostly sizing, watching result states, and knowing where to look when an account gets noisy.

## How it runs

The extension host uses its `process` runner. One `collector-sdk/v1alpha1` Request goes in on stdin, the collector reads the PagerDuty REST API, and one Result comes out on stdout. No arguments (or `--sdk-stdio`) serves one claim, and that's the only serving mode.

```bash
collector --sdk-stdio < request.json > result.json
```

## Image and seed

Releases publish `ghcr.io/eshu-hq/eshu-collector-pagerduty`: one static binary at `/collector` on a distroless base with no shell, running as user `65532:65532`. Mind that path. It's `/collector`, not the `/usr/local/bin/collector` some siblings use, and your init container will care about the difference.

The binary installs itself for the host:

```bash
/collector seed --dest DIR [--image-ref REPO@sha256:DIGEST]
```

Seed writes `DIR/collector` (byte copy, mode 0755) and `DIR/manifest.yaml` (mode 0644). Re-running is safe: correct files stay untouched, and changed ones swap in atomically. The host's `process.command` then points at the seeded copy, and the image itself never runs per claim.

`--image-ref`, or `COLLECTOR_IMAGE_REF` when the flag is absent, stamps the deployed digest into the seeded manifest. An image can't embed its own digest, so you supply it at seed time. The value must be `repository@sha256:<64 hex digits>`, with one trailing newline politely trimmed. Anything else exits non-zero, writes nothing, and never echoes the value back at you. Skip it and the all-zero placeholder digest from `manifest.yaml` goes out unchanged.

Identity card: `dev.eshu.collector.pagerduty`, version `0.1.4`, collector kind `pagerduty`, platform `linux/amd64`, compatible core `>=0.0.5 <0.2.0`.

## Claim budgets and big accounts

Each claim runs against the host's hard kill line at initial lease expiry (the worker setting `ESHU_COMPONENT_COLLECTOR_CLAIM_LEASE_TTL`, named in the example config). The collector stops starting provider calls `min(2s, 5% of remaining time)` early and returns retryable `claim_deadline_exceeded` with no facts, never a partial. An incomplete incident sweep doesn't get committed, which saves future-you from reasoning over half a timeline.

Do the math before you scale up. Reads are sequential, with two calls per incident plus per-service calls when config validation is on. A busy account inside a 6-hour lookback can outgrow the 60s worker default the example warns about. The example suggests at least the 5 minutes the reference deployment used. We'll be candid: no timing proof against a production-sized account exists yet, so measure yours early.

## Signals to watch

Results finish in one of four states:

- `complete`: every list ended on its own. Sip your coffee.
- `partial`: facts plus at least one coverage warning, like a bound hit, a hidden or missing service, or an unusable record. Facts still commit, so read this as "good data, known gaps."
- `retryable`: try again later. You'll see `rate_limited` (HTTP 408, 409, 425, 429), `retryable` (5xx, transport trouble, malformed bodies), `claim_deadline_exceeded`, or `canceled`, with `retry_after_seconds` attached when available.
- `terminal`: stop and fix. Expect `auth_denied` (401, 403), `not_found` (404), other 4xx, or config classes like `config_invalid`, `credential_missing`, `unsupported_option`, `scope_not_configured`, `invalid_claim`, `result_too_large`, and `fact_invalid`.

The collector fills in fact count, warning count, and source latency, and the host records the failure class from the result. You'll also get one `collector health: {...}` line per claim on stderr. That's gold during local runs, but the host's process runner keeps only a byte count and drops the text, so don't wire alerts to it. To chase a failure class, run the collector locally with the activation's config and a recorded request. That reproduces most mysteries in minutes.

Metrics, honestly: the manifest declares `telemetry.metricsPrefix: eshu_dp_pagerduty_collector_`, but nothing here emits a metric under it, and there's no metrics endpoint. The result's failure class is your operator signal today.

## Troubleshooting

A few field notes, symptom first:

- `scope_not_configured` or a terminal scope error usually means `scope_id` in config and `host.scope.id` drifted apart. They must match exactly.
- `credential_missing` means `token_env` named a variable the collector process can't see. Check the workload env, not your laptop shell. We say this from experience.
- `unsupported_option` appears the moment someone sets `allow_empty_successor`. That's the designed answer, not a bug. See known limits just below.
- Repeated partials with truncation warnings say your pagination bounds are clipping busy lists. Raise them, narrow `allowed_service_ids`, or shorten the lookback, whichever matches your intent.
- Deadline results on busy weeks point at lease size. Give large accounts more time before you suspect the API.

## Known limits

Two limits are worth writing in your runbook. First, `allow_empty_successor` fails closed until the collector SDK carries a trusted-deletion signal. Without it, the ingestion store refuses to replace a content-bearing generation with an empty or sharply smaller one, so a suddenly quiet account can't wipe yesterday's evidence. Second, `internal/stableid` and `internal/httpx` are local copies of helpers the public SDK doesn't publish yet. They'll give way to published SDK helpers when those land.

## Build, test, and release

```bash
make ci     # lint + test + race
make lint   # vet, golangci-lint, format diff, actionlint, tidy diff, 500-line cap
make test   # go build ./... and go test ./... -count=1
make race   # race tests plus the coverage floor (90.8%)
docker build -t eshu-collector-pagerduty:local .
```

This Makefile has no separate `actionlint` target, so don't go looking for one. `make lint` runs it. [testdata/README.md](../../testdata/README.md) explains the parity fixtures, and `go test -run TestResultFixturesAreCurrent -update .` rewrites `testdata/fixtures` after a deliberate behavior change.

Releases run from `.github/workflows/release.yml` on `main` pushes and `v*` tags. Pull requests publish nothing, and the workflow deploys nothing. A `main` push publishes moving `:main` and `:latest` plus an immutable `:sha-<long-sha>`. A version tag must match `metadata.version` in `manifest.yaml`, look like `vMAJOR.MINOR.PATCH` with an optional pre-release suffix, and point at the current head of `main`. The job builds `linux/amd64`, pushes to GHCR, signs the digest with keyless cosign, attests SLSA provenance and an SPDX SBOM, verifies all three, and checks the package pulls anonymously. Keep the GHCR package public. One gotcha: image digests aren't reproducible across attempts, so take the digest from the last successful attempt, not the first.

Verify a release before you trust it:

```bash
cosign verify ghcr.io/eshu-hq/eshu-collector-pagerduty@sha256:DIGEST \
  --certificate-identity https://github.com/eshu-hq/eshu-collector-pagerduty/.github/workflows/release.yml@refs/tags/vX.Y.Z \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
gh attestation verify oci://ghcr.io/eshu-hq/eshu-collector-pagerduty@sha256:DIGEST \
  -R eshu-hq/eshu-collector-pagerduty
```

## SDK pins

Exact pins from `go.mod`, no `replace` directives. If this page and the file ever disagree, believe the file.

| Module | Pin |
| --- | --- |
| `github.com/eshu-hq/eshu-sdk/collector` | `v0.2.0-rc.1` |
| `github.com/eshu-hq/eshu-sdk/factschema` | `v0.1.0-rc.5` |
| `gopkg.in/yaml.v3` | `v3.0.1` |

Go version in `go.mod`: `1.26.10`.
