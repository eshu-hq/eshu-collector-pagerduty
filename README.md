# eshu-collector-pagerduty

Eshu PagerDuty incident-context collector, packaged as an Eshu component
extension.

Pre-release. Holds no producer authority until its reviewed cutover
(eshu-hq/eshu#8863).

## What it is

A standalone Go program that the Eshu component-extension host runs with the
`process` runner. The host writes one bounded `collector-sdk/v1alpha1` request
to stdin. The collector writes one `collector-sdk/v1alpha1` result to stdout.
It holds no Postgres, graph, queue, or lease handle, and it never imports
Eshu internals. `boundary_test.go` enforces that.

## Layout

| Path | Purpose |
| --- | --- |
| `cmd/collector` | Binary entry point (SDK stdio request, `seed` subcommand). |
| `manifest.yaml` | Component package manifest. |
| `grant.example.json` | Example producer grant for the core fact kinds. |
| `config.example.yaml` | Example activation config (no real hosts or secrets). |
| `scripts/` | Conformance, digest-pin, and remote read-back helpers. |

## Develop

```sh
go vet ./...
go test -race -count=1 ./...
```

## License

MIT. See `LICENSE`.
