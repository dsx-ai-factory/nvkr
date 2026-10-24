// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestReleaseExecutableAcquisition(t *testing.T) {
	for _, format := range []string{"", "zip"} {
		t.Run(format, func(t *testing.T) { testReleaseExecutableAcquisition(t, format) })
	}
}

func testReleaseExecutableAcquisition(t *testing.T, format string) {
	t.Helper()
	executable := []byte("pinned executable")
	var carrier bytes.Buffer
	gz := gzip.NewWriter(&carrier)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "linux-amd64/tool", Size: int64(len(executable)), Mode: 0755, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(executable); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if format == "zip" {
		carrier.Reset()
		zw := zip.NewWriter(&carrier)
		f, err := zw.Create("linux-amd64/tool")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(executable); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
	}
	archive := carrier.Bytes()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer server.Close()
	for _, test := range []struct {
		name   string
		change func(*ir.Node)
		code   e.Code
	}{
		{name: "exact"},
		{name: "wrong archive format", change: func(n *ir.Node) {
			if n.ExecutableArchive.Format == "zip" {
				n.ExecutableArchive.Format = ""
			} else {
				n.ExecutableArchive.Format = "zip"
			}
		}, code: e.CodeVerifyFailed},
		{name: "changed archive", change: func(n *ir.Node) { n.ExecutableArchive.Digest = sha256Of([]byte("other archive")) }, code: e.CodeVerifyFailed},
		{name: "changed executable", change: func(n *ir.Node) { n.Digest = sha256Of([]byte("other executable")) }, code: e.CodeVerifyFailed},
		{name: "missing member", change: func(n *ir.Node) { n.ExecutableArchive.Member = "other" }, code: e.CodeVerifyFailed},
		{name: "unsafe member", change: func(n *ir.Node) { n.ExecutableArchive.Member = "../tool" }, code: e.CodeInvalidCatalog},
		{name: "wrong delivery", change: func(n *ir.Node) { n.OutputType = "raw"; n.Env = nil }, code: e.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			prior := []byte("previous output")
			if err := os.WriteFile(filepath.Join(root, "tool"), prior, 0600); err != nil {
				t.Fatal(err)
			}
			node := ir.Node{ID: "pull-tool", Kind: ir.KindPull, Component: "tool", Artifact: server.URL,
				Digest: sha256Of(executable), Platform: "linux/amd64", Network: ir.NetworkDefault,
				OutputType: "host-package", ArtifactPath: "tool", Version: "1.2.3",
				Env:               []ir.EnvVar{{Name: "SOURCE_DATE_EPOCH", Value: "1"}},
				ExecutableArchive: &ir.ExecutableArchive{Format: format, Digest: sha256Of(archive), Member: "linux-amd64/tool"}}
			if test.change != nil {
				test.change(&node)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Hour)
			defer cancel()
			deadline, _ := ctx.Deadline()
			transport := releaseExecutableRoundTripper(func(request *http.Request) (*http.Response, error) {
				if actual, bounded := request.Context().Deadline(); !bounded || !actual.Equal(deadline) {
					t.Error("payload transport replaced the caller deadline")
				}
				return server.Client().Transport.RoundTrip(request)
			})
			digest, size, err := downloadReleaseExecutableWithTransport(ctx, node, root, "tool", transport)
			if code(err) != test.code {
				t.Fatalf("error %v; want %s", err, test.code)
			}
			want := executable
			if test.code != "" {
				want = prior
				if digest != "" || size != 0 {
					t.Fatal("failed acquisition returned a payload")
				}
			} else if digest != sha256Of(executable) || size != int64(len(executable)) {
				t.Fatal("result uses archive identity rather than executable identity")
			}
			got, err := os.ReadFile(filepath.Join(root, "tool"))
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("output %q, error %v", got, err)
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".") {
					t.Fatalf("private acquisition residue %s", entry.Name())
				}
			}
		})
	}
}
