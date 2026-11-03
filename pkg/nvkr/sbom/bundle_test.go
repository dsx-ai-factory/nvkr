// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func bundleInventoryFixture(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	for _, file := range []struct{ name, data string }{
		{"recipe.yaml", "accelerator: gb300\n"},
		{"argocd/application.yaml", "apiVersion: argoproj.io/v1alpha1\nkind: Application\n"},
	} {
		if err := tw.WriteHeader(&tar.Header{Name: file.name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(file.data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(file.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestBundleInventoryBindsObservedFilesWithoutDeploymentClaims(t *testing.T) {
	raw := bundleInventoryFixture(t)
	file := filepath.Join(t.TempDir(), "bundle.tar.gz")
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	inventory, err := InspectDependencies(t.Context(), file, digest, "bundle-archive", Scanner{})
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Method != BundleFilesMethod || inventory.ScannerDigest != "" || len(inventory.Components) != 2 {
		t.Fatalf("unexpected inventory: %+v", inventory)
	}
	for _, component := range inventory.Components {
		if component.Type != "file" || component.Version != "" || component.PURL != "" || len(component.Hashes) != 1 {
			t.Fatalf("file inventory claims a package or unbound content: %+v", component)
		}
	}
	first, err := WithDependencies(dependencySubject(t), inventory.Components, inventory.Method, "")
	if err != nil {
		t.Fatal(err)
	}
	firstRaw, err := first.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	inventory.Components[0], inventory.Components[1] = inventory.Components[1], inventory.Components[0]
	second, err := WithDependencies(dependencySubject(t), inventory.Components, inventory.Method, "")
	if err != nil {
		t.Fatal(err)
	}
	secondRaw, err := second.Marshal()
	if err != nil || !bytes.Equal(firstRaw, secondRaw) {
		t.Fatalf("inventory serialization is unstable: %v", err)
	}
	if err := first.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, kind, pin string
		canceled        bool
		code            e.Code
	}{
		{"wrong bytes", "bundle-archive", "sha256:" + strings.Repeat("0", 64), false, e.CodeVerifyFailed},
		{"wrong format", "unknown-bundle", digest, false, e.CodeEngineCapability},
		{"not a Helm chart", "tarball", digest, false, e.CodeProvenanceMissing},
		{"canceled", "bundle-archive", digest, true, e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			_, err := InspectDependencies(ctx, file, tc.pin, tc.kind, Scanner{})
			assertDependencyCode(t, err, tc.code)
		})
	}
}

func TestBundleInventoryRejectsBroaderOrMalformedClaims(t *testing.T) {
	inventory, err := inspectBundleArchive(t.Context(), bytes.NewReader(bundleInventoryFixture(t)))
	if err != nil {
		t.Fatal(err)
	}
	valid, err := WithDependencies(dependencySubject(t), inventory.Components, BundleFilesMethod, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*BOM)
	}{
		{"claims deployed package", func(b *BOM) { b.Components[0].Type = "application" }},
		{"claims package version", func(b *BOM) { b.Components[0].Version = "1.2.3" }},
		{"path traversal", func(b *BOM) { b.Components[0].Name = "../outside" }},
		{"absolute path", func(b *BOM) { b.Components[0].Name = "/outside" }},
		{"missing mode", func(b *BOM) { b.Components[0].Properties = b.Components[0].Properties[1:] }},
		{"privileged mode", func(b *BOM) { b.Components[0].Properties[0].Value = "4755" }},
		{"ambiguous size", func(b *BOM) { b.Components[0].Properties[1].Value = "01" }},
		{"unbounded size", func(b *BOM) { b.Components[0].Properties[1].Value = "999999999999" }},
		{"missing bytes hash", func(b *BOM) { b.Components[0].Hashes = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bom := cloneBOM(valid)
			tc.change(bom)
			// Exercise both the constructor and the serialized-description gate.
			_, err := WithDependencies(dependencySubject(t), bom.Components, BundleFilesMethod, "")
			code := e.CodeVerifyFailed
			if tc.name == "missing bytes hash" {
				code = e.CodeUnverifiedRef
			}
			assertDependencyCode(t, err, code)
			assertDependencyCode(t, bom.Validate(), code)
		})
	}
	_, err = WithDependencies(dependencySubject(t), inventory.Components, BundleFilesMethod, "sha256:"+strings.Repeat("c", 64))
	assertDependencyCode(t, err, e.CodeVerifyFailed)
}
