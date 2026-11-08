# Configuring the PagerDuty collector

One activation describes one PagerDuty account. The host reads the whole file: `host` and `process` configure the host, and everything under `config` reaches this collector. [config.example.yaml](../../config.example.yaml) is a complete file with placeholders, so copy it and adjust. In that example `host.sourceSystem` is `pagerduty` and `host.scope.kind` is `pagerduty_account`.

Choose your scope ID like you'll keep it, because you will. Scope IDs sit inside stable fact keys, so changing one later makes the same account look new to Eshu.

## Keys, and what they do to you

| Key | The practical upshot |
| --- | --- |
| `scope_id` | Required, and it must equal `host.scope.id`. A claim for any other scope fails terminal, which is the collector telling you the activation and the claim disagree. |
| `account_id` | Required. The PagerDuty account identifier for this activation. |
| `token_env` | Required. Names the environment variable holding the API token, `ESHU_PAGERDUTY_API_TOKEN` in the example. The token itself never goes in the file. |
| `api_base_url` | Optional. Defaults to `https://api.pagerduty.com`. Plain `http` is accepted only for a loopback host, which mostly helps local testing. |
| `source_uri` | Optional. A credential-free fallback source reference. |
| `incident_lookback` | How far back incidents get read. Example and default: `6h`. Widen it carefully, since every extra incident costs two more API calls. |
| `incident_limit`, `log_entry_limit`, `change_event_limit` | Page sizes from `0` to `100`. Zero lets PagerDuty pick its default. |
| `pagination_max_pages`, `pagination_max_records` | Hard bounds per list, `10` pages and `1000` records in the example. Hit a bound with more waiting and you get a truncation warning, so the cutoff never hides. |
| `allowed_service_ids` | Optional. Read only these services instead of every service in the account. Naming services here also turns on service reads for them. |
| `config_validation_enabled` | Turn it on to also read live service and integration configuration across the account. Leave it off and incidents still flow. |
| `config_resource_limit` | Page size for those configuration reads, `100` in the example. |
| `max_result_bytes` | Above this many encoded bytes the claim fails terminal with `result_too_large`. Example and default: `3500000`. Raise `process.stdout_limit_bytes` with it. |
| `allow_empty_successor` | Parsed but unsupported. Setting it fails closed with `unsupported_option`, on purpose. The details sit under known limits in [operations.md](../guides/operations.md). |

## The token, handled properly

Your API token travels by environment-variable name only. `token_env` names the variable, the collector process reads it at claim time, and the token rides in a request header from there. It never shows up in facts, logs, or statuses. If the name resolves to no value, the claim fails as `credential_missing` before any PagerDuty call, so a misnamed variable announces itself right away.

Give the token only the read access the GET endpoints need. This repo doesn't name PagerDuty ability scopes or API key types, because the code doesn't define them, so let your PagerDuty admin guidance fill that part in.

## Grants before activation

All six kinds belong to core, so the host won't activate the component without a live producer grant per kind. A grant is core's permission to emit a kind it owns. [grant.example.json](../../grant.example.json) lists all six with scope `pagerduty`, version `0.1.4`, and schema `1.0.0`. Note that a grant's scope is the collector kind, `pagerduty`, not your account scope ID. Mixing those two up is an easy way to lose an hour.

Installs use allowlist trust today. The release does a plain keyless `cosign sign` with no Eshu trust annotations, so strict trust, which checks those annotations, isn't supported yet.
