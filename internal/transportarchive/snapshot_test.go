// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// These tests cover the snapshot-free carrier extraction contract.
package transportarchive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestTransportDigestRejectsChangedOrUnavailableSource(t *testing.T) {
	for _, name := range []string{"exact", "closed", "changed", "canceled", "missing source", "missing metadata"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source")
			payload := []byte("exact archive bytes")
			if err := os.WriteFile(path, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			held, info, err := securepath.OpenRegular(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = held.Close() })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			source := held
			switch name {
			case "closed":
				if err := held.Close(); err != nil {
					t.Fatal(err)
				}
			case "changed":
				if err := os.WriteFile(path, []byte("different and longer bytes"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				cancel()
			case "missing source":
				source = nil
			case "missing metadata":
				info = nil
			}
			digest, metadata, err := stableTransportDigest(ctx, source, info)
			if name == "exact" {
				if err != nil || digest != fmt.Sprintf("sha256:%x", sha256.Sum256(payload)) || metadata == nil {
					t.Fatalf("recheck = %q, %v, %v", digest, metadata, err)
				}
			} else if err == nil || digest != "" || metadata != nil {
				t.Fatalf("invalid recheck returned identity: %q, %v, %v", digest, metadata, err)
			}
		})
	}
}

// Observe synchronous context checkpoints instead of racing extraction in a
// goroutine. The source is changed after admission, read in that changed state,
// and restored (including mtime) before the final held-inode rehash.
func TestUnpackRejectsTransientSourceMutation(t *testing.T) {
	for _, member := range []string{"payload", "trailing bytes"} {
		t.Run(member, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "carrier.tar")
			writeTestTar(t, path, []*tar.Header{{Name: "artifact", Typeflag: tar.TypeReg, Mode: 0o600, Size: 16}})
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// Keep the trailing mutation beyond bufio's first read, so it is
			// consumed only by the drain after tar.Reader reports EOF.
			original = append(original, make([]byte, 2*transportCopyBufferSize)...)
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			held, info, err := securepath.OpenRegular(path)
			if err != nil {
				t.Fatal(err)
			}
			defer held.Close()
			writer, err := os.OpenFile(path, os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			mutationOffset := int64(512)
			if member == "trailing bytes" {
				mutationOffset = int64(len(original) - 1)
			}
			root := t.TempDir()
			mutated, restored := false, false
			ctx := observingTransportContext{Context: t.Context(), observe: func() {
				offset, err := held.Seek(0, io.SeekCurrent)
				if err != nil {
					t.Fatal(err)
				}
				if !mutated {
					entries, err := os.ReadDir(root)
					if err != nil {
						t.Fatal(err)
					}
					if len(entries) == 0 {
						return
					}
					if offset != 0 {
						t.Fatalf("mutation began after extraction read %d bytes", offset)
					}
					if _, err := writer.WriteAt([]byte{'y'}, mutationOffset); err != nil {
						t.Fatal(err)
					}
					mutated = true
				} else if !restored && offset > mutationOffset {
					if _, err := writer.WriteAt(original[mutationOffset:mutationOffset+1], mutationOffset); err != nil {
						t.Fatal(err)
					}
					restored = true
				}
				if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
			}}
			err = UnpackOpen(ctx, held, root, []string{"artifact"})
			if !mutated || !restored {
				t.Fatalf("mutation was not fully exercised: changed=%t restored=%t", mutated, restored)
			}
			if transportCode(err) != e.CodeVerifyFailed || !strings.Contains(err.Error(), "changed while unpacking") {
				t.Fatalf("transient mutation returned %v, want digest mismatch", err)
			}
			assertEmptyTransportRoot(t, root)
		})
	}
}

func TestUnpackChecksCompleteStreamBeforePublication(t *testing.T) {
	for _, tc := range []struct {
		name string
		gzip bool
		code e.Code
	}{
		{name: "plain trailing bytes"},
		{name: "gzip trailing decompressed bytes", gzip: true},
		{name: "gzip corrupt checksum", gzip: true, code: e.CodeVerifyFailed},
		{name: "truncated while draining", code: e.CodeVerifyFailed},
		{name: "grown while draining", code: e.CodeVerifyFailed},
		{name: "canceled while draining", code: e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "carrier")
			writeTestTar(t, path, []*tar.Header{{Name: "artifact", Typeflag: tar.TypeReg, Mode: 0o600, Size: 16}})
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			tarLength := int64(len(raw))
			raw = append(raw, bytes.Repeat([]byte{'z'}, 2*transportCopyBufferSize)...)
			if tc.gzip {
				var compressed bytes.Buffer
				writer := gzip.NewWriter(&compressed)
				if _, err := writer.Write(raw); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				raw = compressed.Bytes()
				if tc.code != "" {
					raw[len(raw)-8] ^= 1
				}
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			held, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer held.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			injected := false
			observed := observingTransportContext{Context: ctx, observe: func() {
				if tc.gzip || tc.code == "" || injected {
					return
				}
				offset, err := held.Seek(0, io.SeekCurrent)
				if err != nil {
					t.Fatal(err)
				}
				if offset <= tarLength {
					return
				}
				injected = true
				switch tc.name {
				case "truncated while draining":
					err = os.Truncate(path, offset)
				case "grown while draining":
					err = os.Truncate(path, int64(len(raw)+1))
				case "canceled while draining":
					cancel()
				}
				if err != nil {
					t.Fatal(err)
				}
			}}
			root := t.TempDir()
			err = unpackOpen(observed, held, root, []string{"artifact"}, tc.gzip)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				payload, err := os.ReadFile(filepath.Join(root, "artifact"))
				if err != nil || string(payload) != strings.Repeat("x", 16) {
					t.Fatalf("published payload = %q, %v", payload, err)
				}
				return
			}
			if transportCode(err) != tc.code {
				t.Fatalf("unpack error = %v, want %s", err, tc.code)
			}
			if !tc.gzip && !injected {
				t.Fatal("stream failure was not exercised")
			}
			assertEmptyTransportRoot(t, root)
		})
	}
}

type observingTransportContext struct {
	context.Context
	observe func()
}

func (ctx observingTransportContext) Err() error {
	ctx.observe()
	return ctx.Context.Err()
}

func assertEmptyTransportRoot(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed extraction left published or staged bytes: %v, %v", entries, err)
	}
}
