// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

// Exercise the actual standard-library body error and its conversion from the
// internal HTTP/2 StreamError to the pinned public x/net/http2 type.
func TestHTTP2ChunkReadRecovery(t *testing.T) {
	for _, test := range []struct {
		name         string
		reset        http2.ErrCode
		failures     int
		wrongArchive bool
		cancelWait   bool
		wantAttempts int
		wantCode     e.Code
	}{
		{name: "peer protocol reset", reset: http2.ErrCodeProtocol, failures: 1, wantAttempts: 2},
		{name: "peer internal reset", reset: http2.ErrCodeInternal, failures: 1, wantAttempts: 2},
		{name: "peer refused reset", reset: http2.ErrCodeRefusedStream, failures: 1, wantAttempts: 2},
		{name: "bounded exhaustion", reset: http2.ErrCodeProtocol, failures: 3, wantAttempts: 3, wantCode: e.CodeUnavailable},
		{name: "archive verification after replay", reset: http2.ErrCodeProtocol, failures: 1, wrongArchive: true, wantAttempts: 2, wantCode: e.CodeVerifyFailed},
		{name: "cancellation during wait", reset: http2.ErrCodeProtocol, failures: 1, cancelWait: true, wantAttempts: 1, wantCode: e.CodeTimeout},
		{name: "peer cancellation rejected", reset: http2.ErrCodeCancel, failures: 1, wantAttempts: 1, wantCode: e.CodeFetchFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			locator, layers, _, manifest := chunkFixture(t)
			if test.wrongArchive {
				locator.ArchiveDigest = "sha256:" + strings.Repeat("0", 64)
			}
			chunks := make([][]byte, len(layers))
			for index, layer := range layers {
				body, err := layer.Compressed()
				if err != nil {
					t.Fatal(err)
				}
				chunks[index], err = io.ReadAll(body)
				if closeErr := body.Close(); err != nil || closeErr != nil {
					t.Fatalf("reading fixture: %v / %v", err, closeErr)
				}
			}
			client, endpoint := newHTTP2ChunkServer(t, chunks, test.failures, test.reset)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			restored := filepath.Join(t.TempDir(), "restored")
			opens, closes := make([]int, len(layers)), make([]int, len(layers))
			resets, waits, cleanups := 0, 0, 0
			for index, layer := range layers {
				layers[index] = editedChunk{Layer: layer, open: func() (io.ReadCloser, error) {
					if index == 0 && opens[index] != cleanups {
						t.Fatal("network retry began before failed staging cleanup")
					}
					opens[index]++
					req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/"+strconv.Itoa(index), nil)
					if err != nil {
						return nil, err
					}
					response, err := client.Do(req)
					if err != nil {
						return nil, err
					}
					if response.ProtoMajor != 2 {
						_ = response.Body.Close()
						t.Fatal("fixture did not negotiate HTTP/2")
					}
					observer := &observedHTTP2Chunk{ReadCloser: response.Body, observed: func(n int, err error) {
						if err == nil || err == io.EOF {
							return
						}
						var stream http2.StreamError
						if !errors.As(err, &stream) || stream.Code != test.reset || stream.Cause == nil || stream.Cause.Error() != "received from peer" || n == 0 {
							t.Errorf("expected real peer reset after body progress; received %T, bytes=%d", err, n)
						}
						resets++
					}}
					return &chunkRetryCloser{Reader: observer, close: func() error {
						closes[index]++
						return observer.Close()
					}}, nil
				}}
			}
			wait := func(ctx context.Context, delay time.Duration) error {
				waits++
				if delay != time.Duration(waits)*time.Second || !slices.Equal(opens, closes) {
					t.Fatal("retry delay or response closure contract changed")
				}
				if _, err := os.Lstat(restored); !os.IsNotExist(err) {
					t.Fatal("partial extraction survived until retry wait")
				}
				if test.cancelWait {
					cancel()
					return waitChunkRetry(ctx, time.Hour)
				}
				return ctx.Err()
			}
			err := unpackChunkArchive(ctx, layers, manifest, locator, restored, wait, func(path string) error {
				cleanups++
				return os.RemoveAll(path)
			})
			if test.wantCode == "" {
				if err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(filepath.Join(restored, "payload/bin/tool"))
				if err != nil || string(raw) != strings.Repeat("binary bytes ", 180) {
					t.Fatalf("replayed archive differs: %v", err)
				}
			} else {
				assertCode(t, err, test.wantCode)
				if _, err := os.Lstat(restored); !os.IsNotExist(err) {
					t.Fatal("failed transfer retained partial output")
				}
			}
			if !slices.Equal(opens, closes) || resets != test.failures {
				t.Fatalf("body reset/closure mismatch: resets=%d, opens=%v, closes=%v", resets, opens, closes)
			}
			for _, count := range opens {
				if count != test.wantAttempts {
					t.Fatalf("recovery did not restart at byte zero within its bound: %v", opens)
				}
			}
		})
	}
}

