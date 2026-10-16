// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestChunkReadRetryRestartsAndAuthenticatesWholeArchive(t *testing.T) {
	for _, test := range []struct {
		name         string
		failures     int
		readError    error
		wrongArchive bool
		wantCode     e.Code
		wantAttempts int
		wantCleanups int
	}{
		{name: "first attempt", wantAttempts: 1},
		{name: "second attempt", failures: 1, wantAttempts: 2, wantCleanups: 1},
		{name: "third attempt", failures: 2, wantAttempts: 3, wantCleanups: 2},
		{name: "exhausted", failures: 3, wantCode: e.CodeUnavailable, wantAttempts: 3, wantCleanups: 3},
		{name: "exhausted timeout", failures: 3, readError: syscall.ETIMEDOUT, wantCode: e.CodeTimeout, wantAttempts: 3, wantCleanups: 3},
		{name: "whole archive checked again", failures: 1, wrongArchive: true, wantCode: e.CodeVerifyFailed, wantAttempts: 2, wantCleanups: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			locator, layers, _, manifest := chunkFixture(t)
			if test.wrongArchive {
				locator.ArchiveDigest = "sha256:" + strings.Repeat("0", 64)
			}
			restored := filepath.Join(t.TempDir(), "restored")
			opens, closes := make([]int, len(layers)), make([]int, len(layers))
			cleanups := 0
			for index, layer := range layers {
				layers[index] = editedChunk{Layer: layer, open: func() (io.ReadCloser, error) {
					if index == 0 && opens[index] != cleanups {
						t.Fatal("reopened archive before failed staging cleanup")
					}
					opens[index]++
					input, err := layer.Compressed()
					if err != nil {
						return nil, err
					}
					reader := io.Reader(input)
					if index == len(layers)-1 && opens[index] <= test.failures {
						failure := test.readError
						if failure == nil {
							failure = io.ErrUnexpectedEOF
						}
						reader = io.MultiReader(io.LimitReader(input, 1), chunkRetryReadFailure{failure})
					}
					return &chunkRetryCloser{Reader: reader, close: func() error { closes[index]++; return input.Close() }}, nil
				}}
			}
			var delays []time.Duration
			wait := func(ctx context.Context, delay time.Duration) error {
				if _, err := os.Lstat(restored); !os.IsNotExist(err) {
					t.Fatal("failed archive staging survived until retry wait")
				}
				if !slices.Equal(opens, closes) {
					t.Fatal("retry wait began before every response closed")
				}
				delays = append(delays, delay)
				return ctx.Err()
			}
			cleanup := func(path string) error {
				if path != restored {
					t.Fatal("cleanup escaped its owned extraction path")
				}
				cleanups++
				return os.RemoveAll(path)
			}
			err := unpackChunkArchive(t.Context(), layers, manifest, locator, restored, wait, cleanup)
			if test.wantCode == "" {
				if err != nil {
					t.Fatal(err)
				}
				actual, readErr := os.ReadFile(filepath.Join(restored, "payload/bin/tool"))
				if readErr != nil || string(actual) != strings.Repeat("binary bytes ", 180) {
					t.Fatalf("replayed archive changed payload: %v", readErr)
				}
			} else {
				assertCode(t, err, test.wantCode)
				if _, statErr := os.Lstat(restored); !os.IsNotExist(statErr) {
					t.Fatal("rejected archive retained partial output")
				}
			}
			if cleanups != test.wantCleanups || !slices.Equal(opens, closes) {
				t.Fatalf("cleanup/close lifetime mismatch: %d, %v, %v", cleanups, opens, closes)
			}
			for _, count := range opens {
				if count != test.wantAttempts {
					t.Fatalf("retry resumed a prefix or exceeded its bound: %v", opens)
				}
			}
			wantDelays := []time.Duration{time.Second, 2 * time.Second}[:test.wantAttempts-1]
			if !slices.Equal(delays, wantDelays) {
				t.Fatalf("retry delays = %v; want %v", delays, wantDelays)
			}
		})
	}
}

