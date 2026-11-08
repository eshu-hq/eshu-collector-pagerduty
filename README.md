# eshu-collector-pagerduty

Status: pre-release. It isn't the live PagerDuty producer for Eshu yet, and Eshu core still owns admission and storage.

When something breaks at 2 a.m., PagerDuty remembers what happened. This collector brings that memory into Eshu as facts: recent incidents, how they moved, which changes were nearby, and (when you ask for it) how services and integrations are wired. Eshu can then sit incident context right next to the code and deploys it already knows about.

```mermaid
flowchart LR
    A["PagerDuty REST API"] --> B["This collector"]
    B --> C["Eshu extension host"]
    C --> D["Eshu core"]
```

In words: PagerDuty answers, this collector translates, the host validates and commits, and core stores. The host drives the whole exchange one claim (one bounded unit of work) at a time.

## When to use it, and when to walk away

Use it when you want Eshu to reason about incidents alongside everything else it tracks. "What changed near this outage?" is exactly the kind of question this evidence supports.

Walk away if you need to page someone, acknowledge or resolve an incident, or replace PagerDuty. This collector only reads. It writes nothing back, ever.

## Your first smoke run

```bash
collector --sdk-stdio < request.json > result.json
```

That feeds one recorded request in and writes one result out, which is also the only way the host talks to it. For production you'll seed the release image and configure an account. Both live in [docs/guides/operations.md](docs/guides/operations.md) and [docs/reference/configuration.md](docs/reference/configuration.md).

## Docs

| Question on your mind | Page |
| --- | --- |
| How do incidents become facts? | [docs/architecture/README.md](docs/architecture/README.md) |
| What goes in the config file? | [docs/reference/configuration.md](docs/reference/configuration.md) |
| How do I run, release, or debug it? | [docs/guides/operations.md](docs/guides/operations.md) |

Keep [config.example.yaml](config.example.yaml), [grant.example.json](grant.example.json), [manifest.yaml](manifest.yaml), and [testdata/README.md](testdata/README.md) within reach. You'll want them.

## Related repos

- [eshu](https://github.com/eshu-hq/eshu): core and the extension host that runs this collector.
- [eshu-sdk](https://github.com/eshu-hq/eshu-sdk): the collector and fact-schema modules pinned here.
- [eshu-deploy](https://github.com/eshu-hq/eshu-deploy): deployment wiring for Eshu components.

## A note on scope

One activation covers one PagerDuty account, and the incident sweep does two API calls per incident inside your lookback window. That's the number to keep in your head when you size leases for a busy account. Service and integration reads stay off until you opt in, so a first activation can stay lean and grow when you're ready.

Manifest version today is `0.1.4` for collector kind `pagerduty`.
Exact SDK pins and the release flow sit in [docs/guides/operations.md](docs/guides/operations.md).
If a fact kind ever changes, the manifest and [docs/architecture/README.md](docs/architecture/README.md) change together.

## Developing

`make ci` before you push, please. The details wait in [docs/guides/operations.md](docs/guides/operations.md).

## Contributing, security, license

Start with [CONTRIBUTING.md](CONTRIBUTING.md). License: MIT, see [LICENSE](LICENSE). You'll find no `SECURITY.md` in this repo.
