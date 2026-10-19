// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package transportarchive

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func streamFixture(t *testing.T) []byte {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	if err := tw.WriteHeader(&tar.Header{Name: "payload", Typeflag: tar.TypeReg, Mode: 0o751, Size: 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("content")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

type checkedEOFReader struct {
	io.Reader
	check func()
	err   error
}

func (r checkedEOFReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF {
		r.check()
		if r.err != nil {
			return n, r.err
		}
	}
	return n, err
}

func TestStreamPublicationRequiresCompleteVerifiedEOF(t *testing.T) {
	for _, tc := range []struct {
		name       string
		trailing   string
		sizeChange int64
		badDigest  bool
		lateError  bool
		want       e.Code
	}{
		{name: "exact"},
		{name: "authenticated trailing bytes", trailing: "carrier padding"},
		{name: "whole archive digest", badDigest: true, want: e.CodeVerifyFailed},
		{name: "trailing bytes included in digest", trailing: "carrier padding", badDigest: true, want: e.CodeVerifyFailed},
		{name: "truncated declared size", sizeChange: 1, want: e.CodeVerifyFailed},
		{name: "extra source byte", sizeChange: -1, want: e.CodeVerifyFailed},
		{name: "error after tar end", lateError: true, want: e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := append(streamFixture(t), tc.trailing...)
			digest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
			if tc.badDigest {
				digest = "sha256:" + strings.Repeat("0", 64)
			}
			root := t.TempDir()
			checked := false
			reader := checkedEOFReader{Reader: bytes.NewReader(raw), check: func() {
				checked = true
				if _, err := os.Lstat(filepath.Join(root, "payload")); !os.IsNotExist(err) {
					t.Fatalf("payload was published before source EOF: %v", err)
				}
			}}
			if tc.lateError {
				reader.err = io.ErrUnexpectedEOF
			}
			err := UnpackStream(t.Context(), reader, int64(len(raw))+tc.sizeChange, digest, root, []string{"payload"})
			if tc.want != "" {
				if transportCode(err) != tc.want {
					t.Fatalf("error = %v, want %s", err, tc.want)
				}
				entries, err := os.ReadDir(root)
				if err != nil || len(entries) != 0 {
					t.Fatalf("rejected stream left filesystem output: %v", err)
				}
				return
			}
			if err != nil || !checked {
				t.Fatalf("complete source was not verified: checked=%v error=%v", checked, err)
			}
			got, err := os.ReadFile(filepath.Join(root, "payload"))
			if err != nil || string(got) != "content" {
				t.Fatalf("streamed contents = %q, error=%v", got, err)
			}
		})
	}
}

func TestStreamRejectsCapabilitiesBeforeFilesystemWork(t *testing.T) {
	for _, tc := range []struct {
		name   string
		size   int64
		digest string
		nilCtx bool
		cancel bool
		want   e.Code
	}{
		{name: "negative size", size: -1, digest: "sha256:" + strings.Repeat("0", 64), want: e.CodeVerifyFailed},
		{name: "excess size", size: MaxArchiveBytes + 1, digest: "sha256:" + strings.Repeat("0", 64), want: e.CodeVerifyFailed},
		{name: "wrong digest algorithm", digest: "sha512:" + strings.Repeat("0", 64), want: e.CodeVerifyFailed},
		{name: "uppercase digest", digest: "sha256:" + strings.Repeat("A", 64), want: e.CodeVerifyFailed},
		{name: "nil context", nilCtx: true, want: e.CodeUsage},
		{name: "canceled", cancel: true, want: e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.nilCtx {
				ctx = nil
			} else if tc.cancel {
				cancel()
			}
			root := filepath.Join(t.TempDir(), "absent")
			err := UnpackStream(ctx, bytes.NewReader(nil), tc.size, tc.digest, root, []string{"payload"})
			if transportCode(err) != tc.want {
				t.Fatalf("error = %v, want %s", err, tc.want)
			}
			if _, err := os.Lstat(root); !os.IsNotExist(err) {
				t.Fatalf("invalid stream touched destination: %v", err)
			}
		})
	}
}
