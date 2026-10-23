// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

// Package collector is the Eshu PagerDuty collector, packaged as a component
// extension that the Eshu component-extension host runs with the process
// runner.
//
// The host writes one bounded collector-sdk/v1alpha1 Request to the process's
// standard input and reads one collector-sdk/v1alpha1 Result from its standard
// output. The package turns that Request into PagerDuty REST reads and the
// Result into the incident-context fact kinds the Eshu core already owns:
// incident.record, incident.lifecycle_event, change.record, and the
// incident_routing observed-service, observed-integration, and coverage-warning
// kinds. The host builds the durable envelope, validates the Result, and owns
// claims, leases, and commits.
//
// Boundary: this package imports only the published SDK modules
// (sdk/go/collector, sdk/go/factschema), the standard library, and this
// module's own internal packages. It never imports go/internal from the core
// module, another collector implementation, or any Postgres, graph, queue, or
// lease handle. boundary_test.go enforces that, and the extraction policy in
// eshu-hq/eshu#4047 depends on it.
//
// This repository holds no producer authority until its reviewed cutover
// (eshu-hq/eshu#8863).
package collector
