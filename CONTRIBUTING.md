# Contributing

## Local checks

Run `make ci` before pushing. It runs the same `lint`, `test`, and `race`
lane commands CI runs (vet, org golangci-lint set, gofumpt format check,
actionlint at the pinned `ACTIONLINT_VERSION`, `go mod tidy -diff`, the
500-line file cap, build, tests, race with the coverage floor). CI stays
authoritative; `make ci` is the fast local floor. `make lint` runs
actionlint unconditionally, while CI runs it as its own `actionlint` lane
scoped to workflow changes — local is the stricter superset, so a
workflow-syntax break fails before push.

Need a single lane? `make lint`, `make test`, or `make race` run on their own.

`bash scripts/pagerduty-seed-matrix.sh` is a local seeded RED/GREEN matrix: it plants a defect per lane (vet, lint, format, race, tidy, the 500-line cap, the coverage floor), asserts the lane turns red naming the seed, removes it, and asserts green again. It is not a CI gate.

## Coverage floor

The `race` lane uploads a coverprofile artifact, posts per-package totals to
the job summary, and fails below `COVERAGE_FLOOR` (percent of statements,
same default in the `Makefile`). Three numbers matter: the measured total
is 90.9–91.1% (1210–1213/1331 statements, epic #9, 2026-10-28, across one
plain and three `-race` runs; the band is a context-cancellation branch in
`fetchIncidentChildren` that only wins its race under `-race` slowdown,
plus the usual amd64-vs-arm64 platform noise); the floor holds
at 90.8% as a stability margin, so that noise never goes red — the same
margin rule that kept the earlier 87.1% floor green when CI read 87.2%
against a 87.3% laptop measurement. The floor may only rise as tests land — never lower it,
exclude packages silently, or delete assertions to move it. Generated code
may be excluded only by an explicit, reviewed list. The 90% finish line from
#9 is met: the floor (90.8%) stands above it.

## Lint and size rules

`golangci-lint run ./...` with the committed `.golangci.yml` must report zero
findings — no baselines, no new-code-only mode. Any exclusion needs a reason
and a linked open issue. Go files are capped at 500 lines (`wc -l` step in
the `lint` lane); split before approaching the limit.

## What CI runs on your PR

`.github/workflows/ci.yml` is a thin caller. Its `changes` job (always
runs, every event) picks lanes by changed paths, then the `go-ci` job calls
the shared `eshu-hq/.github` `go-collector-ci.yml` reusable, pinned by
commit SHA (tag `v2` as a comment only), which holds the lane bodies:
`lint`, `test`, `race`, `boundary`, `shell`, `image`, `govulncheck`,
`gosec`, and `actionlint`. Their check runs nest as `go-ci / <lane>`. The
tool versions (golangci-lint, govulncheck, gosec, actionlint) and the
coverage floor are inputs on the `go-ci` call; bump them there, and keep the
floor equal to `COVERAGE_FLOOR` in the `Makefile`
(`TestCoverageFloorParity` enforces it). `ci_reusable_test.go` freezes the
caller shape, the pin, and every run gate.

Docs-only PRs run no lane. Go changes (including `testdata/**` and
`manifest.yaml`, which are compiled or embedded) run
`lint`/`test`/`race`/`boundary`/`image` (the Dockerfile builds the binary
from them) plus `gosec`. Fixture files tests read run `test` only.
Dependency changes add `govulncheck` (`gosec` already runs). Workflow files
and `.github/dependabot.yml` run `actionlint` plus
`lint`/`test`/`race`/`boundary` plus `gosec`. Shell scripts run `shell`, and
Dockerfile changes run `image`. Anything unclassified (the `misc`
catch-all, e.g. `Makefile`) runs `lint`/`test`/`race`/`boundary`. Pushes,
schedules, and dispatches run every lane. The `image` lane proves the seed
contract and that `manifest.yaml` and every Dockerfile `FROM` stay
digest-pinned. Two caller jobs sit beside `go-ci`: `gosec-sarif` uploads the
gosec report to the Security tab on non-PR events, and `govulncheck-report`
files or updates a findings issue when a scheduled run fails. `ci-complete`
is the only required context — it passes when lanes were legitimately
skipped.