type observedHTTP2Chunk struct {
	io.ReadCloser
	read     int
	observed func(int, error)
}

func (body *observedHTTP2Chunk) Read(p []byte) (int, error) {
	n, err := body.ReadCloser.Read(p)
	body.read += n
	body.observed(body.read, err)
	return n, err
}

// net/http intentionally offers no handler API for selecting an RST_STREAM
// code. This TLS peer emits real frames while the client retains the production
// transport. httptest joins connections; each also has a bounded lifetime.
func newHTTP2ChunkServer(t *testing.T, chunks [][]byte, failures int, reset http2.ErrCode) (*http.Client, string) {
	t.Helper()
	server := httptest.NewUnstartedServer(nil)
	server.EnableHTTP2 = true
	var mu sync.Mutex
	attempts := 0
	server.Config.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){
		"h2": func(_ *http.Server, conn *tls.Conn, _ http.Handler) {
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
				t.Error(err)
				return
			}
			preface := make([]byte, len(http2.ClientPreface))
			if _, err := io.ReadFull(conn, preface); err != nil || string(preface) != http2.ClientPreface {
				t.Errorf("invalid HTTP/2 preface: %v", err)
				return
			}
			framer := http2.NewFramer(conn, conn)
			if err := framer.WriteSettings(); err != nil {
				t.Error(err)
				return
			}
			decoder := hpack.NewDecoder(4096, nil)
			for {
				frame, err := framer.ReadFrame()
				if err != nil {
					return
				}
				switch frame := frame.(type) {
				case *http2.SettingsFrame:
					if !frame.IsAck() {
						if err := framer.WriteSettingsAck(); err != nil {
							return
						}
					}
				case *http2.HeadersFrame:
					fields, err := decoder.DecodeFull(frame.HeaderBlockFragment())
					if err != nil || !frame.HeadersEnded() {
						t.Errorf("invalid fixture request headers: %v", err)
						return
					}
					index := -1
					for _, field := range fields {
						if field.Name == ":path" {
							index, err = strconv.Atoi(strings.TrimPrefix(field.Value, "/"))
						}
					}
					if err != nil || index < 0 || index >= len(chunks) {
						t.Error("invalid fixture chunk request")
						return
					}
					var headers bytes.Buffer
					encoder := hpack.NewEncoder(&headers)
					for _, field := range []hpack.HeaderField{{Name: ":status", Value: "200"}, {Name: "content-length", Value: strconv.Itoa(len(chunks[index]))}} {
						if err := encoder.WriteField(field); err != nil {
							t.Error(err)
							return
						}
					}
					if err := framer.WriteHeaders(http2.HeadersFrameParam{StreamID: frame.StreamID, BlockFragment: headers.Bytes(), EndHeaders: true}); err != nil {
						return
					}
					mu.Lock()
					if index == 0 {
						attempts++
					}
					interrupt := index == len(chunks)-1 && attempts <= failures
					mu.Unlock()
					data := chunks[index]
					if interrupt {
						data = data[:len(data)/2]
					}
					if err := framer.WriteData(frame.StreamID, !interrupt, data); err != nil {
						return
					}
					if interrupt {
						if err := framer.WriteRSTStream(frame.StreamID, reset); err != nil {
							return
						}
					}
				case *http2.PingFrame:
					if !frame.IsAck() {
						if err := framer.WritePing(true, frame.Data); err != nil {
							return
						}
					}
				case *http2.GoAwayFrame:
					return
				case *http2.WindowUpdateFrame, *http2.RSTStreamFrame:
				default:
					t.Errorf("unexpected fixture frame %T", frame)
					return
				}
			}
		},
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	client := server.Client()
	t.Cleanup(client.CloseIdleConnections)
	return client, server.URL
}
