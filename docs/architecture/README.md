# Architecture

Incidents tell stories: something fired, someone got paged, a change landed nearby, things calmed down. This collector reads that story out of PagerDuty's REST API and retells it as facts Eshu can store. It also peeks at service and integration setup, but only when you ask it to.

## The cast

See [Diagrams](../diagrams/README.md) for the picture.

Text version: PagerDuty's REST API feeds this collector, the collector feeds the extension host, and the host feeds Eshu core. The host hands over one claim, validates the result, and owns claims, leases (timed ownership of the work), envelope building, and commits. Core owns admission, projection, and persistence. All the PagerDuty conversation happens in this collector.

## One claim, narrated

See [Diagrams](../diagrams/README.md) for the picture.

Same flow, prose edition: a request arrives and config gets checked before any network traffic, including whether the claim scope matches the one account this activation serves. Then the collector pages through recent incidents inside your lookback window. For each incident it fetches timeline entries and related changes, two calls per incident, done sequentially. Service and integration reads join in only when `config_validation_enabled` is set or you've named services in `allowed_service_ids`. Incident reads always run, since they're the main event.

Pagination stays on a leash. Page counts and record counts have hard bounds, and hitting one with more data waiting produces a truncation warning instead of a quiet cutoff. You'll always know when the story got abridged.

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

## Firm boundaries

This collector never writes to PagerDuty. No paging, no acknowledging, no resolving, not even by accident, since the code only issues reads. It holds no Postgres, graph, queue, or lease handle, and it never imports Eshu core internals (`boundary_test.go` checks). It doesn't build the durable envelope or commit facts, and it doesn't decide a claim's fate beyond the failure class it reports. Kinds outside the six above never appear, whatever the API returns.
