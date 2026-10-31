// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"archive/tar"
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

func companionArchive(t *testing.T, members map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"bin/tool", "tool.sig", "extra.sig"} {
		content, ok := members[name]
		if !ok {
			continue
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(content)), Mode: 0o644, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestToolCompanionPinResolution(t *testing.T) {
	for _, test := range []struct {
		name       string
		members    map[string]string
		companions []string
		want       e.Code
	}{
		{name: "companion pinned", members: map[string]string{"bin/tool": "exe", "tool.sig": "attestation"}, companions: []string{"tool.sig"}},
		{name: "no companions", members: map[string]string{"bin/tool": "exe", "tool.sig": "attestation"}},
		{name: "missing companion", members: map[string]string{"bin/tool": "exe"}, companions: []string{"tool.sig"}, want: e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := companionArchive(t, test.members)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(raw) }))
			defer server.Close()
			source := ToolSource{Format: "tar-gzip", URL: server.URL + "/{{.Tag}}/tool.tar.gz", Platform: "linux/amd64",
				ExecutablePath: "bin/tool", Companions: test.companions}
			pin := Pin{Tag: "v1.2.3"}
			tool, err := resolveToolWithClient(t.Context(), source, pin, server.Client())
			if pinErrorCode(err) != test.want {
				t.Fatalf("error %v; want %s", err, test.want)
			}
			if test.want != "" {
				return
			}
			if len(tool.Companions) != len(test.companions) {
				t.Fatalf("companions %+v; want %v", tool.Companions, test.companions)
			}
			for i, path := range test.companions {
				want := ToolCompanion{Path: path, Digest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(test.members[path])))}
				if tool.Companions[i] != want {
					t.Fatalf("companion %+v; want %+v", tool.Companions[i], want)
				}
			}
			pin.Tool = &tool
			if err := checkToolPin(&source, pin); err != nil {
				t.Fatal(err)
			}
			source.Companions = append(source.Companions, "x.sig")
			if pinErrorCode(checkToolPin(&source, pin)) != e.CodeUnpinnedSource {
				t.Fatal("stale companion declaration accepted")
			}
		})
	}
}

func TestToolCompanionDeclarationRejectsAmbiguity(t *testing.T) {
	for _, test := range []struct {
		name       string
		format     string
		companions []string
	}{
		{name: "raw asset", format: "raw", companions: []string{"tool.sig"}},
		{name: "unsafe path", companions: []string{"../tool.sig"}},
		{name: "absolute path", companions: []string{"/tool.sig"}},
		{name: "unsorted", companions: []string{"b.sig", "a.sig"}},
		{name: "duplicate", companions: []string{"a.sig", "a.sig"}},
		{name: "executable name", companions: []string{"dist/tool"}},
		{name: "colliding names", companions: []string{"a/tool.sig", "b/tool.sig"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			format := test.format
			if format == "" {
				format = "tar-gzip"
			}
			source := ToolSource{Format: format, URL: "https://example.test/{{.Tag}}/tool", Platform: "linux/amd64",
				ExecutablePath: "bin/tool", Companions: test.companions}
			if pinErrorCode(validateToolSource(source)) != e.CodeInvalidCatalog {
				t.Fatal("ambiguous companion declaration admitted")
			}
		})
	}
	pin := ToolPin{Format: "tar-gzip", URL: "https://example.test/tool", Platform: "linux/amd64", ExecutablePath: "bin/tool",
		ArchiveDigest: goodArtifactDigest, ExecutableDigest: goodArtifactDigest, Companions: []ToolCompanion{{Path: "tool.sig"}}}
	if pinErrorCode(validateToolPin(pin)) != e.CodeUnpinnedSource {
		t.Fatal("unpinned companion accepted")
	}
	lock := &PinLock{Pins: map[string]Pin{"tool": {Tool: &pin}}}
	clone := clonePinLock(lock)
	clone.Pins["tool"].Tool.Companions[0].Path = "changed"
	if lock.Pins["tool"].Tool.Companions[0].Path != "tool.sig" {
		t.Fatal("lock clone aliases tool companions")
	}
}

func TestReleaseExecutableCompanionOwnership(t *testing.T) {
	registry := `apiVersion: nvkr.dev/catalog/v3
kind: SourceBuildRegistry
sources:
  upstream:
    repo: https://github.com/example/tool
    constraint: 1.2.3
    tool:
      format: tar-gzip
      url: https://example.test/{{.Tag}}/tool.tar.gz
      platform: linux/amd64
      executablePath: bin/tool
      companions: [tool-attestation.sigstore.json]
components:
  tool:
    source: upstream
    platforms: [linux/amd64]
    outputType: host-package
    prebuilt:
      releaseExecutable: true
      evidence: {none: true}
`
	companionDigest := "sha256:" + strings.Repeat("c", 64)
	lock := `apiVersion: nvkr.dev/lock/v1
kind: PinLock
pins:
  upstream:
    tag: v1.2.3
    commit: ` + goodCommit + `
    treeHash: ` + goodTree + `
    sourceDateEpoch: 123
    tool:
      format: tar-gzip
      url: https://example.test/v1.2.3/tool.tar.gz
      platform: linux/amd64
      executablePath: bin/tool
      archiveDigest: ` + goodArtifactDigest + `
      executableDigest: sha256:` + strings.Repeat("b", 64) + `
      companions:
        - path: tool-attestation.sigstore.json
          digest: ` + companionDigest + `
`
	for _, test := range []struct {
		name     string
		registry string
		lock     string
		code     e.Code
	}{
		{name: "source owns companion pins", registry: registry, lock: lock},
		{name: "lock lacks companion", registry: registry,
			lock: lock[:strings.Index(lock, "      companions:")], code: e.CodeUnpinnedSource},
		{name: "lock pins undeclared companion",
			registry: strings.ReplaceAll(registry, "      companions: [tool-attestation.sigstore.json]\n", ""), lock: lock, code: e.CodeUnpinnedSource},
		{name: "companion digest missing", registry: registry,
			lock: strings.ReplaceAll(lock, "          digest: "+companionDigest+"\n", ""), code: e.CodeUnpinnedSource},
		{name: "companion shadows executable", registry: strings.ReplaceAll(registry, "[tool-attestation.sigstore.json]", "[dist/tool]"),
			lock: strings.ReplaceAll(lock, "path: tool-attestation.sigstore.json", "path: dist/tool"), code: e.CodeInvalidCatalog},
		{name: "companion shadows component name", registry: strings.ReplaceAll(strings.ReplaceAll(registry,
			"[tool-attestation.sigstore.json]", "[dist/kit]"), "  tool:\n    source:", "  kit:\n    source:"),
			lock: strings.ReplaceAll(lock, "path: tool-attestation.sigstore.json", "path: dist/kit"), code: e.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			cat, err := LoadContext(t.Context(), bytesProvider{RegistryFile: []byte(test.registry), LockFile: []byte(test.lock)})
			if pinErrorCode(err) != test.code {
				t.Fatalf("error %v; want %s", err, test.code)
			}
			if test.code != "" {
				return
			}
			executable := cat.Components["tool"].Prebuilt.Executable
			if len(executable.Companions) != 1 || executable.Companions[0] != (ToolCompanion{Path: "tool-attestation.sigstore.json", Digest: companionDigest}) {
				t.Fatalf("wrong resolved companions %+v", executable.Companions)
			}
			executable.Companions[0].Path = "changed"
			if cat.Tools["upstream"].Companions[0].Path != "tool-attestation.sigstore.json" {
				t.Fatal("resolved component aliases catalog tool pin")
			}
		})
	}
}
