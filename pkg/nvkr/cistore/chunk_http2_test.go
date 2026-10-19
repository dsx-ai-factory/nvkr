// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"golang.org/x/net/http2"
)

func TestChunkHTTP2ResetAdmission(t *testing.T) {
	peer := func(code http2.ErrCode) error {
		return http2.StreamError{StreamID: 3, Code: code, Cause: errors.New("received from peer")}
	}
	for _, test := range []struct {
		name, category string
		cause          error
		code           e.Code
		retry          bool
	}{
		{"peer protocol", "http2-peer-protocol-reset", peer(http2.ErrCodeProtocol), e.CodeUnavailable, true},
		{"peer internal", "http2-peer-internal-reset", peer(http2.ErrCodeInternal), e.CodeUnavailable, true},
		{"peer refused stream", "http2-peer-refused-stream", peer(http2.ErrCodeRefusedStream), e.CodeUnavailable, true},
		{"wrapped peer", "http2-peer-protocol-reset", fmt.Errorf("credential-marker: %w", &url.Error{Op: "Get", URL: "https://example.test/?secret=credential-marker", Err: peer(http2.ErrCodeProtocol)}), e.CodeUnavailable, true},
		{"local protocol", "http2-stream", http2.StreamError{StreamID: 3, Code: http2.ErrCodeProtocol, Cause: errors.New("credential-marker")}, e.CodeFetchFailed, false},
		{"missing peer cause", "http2-stream", http2.StreamError{StreamID: 3, Code: http2.ErrCodeProtocol}, e.CodeFetchFailed, false},
		{"zero stream", "http2-stream", http2.StreamError{Code: http2.ErrCodeProtocol, Cause: errors.New("received from peer")}, e.CodeFetchFailed, false},
		{"peer cancel", "http2-stream", peer(http2.ErrCodeCancel), e.CodeFetchFailed, false},
		{"peer inadequate security", "http2-stream", peer(http2.ErrCodeInadequateSecurity), e.CodeFetchFailed, false},
		{"unknown peer code", "http2-stream", peer(http2.ErrCode(99)), e.CodeFetchFailed, false},
		{"lookalike text", "http2-protocol", errors.New("stream error: stream ID 3; PROTOCOL_ERROR; received from peer"), e.CodeFetchFailed, false},
		{"connection failure", "unknown", http2.ConnectionError(http2.ErrCodeProtocol), e.CodeFetchFailed, false},
		{"filesystem read", "filesystem-failure", &os.PathError{Op: "read", Path: "credential-marker", Err: peer(http2.ErrCodeProtocol)}, e.CodeFetchFailed, false},
		{"filesystem link", "filesystem-failure", &os.LinkError{Op: "link", Old: "credential-marker", New: "credential-marker", Err: peer(http2.ErrCodeProtocol)}, e.CodeFetchFailed, false},
		{"joined resets", "http2-peer-protocol-reset", errors.Join(peer(http2.ErrCodeProtocol), peer(http2.ErrCodeInternal)), e.CodeUnavailable, false},
		{"joined EOF", "http2-peer-protocol-reset", errors.Join(peer(http2.ErrCodeProtocol), io.ErrUnexpectedEOF), e.CodeUnavailable, false},
		{"joined integrity failure", "integrity-rejected", errors.Join(peer(http2.ErrCodeProtocol), e.New(e.CodeVerifyFailed, "credential-marker")), e.CodeVerifyFailed, false},
		{"joined cancellation", "context-canceled", errors.Join(peer(http2.ErrCodeProtocol), context.Canceled), e.CodeTimeout, false},
		{"coded failure", "boundary-rejected", e.Wrap(e.CodeFetchFailed, peer(http2.ErrCodeProtocol), "credential-marker"), e.CodeFetchFailed, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, operation := range []string{"reading", "opening", "closing"} {
				t.Run(operation, func(t *testing.T) {
					reader := &chunkReader{ctx: t.Context(), descriptors: []v1.Descriptor{{Size: 100}}, remaining: 60}
					err := reader.ioFailure(operation, test.cause)
					wantCode := test.code
					if operation == "closing" && wantCode != e.CodeTimeout {
						wantCode = e.CodeFetchFailed
					}
					assertCode(t, err, wantCode)
					if !strings.Contains(err.Error(), "diagnostic: "+test.category+";") || !strings.Contains(err.Error(), "read=40/100") {
						t.Fatalf("lost safe HTTP/2 diagnostic: %v", err)
					}
					if isRetryableChunkRead(err) != (test.retry && operation == "reading") {
						t.Fatalf("incorrect HTTP/2 retry admission: %v", err)
					}
					if strings.Contains(err.Error(), "credential-marker") || strings.Contains(err.Error(), "example.test") || errors.Is(err, test.cause) {
						t.Fatal("raw provider diagnostic escaped")
					}
				})
			}
		})
	}
}
