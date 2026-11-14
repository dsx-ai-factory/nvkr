// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

//go:embed testdata/command-body.sh
var embeddedCommandBody string

func TestCommandFilesExactBytesAndPlatforms(t *testing.T) {
	for _, body := range []string{embeddedCommandBody, "", "no newline", "one\n", "two\n\n", "CRLF\r\n", "héllo\t\n"} {
		t.Run(body, func(t *testing.T) {
			provider, err := BindContext(t.Context(), bytesProvider{"scripts/body": []byte(body)})
			if err != nil {
				t.Fatal(err)
			}
			raw := []byte(`{"sources":{"s":{"vendorCommand":["sh","-ec",{"file":"scripts/body"}]}},"components":{"c":{"buildCommand":[{"file":"scripts/body"},"argument"],"buildCommandByPlatform":{"linux/arm64":["bash","-c",{"file":"scripts/body"}]}}}}`)
			expanded, err := expandCommandFiles(raw, RegistryFile, []DataProvider{provider})
			if err != nil {
				t.Fatal(err)
			}
			var reg registryDoc
			if err := json.Unmarshal(expanded, &reg); err != nil {
				t.Fatal(err)
			}
			if reg.Sources["s"].VendorCommand[2] != body || reg.Components["c"].BuildCommand[0] != body || reg.Components["c"].BuildCommandByPlatform["linux/arm64"][2] != body {
				t.Fatal("file bytes changed")
			}
		})
	}
}

func TestCommandFilesRejectUnsafeOperands(t *testing.T) {
	for _, operand := range []string{`null`, `false`, `123`, `[]`, `{"file":"../body"}`, `{"file":"a/../body"}`, `{"file":"/body"}`, `{"file":"a//body"}`, `{"file":"./body"}`, `{"file":""}`, `{"file":"C:/body"}`, `{"file":"a\\body"}`, `{"file":"a\u0000body"}`, `{"file":2}`, `{"file":null}`, `{"file":"body","extra":true}`, `{"shell":"body"}`, `{"file":"body","file":"other"}`} {
		t.Run(operand, func(t *testing.T) {
			provider, err := BindContext(t.Context(), bytesProvider{"body": []byte("body")})
			if err != nil {
				t.Fatal(err)
			}
			_, err = expandCommandFiles([]byte(`{"components":{"c":{"buildCommand":[`+operand+`]}}}`), RegistryFile, []DataProvider{provider})
			if catalogErrorCode(err) != e.CodeInvalidCatalog {
				t.Fatalf("code = %s: %v", catalogErrorCode(err), err)
			}
		})
	}
}

func TestCommandFilesProviderBounds(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     []byte
		canceled bool
		initial  int64
		code     e.Code
	}{
		{name: "invalid UTF8", body: []byte{0xff}, code: e.CodeInvalidCatalog},
		{name: "file bound", body: []byte(strings.Repeat("x", int(MaxCatalogFileBytes)+1)), code: e.CodeInvalidCatalog},
		{name: "aggregate bound", body: []byte("xx"), initial: MaxCatalogTotalBytes - 1, code: e.CodeInvalidCatalog},
		{name: "canceled", body: []byte("x"), canceled: true, code: e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			provider, err := BindContext(ctx, bytesProvider{"body": tc.body})
			if err != nil {
				t.Fatal(err)
			}
			provider.(*boundContextProvider).totalBytes = tc.initial
			if tc.canceled {
				cancel()
			}
			_, err = readCommandFile(RegistryFile, "body", []DataProvider{provider})
			if catalogErrorCode(err) != tc.code {
				t.Fatalf("code = %s: %v", catalogErrorCode(err), err)
			}
		})
	}
}

func TestCommandFilesNoFollow(t *testing.T) {
	for _, kind := range []string{"leaf symlink", "parent symlink", "directory", "missing"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			outside := t.TempDir()
			writeFragmentFixture(t, filepath.Join(outside, "body"), []byte("secret"))
			name := "body"
			var err error
			switch kind {
			case "leaf symlink":
				err = os.Symlink(filepath.Join(outside, "body"), filepath.Join(root, name))
			case "parent symlink":
				err = os.Symlink(outside, filepath.Join(root, "link"))
				name = "link/body"
			case "directory":
				err = os.Mkdir(filepath.Join(root, name), 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			provider, err := BindContext(t.Context(), Dir(root))
			if err != nil {
				t.Fatal(err)
			}
			_, err = readCommandFile(RegistryFile, name, []DataProvider{provider})
			if catalogErrorCode(err) != e.CodeInvalidCatalog {
				t.Fatalf("code = %s: %v", catalogErrorCode(err), err)
			}
		})
	}
}

func TestCommandFilesNeedContextProvider(t *testing.T) {
	for _, providers := range [][]DataProvider{nil, {bytesProvider{"body": []byte("body")}}} {
		_, err := readCommandFile(RegistryFile, "body", providers)
		if catalogErrorCode(err) != e.CodeEngineCapability {
			t.Fatalf("unbound provider admitted: %v", err)
		}
	}
}

func TestCommandFilesStrictDecode(t *testing.T) {
	for _, extra := range []string{"", "buildShell: body", "unknown: value", "buildCommandByPlatform: {linux/amd64: [body]}\n    unknown: value"} {
		raw := []byte("apiVersion: nvkr.dev/catalog/v3\nkind: SourceBuildRegistry\nsources:\n  source:\n    repo: https://example.test/source\n    toolchainImage: " + goodImage + "\ncomponents:\n  tool:\n    platforms: [linux/amd64]\n    fromSource: true\n    source: source\n    outputType: host-package\n    artifactPath: out/tool\n    writeDir: out\n    buildCommand: [sh, -ec, {file: body}]\n    " + extra + "\n")
		provider, err := BindContext(t.Context(), bytesProvider{"body": []byte("echo ok\n")})
		if err != nil {
			t.Fatal(err)
		}
		_, err = decodeRegistry(raw, "test", provider)
		if extra == "" {
			if err != nil {
				t.Fatalf("valid control rejected: %v", err)
			}
			continue
		}
		if catalogErrorCode(err) != e.CodeInvalidCatalog {
			t.Fatalf("unknown field admitted: %v", err)
		}
	}
}
