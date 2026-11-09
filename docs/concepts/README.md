# Concepts

## What this is

A pre-release, read-only collector that brings PagerDuty's memory into Eshu as facts: recent incidents, how they moved, which changes were nearby, and — when you ask for it — how services and integrations are wired. Eshu can then sit incident context right next to the code and deploys it already knows about.

## Why it exists

"What changed near this outage?" is exactly the kind of question this evidence supports. PagerDuty remembers the timeline; this collector retells it in a form Eshu can store and reason over.

## The cost model to remember

One activation covers one PagerDuty account. The incident sweep pages incidents inside your lookback window and then makes two calls per incident (log entries, then related change events), sequentially. Service and integration reads join only when `config_validation_enabled` is set or you name services in `allowed_service_ids`.

## Claims, grants, and honest truncation

The host drives everything one claim at a time and owns claims, leases, envelope building, and commits; core owns admission and storage. All six kinds are core-owned, so host-side grants gate activation. Pagination has hard bounds, and hitting one with more data waiting produces a coverage warning — the story may be abridged, but never silently.

## Boundaries

It never writes to PagerDuty — no paging, acknowledging, or resolving. It holds no Postgres, graph, queue, or lease handle, and never imports Eshu core internals. No result, status, or log line carries a provider response body, a token, or a credential-bearing URL.
