# Test data

Everything here is synthetic. No file holds a real host, account, incident,
credential, or provider payload.

| Path | What it is |
| --- | --- |
| `source/*.json` | Recorded PagerDuty REST API responses. A tiny fixture server in the tests answers each request from `path` plus the `offset` query value. |
| `parity/cases.json` | The parity scenarios: a fixture, an activation config, and a fixed clock. |
| `parity/golden_dump_test.go.txt` | The throwaway dumper that produces `golden/`. It is a `.txt` file so it is never compiled here. |
| `golden/*.json` | What the **in-tree** collector in `eshu-hq/eshu` emits for each parity case. |

`parity_test.go` replays every case through this collector and compares it
with `golden/`: kind, stable key, schema version, source confidence,
observation time, payload (JSON-normalized), source reference, scope, and
generation. It does not compare `FactID`: the host builds that in its own
namespace (`CollectorExtensionFact`), so it differs by design.

## Regenerate `golden/`

`golden/` was produced from `eshu-hq/eshu` `main` at `ce050339cf`, with
`go/internal/collector/pagerduty` unchanged between `c697b07618` and that
commit. Regenerate it after the in-tree collector, a fixture, or a case
changes. Use a throwaway worktree of `eshu-hq/eshu`; never edit its main
checkout.

```sh
ESHU=/path/to/eshu                      # a clone of eshu-hq/eshu
GOLDEN=/path/to/eshu-wt/golden          # a new worktree path
THIS=/path/to/eshu-collector-pagerduty  # this repository

git -C "$ESHU" fetch origin
git -C "$ESHU" worktree add -b golden/pagerduty-parity "$GOLDEN" origin/main

cp "$THIS/testdata/parity/golden_dump_test.go.txt" \
   "$GOLDEN/go/internal/collector/pagerduty/zz_golden_dump_test.go"

(cd "$GOLDEN/go" && \
  PARITY_REPO="$THIS" PARITY_OUT="$THIS/testdata/golden" \
  go test ./internal/collector/pagerduty -run TestZZGoldenDump -count=1 -v)

rm "$GOLDEN/go/internal/collector/pagerduty/zz_golden_dump_test.go"
git -C "$ESHU" worktree remove "$GOLDEN"
```

Then run `go test -race -count=1 .` here. A parity failure means this
collector and the in-tree collector disagree: fix the disagreement, or record
it as an intentional difference (see below), but do not edit `golden/` by hand.

## Intentional differences from the in-tree collector

These are the only places the two outputs differ, and the parity test pins
each one:

- **`FactID`.** Not compared; the host derives it.
- **Partial state.** The in-tree collector always completes a generation and
  counts coverage warnings in a metric. This collector reports a `partial`
  Result when it emitted any coverage warning. The facts are the same.
- **`freshness_hint` with an embedded URL.** The hint is a digest of the
  payloads. A case whose payloads embed the fixture server's ephemeral URL
  hashes different bytes in each run, so the digest is compared only for cases
  without it. The payloads themselves are compared for every case.

Other differences exist but have no parity case because they only occur on
hostile or unusual input; each has its own unit test:

- A fact repeated across offset pages is emitted once (first occurrence wins).
- A provider record without an ID is dropped with a coverage warning instead of
  failing the claim.
- A provider identifier that is not a PagerDuty-shaped ID is never placed in a
  request path.
- Plain `http` API URLs are refused unless the host is loopback, so the token
  never crosses a network in cleartext.
- Query keys that the SDK considers sensitive are removed from emitted URLs
  (a superset of the in-tree list).
- The source reference URI falls back to the API base URL when no usable URI
  remains, because the SDK requires one. The payload `source_url` is unchanged.
- Integration facts carry an SDK `redactions` list naming the dropped key
  fields. The in-tree envelope has no such list.
