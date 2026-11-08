# Diagrams

Every diagram for eshu-collector-pagerduty lives on this page, each with a caption and a plain-text version. Other pages link here instead of repeating the picture. The root README keeps the first one only.

## Where this collector sits

```mermaid
flowchart LR
    A["PagerDuty REST API"] --> B["This collector"]
    B --> C["Eshu extension host"]
    C --> D["Eshu core"]
```

Caption: the provider answers, this collector translates, the Eshu extension host validates and commits, and Eshu core stores what it admits.

Text version: provider API → this collector → Eshu extension host → Eshu core. The host drives the exchange one claim (one bounded unit of work) at a time; the collector never runs on its own schedule.

## The neighborhood, with responsibilities

```mermaid
flowchart LR
    P["PagerDuty REST API"] --> C["This collector"]
    C --> H["Eshu extension host"]
    H --> K["Eshu core"]
```

Caption: the same chain with the host's claim edge made explicit. The host owns claims, leases, envelope building, validation, and commits; core owns admission, projection, and persistence.

Text version: the host hands over one claim and takes back one result. Everything between "host asked" and "here is what the provider said" belongs to this collector; everything downstream belongs to the host and core.

## One claim, start to finish

```mermaid
sequenceDiagram
    participant Host as Extension host
    participant Col as Collector
    participant PD as PagerDuty API
    Host->>Col: Request on stdin (one claim)
    Col->>Col: validate config, resolve token, check scope
    Col->>PD: GET /incidents (lookback window, paged)
    loop each incident
        Col->>PD: GET log entries
        Col->>PD: GET related change events
    end
    opt config validation on or services allow-listed
        Col->>PD: GET services and integrations
    end
    Col->>Col: normalize within pagination bounds
    Col-->>Host: Result on stdout (facts plus warnings)
    Host->>Host: validate and commit
```

Caption: the sequence inside a single claim — config and credentials resolve before any network call, provider reads run in order within their bounds, and one result goes back on stdout.

Text version: request in on stdin → parse config, resolve env-named credentials, check grant/scope → pick the target for the claim scope → bounded provider GET reads → normalize and fingerprint → one result out on stdout → host validates and commits.