func TestChunkReadRetryPreservesIndependentArchiveRejection(t *testing.T) {
	header := func(name string) []byte {
		var raw bytes.Buffer
		writer := tar.NewWriter(&raw)
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return raw.Bytes()[:512]
	}
	for _, test := range []struct {
		name     string
		header   []byte
		conflict bool
	}{
		{"invalid TAR header", bytes.Repeat([]byte{'x'}, 512), false},
		{"disallowed path", header("outside-allowed-path"), false},
		{"existing extraction destination", header("payload"), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			locator, layers, _, manifest := chunkFixture(t)
			restored := filepath.Join(t.TempDir(), "restored")
			opens, closes, cleanups := 0, 0, 0
			layers[0] = editedChunk{Layer: layers[0], open: func() (io.ReadCloser, error) {
				opens++
				if test.conflict {
					entries, err := os.ReadDir(restored)
					if err != nil || len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), ".nvkr-unpack-") {
						t.Fatalf("missing private extraction stage: %v", err)
					}
					if err := os.WriteFile(filepath.Join(restored, entries[0].Name(), "payload"), []byte("existing"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				return &chunkRetryCloser{Reader: chunkRetryReadResult{data: test.header, err: io.ErrUnexpectedEOF}, close: func() error {
					closes++
					return nil
				}}, nil
			}}
			err := unpackChunkArchive(t.Context(), layers, manifest, locator, restored,
				func(context.Context, time.Duration) error {
					t.Fatal("independent archive rejection was retried")
					return nil
				}, func(path string) error {
					cleanups++
					return os.RemoveAll(path)
				})
			assertCode(t, err, e.CodeVerifyFailed)
			if isRetryableChunkRead(err) || opens != 1 || closes != 1 || cleanups != 1 {
				t.Fatalf("independent rejection lost precedence: %v / %d / %d / %d", err, opens, closes, cleanups)
			}
			if _, err := os.Lstat(restored); !os.IsNotExist(err) {
				t.Fatal("rejected archive retained partial output")
			}
		})
	}
}

func TestChunkReadRetryStopsAtNonretryableFailures(t *testing.T) {
	for _, test := range []struct {
		name string
		code e.Code
	}{
		{"open", e.CodeUnavailable},
		{"opaque read", e.CodeFetchFailed},
		{"response close", e.CodeFetchFailed},
		{"staging cleanup", e.CodeImmutableViolation},
		{"canceled wait", e.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			locator, layers, _, manifest := chunkFixture(t)
			restored := filepath.Join(t.TempDir(), "restored")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			opens, closes, waits := 0, 0, 0
			last := len(layers) - 1
			original := layers[last]
			layers[last] = editedChunk{Layer: original, open: func() (io.ReadCloser, error) {
				opens++
				if test.name == "open" {
					return nil, io.ErrUnexpectedEOF
				}
				failure := io.ErrUnexpectedEOF
				if test.name == "opaque read" {
					failure = errors.New("untrusted provider URL and token")
				}
				return &chunkRetryCloser{Reader: chunkRetryReadFailure{failure}, close: func() error {
					closes++
					if test.name == "response close" {
						return errors.New("untrusted provider close diagnostic")
					}
					return nil
				}}, nil
			}}
			wait := func(context.Context, time.Duration) error {
				waits++
				if test.name != "canceled wait" {
					t.Fatal("nonretryable error reached retry wait")
				}
				cancel()
				return waitChunkRetry(ctx, time.Hour)
			}
			cleanup := func(path string) error {
				if test.name == "staging cleanup" {
					return errors.New("untrusted cleanup diagnostic")
				}
				return os.RemoveAll(path)
			}
			err := unpackChunkArchive(ctx, layers, manifest, locator, restored, wait, cleanup)
			assertCode(t, err, test.code)
			if strings.Contains(err.Error(), "untrusted") || opens != 1 || (test.name != "open" && closes != 1) {
				t.Fatalf("failure leaked, retried or failed to close: %v / %d / %d", err, opens, closes)
			}
			if (test.name == "canceled wait") != (waits == 1) {
				t.Fatalf("unexpected retry waits: %d", waits)
			}
		})
	}
}

type chunkRetryReadFailure struct{ err error }

func (r chunkRetryReadFailure) Read([]byte) (int, error) { return 0, r.err }

type chunkRetryReadResult struct {
	data []byte
	err  error
}

func (r chunkRetryReadResult) Read(p []byte) (int, error) { return copy(p, r.data), r.err }

type chunkRetryCloser struct {
	io.Reader
	close func() error
}

func (r *chunkRetryCloser) Close() error { return r.close() }
