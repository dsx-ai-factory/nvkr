// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/assemble"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestAPTProjectionIsOrderIndependentAndReopensExactPackages(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "tool")
	if err := os.WriteFile(binary, []byte("executable bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	var inputs []DebianPackageInput
	for _, arch := range []string{"arm64", "amd64"} {
		path := filepath.Join(root, arch+".deb")
		if _, err := assemble.Deb(binary, "tool", "1.0.0", 1733939955, arch, path); err != nil {
			t.Fatal(err)
		} //nolint:staticcheck // Diagnostic projection fixture, not qualified production evidence.
		inputs = append(inputs, DebianPackageInput{Path: path})
	}
	request := APTRequest{Distribution: "stable", Component: "main", Epoch: 1733939955, Packages: inputs}
	first, err := BuildAPTRepository(t.Context(), filepath.Join(root, "first"), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Packages = []DebianPackageInput{inputs[1], inputs[0]}
	second, err := BuildAPTRepository(t.Context(), filepath.Join(root, "second"), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != second.Digest {
		t.Fatal("input order changed repository identity")
	}
	observed, err := InspectAPTRepository(t.Context(), first.Path)
	if err != nil || observed.Digest != first.Digest {
		t.Fatalf("observation = %+v, %v", observed, err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		for _, name := range []string{"Packages", "Packages.gz"} {
			rel := filepath.Join("dists", "stable", "main", "binary-"+arch, name)
			one, err := os.ReadFile(filepath.Join(first.Path, rel))
			if err != nil {
				t.Fatal(err)
			}
			two, err := os.ReadFile(filepath.Join(second.Path, rel))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(one, two) {
				t.Fatalf("metadata %s changed with order", rel)
			}
			byHash, err := os.ReadFile(filepath.Join(first.Path, filepath.Dir(rel), "by-hash", "SHA256", strings.TrimPrefix(digestBytes(one), "sha256:")))
			if err != nil || !bytes.Equal(one, byHash) {
				t.Fatalf("Acquire-By-Hash metadata mismatch: %v", err)
			}
		}
	}
	packagePath := filepath.Join(first.Path, first.Repository.Packages[0].Filename)
	if err := os.WriteFile(packagePath, []byte("substituted"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = InspectAPTRepository(t.Context(), first.Path)
	assertRegistryCode(t, err, e.CodeVerifyFailed)
	request.Packages = []DebianPackageInput{inputs[0], inputs[0]}
	out := filepath.Join(root, "duplicate")
	_, err = BuildAPTRepository(t.Context(), out, request)
	assertRegistryCode(t, err, e.CodeVerifyFailed)
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("duplicate package produced output: %v", err)
	}
}

func TestAPTControlParsingAndProjectionPreserveMultilineDescription(t *testing.T) {
	control, err := parseControl([]byte("Package: tool\nVersion: 1.0\nArchitecture: amd64\nDescription: summary\n continuation\n\tsecond line\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	if control["Description"] != "summary\ncontinuation\nsecond line" || firstControlLine(control["Description"]) != "summary" {
		t.Fatalf("control = %v", control)
	}
	var paragraph bytes.Buffer
	writePackageParagraph(&paragraph, control, DebianPackage{Filename: "pool/tool.deb", Size: 7, SHA256: digestBytes([]byte("fixture"))})
	if !strings.Contains(paragraph.String(), "Description: summary\n continuation\n second line\n") {
		t.Fatalf("description lost continuation: %s", paragraph.String())
	}
	for _, raw := range []string{" orphan continuation\n", "Missing separator\n", ": empty field name\n"} {
		_, err := parseControl([]byte(raw))
		assertRegistryCode(t, err, e.CodeVerifyFailed)
	}
}
