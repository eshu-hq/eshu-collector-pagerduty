# Facts reference

## The six kinds of fact

This table mirrors the `emittedFacts` in `manifest.yaml`. If you change one side, change the other side too. Each kind carries schema `1.0.0` and confidence `reported`, because PagerDuty reported the values itself.

| Kind | One of these records… | PagerDuty read behind it |
| --- | --- | --- |
| `incident.record` | a single incident | `GET /incidents` |
| `incident.lifecycle_event` | one timeline entry on an incident | `GET /incidents/{id}/log_entries` |
| `change.record` | one change related to an incident | `GET /incidents/{id}/related_change_events` |
| `incident_routing.observed_pagerduty_service` | a live service | `GET /services`, `GET /services/{id}` |
| `incident_routing.observed_pagerduty_integration` | a live integration on a service | `GET /services/{id}/integrations` |
| `incident_routing.coverage_warning` | a bound hit, a hidden or missing service, or an unusable record | whichever read above came up short |

Keep this in your back pocket: no result, status, or log line carries a provider response body, a token, or a URL with credentials. What travels is the incident evidence itself and bounded failure vocabulary.

## SDK pins

Exact pins from `go.mod`, no `replace` directives. If this page and the file ever disagree, believe the file.

| Module | Pin |
| --- | --- |
| `github.com/eshu-hq/eshu-sdk/collector` | `v0.2.0-rc.1` |
| `github.com/eshu-hq/eshu-sdk/factschema` | `v0.1.0-rc.5` |
| `gopkg.in/yaml.v3` | `v3.0.1` |

Go version in `go.mod`: `1.26.10`.

The manifest ([manifest.yaml](../../manifest.yaml)) stays the source of truth for this list. If a fact kind ever changes, the manifest and this page change together.
