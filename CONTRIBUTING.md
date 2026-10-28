# Contributing

## Local checks

Run `make ci` before pushing. It runs the same `lint`, `test`, and `race`
lane commands CI runs (vet, org golangci-lint set, gofumpt format check,
actionlint at the pinned `ACTIONLINT_VERSION`, `go mod tidy -diff`, the
500-line file cap, build, tests, race with the coverage floor). CI stays
authoritative; `make ci` is the fast local floor. `make lint` runs
actionlint unconditionally, while the CI lint lane scopes it to workflow
changes — local is the stricter superset, so a workflow-syntax break fails
before push.

Need a single lane? `make lint`, `make test`, or `make race` run on their own.

## Coverage floor

The `race` lane uploads a coverprofile artifact, posts per-package totals to
the job summary, and fails below `COVERAGE_FLOOR` (percent of statements,
same default in the `Makefile`). Three numbers matter: the measured baseline
is 87.3% (1162/1331 statements, epic #9, 2026-10-27, re-measured 87.2%
and 87.3% across two runs at this HEAD); CI observed 87.2% on dispatch run
40618903647 (amd64 vs arm64-laptop plus run-to-run noise); the floor holds
at 87.1% as a stability margin, so that noise never goes red — had the
floor been set at 87.3, the CI run at 87.2 would have failed. The floor may only rise as tests land — never lower it,
exclude packages silently, or delete assertions to move it. Generated code
may be excluded only by an explicit, reviewed list. The finish line is 90%
(about 36 more covered statements at today's size, tracked in #9).

## Lint and size rules

`golangci-lint run ./...` with the committed `.golangci.yml` must report zero
findings — no baselines, no new-code-only mode. Any exclusion needs a reason
and a linked open issue. Go files are capped at 500 lines (`wc -l` step in
the `lint` lane); split before approaching the limit.

## What CI runs on your PR

`.github/workflows/ci.yml` picks lanes by changed paths: docs-only PRs run
no lane, Go changes run `lint`/`test`/`race`/`boundary` plus `gosec`,
fixture files tests read run `test`, dependency changes add `govulncheck`
(`gosec` already runs), workflow changes run actionlint inside `lint` plus
`lint`/`test`/`race`/`boundary` plus `gosec`, shell scripts run `shell`,
Dockerfile changes run `image`, and anything unclassified (the `misc`
catch-all, e.g. `Makefile`) runs `lint`/`test`/`race`/`boundary`.
Pushes, schedules, and dispatches run every lane. `ci-complete` is the only
required context — it passes when lanes were legitimately skipped.
