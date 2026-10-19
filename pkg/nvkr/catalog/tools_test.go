// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestToolReleasePinResolutionAndVersionBinding(t *testing.T) {
	for _, tc := range []struct {
		name, format, member string
		status               int
		want                 e.Code
	}{
		{"raw", "raw", "tool", 200, ""}, {"archive", "tar-gzip", "tool", 200, ""}, {"ZIP", "zip", "tool", 200, ""},
		{"ZIP missing executable", "zip", "other", 200, e.CodeVerifyFailed},
		{"ZIP unsafe member", "zip", "../tool", 200, e.CodeVerifyFailed},
		{"missing executable", "tar-gzip", "other", 200, e.CodeVerifyFailed},
		{"unsafe member", "tar-gzip", "../tool", 200, e.CodeVerifyFailed},
		{"not found", "raw", "tool", 404, e.CodeFetchFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executable := []byte("exact release executable")
			raw := executable
			if tc.format == "tar-gzip" {
				var b bytes.Buffer
				gz := gzip.NewWriter(&b)
				tw := tar.NewWriter(gz)
				if err := tw.WriteHeader(&tar.Header{Name: tc.member, Size: int64(len(executable)), Mode: 0755, Typeflag: tar.TypeReg}); err != nil {
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
				raw = b.Bytes()
			}
			if tc.format == "zip" {
				var b bytes.Buffer
				zw := zip.NewWriter(&b)
				f, err := zw.Create(tc.member)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.Write(executable); err != nil {
					t.Fatal(err)
				}
				if err := zw.Close(); err != nil {
					t.Fatal(err)
				}
				raw = b.Bytes()
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1.2.3/tool-1.2.3" {
					t.Errorf("unresolved version path %s", r.URL.Path)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write(raw)
			}))
			defer server.Close()
			source := ToolSource{Format: tc.format, URL: server.URL + "/{{.Tag}}/tool-{{.Version}}", Platform: "linux/amd64", ExecutablePath: "tool"}
			pin := Pin{Tag: "v1.2.3"}
			tool, err := resolveToolWithClient(t.Context(), source, pin, server.Client())
			if tc.want != "" {
				if pinErrorCode(err) != tc.want {
					t.Fatalf("%v, want %s", err, tc.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tool.ArchiveDigest != fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) || tool.ExecutableDigest != fmt.Sprintf("sha256:%x", sha256.Sum256(executable)) {
				t.Fatal("incorrect byte identity")
			}
			pin.Tool = &tool
			if err := checkToolPin(&source, pin); err != nil {
				t.Fatal(err)
			}
			pin.Tag = "v1.2.4"
			if pinErrorCode(checkToolPin(&source, pin)) != e.CodeUnpinnedSource {
				t.Fatal("stale version accepted")
			}
		})
	}
}
func TestToolDeclarationAndPinRejectAmbiguity(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ToolSource)
	}{
		{"format", func(s *ToolSource) { s.Format = "7z" }}, {"platform", func(s *ToolSource) { s.Platform = "linux/any" }},
		{"http", func(s *ToolSource) { s.URL = "http://example.test/tool" }}, {"credentials", func(s *ToolSource) { s.URL = "https://token@example.test/tool" }},
		{"template", func(s *ToolSource) { s.URL = "https://example.test/{{.NoSuchField}}" }}, {"path", func(s *ToolSource) { s.ExecutablePath = "bin/../tool" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := ToolSource{Format: "raw", URL: "https://example.test/{{.Tag}}/tool", Platform: "linux/amd64", ExecutablePath: "tool"}
			tc.edit(&s)
			if pinErrorCode(validateToolSource(s)) != e.CodeInvalidCatalog {
				t.Fatal("invalid declaration admitted")
			}
		})
	}
	p := Pin{Tool: &ToolPin{Format: "raw", URL: "https://example.test/tool", Platform: "linux/amd64", ExecutablePath: "tool", ArchiveDigest: "sha256:" + strings.Repeat("a", 64), ExecutableDigest: "sha256:" + strings.Repeat("b", 64)}}
	if pinErrorCode(validateToolPin(*p.Tool)) != e.CodeUnpinnedSource {
		t.Fatal("raw mismatched hashes accepted")
	}
	lock := &PinLock{Pins: map[string]Pin{"tool": p}}
	clone := clonePinLock(lock)
	clone.Pins["tool"].Tool.URL = "https://changed.test/tool"
	if lock.Pins["tool"].Tool.URL == clone.Pins["tool"].Tool.URL {
		t.Fatal("lock clone aliases tool pin")
	}
}
