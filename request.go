// SPDX-License-Identifier: MIT
// Copyright (c) 2025-2026 eshu-hq

package collector

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	sdk "github.com/eshu-hq/eshu-sdk/collector"
)

// maxRequestBytes bounds the Request document read from standard input. The
// host sends the claim, the contract, and the activation config, which are a
// few kilobytes.
const maxRequestBytes = 1 << 20

// Request is the bounded JSON document the host passes to the process. It
// mirrors the host's Request type.
type Request struct {
	ProtocolVersion string         `json:"protocol_version"`
	Claim           sdk.Claim      `json:"claim"`
	Contract        sdk.Contract   `json:"contract"`
	Config          map[string]any `json:"config,omitempty"`
}

// DecodeRequest reads exactly one Request from r. It rejects unknown fields,
// trailing JSON, oversized input, and an unsupported protocol version. The
// error text never quotes the input.
func DecodeRequest(r io.Reader) (Request, error) {
	limited := &io.LimitedReader{R: r, N: maxRequestBytes + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		if limited.N <= 0 {
			return Request{}, fmt.Errorf("decode SDK request: input exceeds %d bytes", maxRequestBytes)
		}
		return Request{}, fmt.Errorf("decode SDK request: %w", requestDecodeCause(err))
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Request{}, errors.New("decode SDK request: trailing JSON value")
	}
	if limited.N <= 0 {
		return Request{}, fmt.Errorf("decode SDK request: input exceeds %d bytes", maxRequestBytes)
	}
	if strings.TrimSpace(request.ProtocolVersion) != sdk.ProtocolVersionV1Alpha1 {
		return Request{}, fmt.Errorf("protocol_version %q is unsupported", request.ProtocolVersion)
	}
	return request, nil
}

// requestDecodeCause keeps the JSON error category and drops any quoted value.
func requestDecodeCause(err error) error {
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		return fmt.Errorf("invalid JSON at byte %d", syntax.Offset)
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return fmt.Errorf("field %q has the wrong type", typeErr.Field)
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return errors.New("input ended early")
	}
	return errors.New("invalid request")
}
