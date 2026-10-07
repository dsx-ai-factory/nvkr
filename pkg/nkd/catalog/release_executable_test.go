// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestReleaseExecutableDeclarationAndPinOwnership(t *testing.T) {
	registry := `apiVersion: nkd.dev/catalog/v3
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
components:
  tool:
    source: upstream
    platforms: [linux/amd64]
    outputType: host-package
    prebuilt:
      releaseExecutable: true
      evidence: {none: true}
`
	lock := `apiVersion: nkd.dev/lock/v1
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
`
	for _, test := range []struct {
		name   string
		change func(*registryDoc, *PinLock)
		code   e.Code
	}{
		{name: "source owns both pins"},
		{name: "two acquisition declarations", change: func(r *registryDoc, _ *PinLock) {
			r.Components["tool"].Prebuilt.URL = "https://example.test/another"
		}, code: e.CodeInvalidCatalog},
		{name: "wrong platform", change: func(r *registryDoc, _ *PinLock) {
			c := r.Components["tool"]
			c.Platforms = []string{"linux/arm64"}
			r.Components["tool"] = c
		}, code: e.CodeInvalidCatalog},
		{name: "missing tool source", change: func(r *registryDoc, _ *PinLock) {
			s := r.Sources["upstream"]
			s.Tool = nil
			r.Sources["upstream"] = s
		}, code: e.CodeInvalidCatalog},
		{name: "duplicate artifact pin", change: func(_ *registryDoc, l *PinLock) {
			l.Artifacts = map[string]ArtifactPin{"tool": {Tag: "v1.2.3", Digests: map[string]string{"amd64": goodArtifactDigest}}}
		}, code: e.CodeInvalidCatalog},
		{name: "missing executable pin", change: func(_ *registryDoc, l *PinLock) {
			l.Pins["upstream"].Tool.ExecutableDigest = ""
		}, code: e.CodeUnpinnedSource},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, err := decodeRegistry([]byte(registry), "fixture")
			if err != nil {
				t.Fatal(err)
			}
			l, err := decodePinLock([]byte(lock), "fixture")
			if err != nil {
				t.Fatal(err)
			}
			if test.change != nil {
				test.change(r, l)
			}
			err = validateRegistryDeclarations(r)
			if err == nil {
				err = validateLockAgainstRegistry(r, l, true)
			}
			if pinErrorCode(err) != test.code {
				t.Fatalf("error %v; want %s", err, test.code)
			}
		})
	}
	cat, err := LoadContext(t.Context(), bytesProvider{RegistryFile: []byte(registry), LockFile: []byte(lock)})
	if err != nil {
		t.Fatal(err)
	}
	pin := cat.Components["tool"].Prebuilt
	if pin.ReleaseExecutable || pin.Executable == nil || pin.URL != "https://example.test/v1.2.3/tool.tar.gz" ||
		pin.Digests["amd64"] != pin.Executable.ExecutableDigest {
		t.Fatalf("wrong resolved acquisition %+v", pin)
	}
}
