// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

// Command collector is the Eshu PagerDuty collector component.
//
// With no arguments (or --sdk-stdio) it reads one collector-sdk/v1alpha1
// Request from standard input, runs the claim, and writes one Result to
// standard output. With the seed subcommand it copies itself and its manifest
// into a directory so an init container can hand them to the component home.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	collector "github.com/eshu-hq/eshu-collector-pagerduty"
	"github.com/eshu-hq/eshu-collector-pagerduty/internal/httpx"
)

// deps are the process inputs the command reads, injectable for tests.
type deps struct {
	getenv     func(string) string
	now        func() time.Time
	httpClient httpx.HTTPDoer
	executable func() (string, error)
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr, deps{
		getenv:     os.Getenv,
		executable: os.Executable,
	}))
}

// run executes the command and returns the process exit code: 0 for a written
// Result of any state, 1 for a protocol or I/O failure, 2 for a usage error.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, d deps) int {
	if len(args) > 0 && args[0] == "seed" {
		return runSeed(args[1:], stdout, stderr, d)
	}
	flags := flag.NewFlagSet("collector", flag.ContinueOnError)
	flags.SetOutput(stderr)
	// --sdk-stdio is accepted for activation files that name it explicitly;
	// reading the Request from standard input is the only serving mode.
	_ = flags.Bool("sdk-stdio", false, "read one collector SDK host request from stdin (the default)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "collector: unexpected argument %q\n", flags.Arg(0))
		return 2
	}
	return serve(ctx, stdin, stdout, stderr, d)
}

func serve(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, d deps) int {
	monitor := collector.NewMonitor()
	request, err := collector.DecodeRequest(stdin)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "collector: %v\n", err)
		return 1
	}
	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	handler := collector.Handler{Getenv: d.getenv, Now: d.now}
	if d.httpClient != nil {
		handler.HTTPClient = d.httpClient
	}
	result, summary := handler.Handle(signalCtx, request)
	monitor.SetResource(collector.ResourceUse{MaxResultBytes: collector.DefaultMaxResultBytes})
	if summary.FailureClass != "" {
		monitor.ObserveFailure(summary.FailureClass)
	} else {
		monitor.ObserveSuccess(summary.FreshnessHint, time.Now().UTC())
	}
	logSummary(stderr, monitor, summary)

	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		_, _ = fmt.Fprintf(stderr, "collector: write result: %v\n", errors.Unwrap(err))
		return 1
	}
	return 0
}

// logSummary writes one bounded health line to standard error. The summary
// carries counts and bounded identifiers only: never a token, URL, or provider
// body.
func logSummary(stderr io.Writer, monitor *collector.Monitor, summary collector.Summary) {
	health, resource := monitor.Snapshot()
	line, err := json.Marshal(map[string]any{"health": health, "resource": resource, "claim": summary})
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(stderr, "collector health: %s\n", line)
}
