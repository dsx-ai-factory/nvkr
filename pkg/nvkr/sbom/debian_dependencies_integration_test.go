// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

// TestRealDebianArtifactDependencies exercises Debian control-metadata discovery
// on controlled fixtures through the explicitly named diagnostic API.
// It does not prove embedded application dependency coverage, installation,
// signing or repository publication.
func TestRealDebianArtifactDependencies(t *testing.T) {
	optIn := os.Getenv("NVKR_SBOM_DEBIAN_INTEGRATION")
	if optIn == "" {
		t.Skip("set NVKR_SBOM_DEBIAN_INTEGRATION=1 to exercise pinned Linux Syft and dpkg-deb")
	}
	if optIn != "1" {
		t.Fatal("NVKR_SBOM_DEBIAN_INTEGRATION must be 1")
	}
	scanner := Scanner{Path: os.Getenv("NVKR_SBOM_SCANNER"), Digest: os.Getenv("NVKR_SBOM_SCANNER_DIGEST")}
	readerDigest := os.Getenv("NVKR_SBOM_PACKAGE_READER_DIGEST")
	if scanner.Path == "" || !canonical.IsDigest(scanner.Digest) || !canonical.IsDigest(readerDigest) {
		t.Fatal("opted-in Debian regression requires NVKR_SBOM_SCANNER, NVKR_SBOM_SCANNER_DIGEST and NVKR_SBOM_PACKAGE_READER_DIGEST")
	}
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		t.Fatal("held Debian scanner integration requires native Linux amd64 or arm64")
	}
	reader, err := heldfile.OpenContext(t.Context(), "/usr/bin/dpkg-deb", heldfile.Options{
		Label: "Debian-artifact-test-package-reader", Executable: true, MaxBytes: 512 << 20, ExpectedDigest: readerDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	const packageName, version = "nvkr-debian-artifact-fixture", "1.0.0"
	for _, compression := range []string{"gzip", "xz"} {
		t.Run(compression, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "DEBIAN"), 0o755); err != nil {
				t.Fatal(err)
			}
			control := "Package: " + packageName + "\nVersion: " + version + "\nArchitecture: " + runtime.GOARCH + "\nMaintainer: Fixture <fixture@example.invalid>\nDescription: final Debian archive regression\n"
			if err := os.WriteFile(filepath.Join(root, "DEBIAN", "control"), []byte(control), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "payload"), []byte("controlled fixture payload\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			filename := filepath.Join(t.TempDir(), "package.deb")
			var output, diagnostic boundedScanBuffer
			if err := heldfile.RunContext(ctx, reader, []string{"--root-owner-group", "--uniform-compression", "-Z" + compression, "--build", root, filename}, heldfile.RunOptions{
				Stdout: &output, Stderr: &diagnostic, TerminationGrace: time.Second,
				Env: []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "TZ=UTC", "SOURCE_DATE_EPOCH=1733939955"},
			}); err != nil {
				t.Fatalf("building real fixture package: %v, %s, %s", err, output.String(), diagnostic.String())
			}
			subject, err := heldfile.OpenContext(ctx, filename, heldfile.Options{Label: "fixture-Debian-test-subject", MaxBytes: 1 << 20})
			if err != nil {
				t.Fatal(err)
			}
			defer subject.Close()
			inventory, err := InspectDebianArchiveMetadata(ctx, filename, subject.Digest(), scanner)
			if err != nil {
				t.Fatal(err)
			}
			// Exact component count and PURL form record the Syft v1.51.1
			// fixture contract; revalidate them when changing its pin.
			if inventory.Method != debianArchiveMetadataMethod || inventory.ScannerDigest != scanner.Digest || len(inventory.Components) != 1 {
				t.Fatalf("unexpected Debian archive inventory: %+v", inventory)
			}
			component := inventory.Components[0]
			wantPURL := "pkg:deb/" + packageName + "@" + version + "?arch=" + runtime.GOARCH
			if component.Type != "library" || component.Name != packageName || component.Version != version || component.PURL != wantPURL {
				t.Fatalf("Debian archive metadata differs from fixture: %+v", component)
			}
			// Diagnostic package identity is not payload dependency evidence.
			_, err = WithDependencies(dependencySubject(t), inventory.Components, inventory.Method, inventory.ScannerDigest)
			assertDependencyCode(t, err, e.CodeUnsupported)
			// The same fixture bytes without the hint must not yield an
			// accepted inventory under this selected Syft version.
			unhinted := filepath.Join(t.TempDir(), "package")
			unhintedFile, err := os.OpenFile(unhinted, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			_, copyErr := subject.WriteToContext(ctx, unhintedFile)
			closeErr := unhintedFile.Close()
			if copyErr != nil || closeErr != nil {
				t.Fatalf("copying unhinted fixture: %v, %v", copyErr, closeErr)
			}
			_, err = InspectDependencies(ctx, unhinted, subject.Digest(), "host-package", scanner)
			assertDependencyCode(t, err, e.CodeProvenanceMissing)
			// Ordinary inspection remains filename-neutral, even for .deb.
			_, err = InspectDependencies(ctx, filename, subject.Digest(), "host-package", scanner)
			assertDependencyCode(t, err, e.CodeProvenanceMissing)
			// Explicit diagnostics select their archive name independently of
			// the caller's path; the same unhinted bytes retain package identity.
			unhintedInventory, err := InspectDebianArchiveMetadata(ctx, unhinted, subject.Digest(), scanner)
			if err != nil || unhintedInventory.Method != inventory.Method || len(unhintedInventory.Components) != 1 || unhintedInventory.Components[0].PURL != wantPURL {
				t.Fatalf("unhinted diagnostic inventory: %+v, %v", unhintedInventory, err)
			}
			// These are pre-scan binding and original-fixture stability checks,
			// not a separate observation of Syft's private snapshot.
			_, err = InspectDebianArchiveMetadata(ctx, filename, "sha256:"+strings.Repeat("0", 64), scanner)
			assertDependencyCode(t, err, e.CodeVerifyFailed)
			checked, err := heldfile.OpenContext(ctx, filename, heldfile.Options{
				Label: "fixture-Debian-test-readback", MaxBytes: 1 << 20, ExpectedDigest: subject.Digest(),
			})
			if err != nil {
				t.Fatal(err)
			}
			defer checked.Close()
			if checked.Size() != subject.Size() {
				t.Fatal("Debian fixture size changed")
			}
			t.Logf("scanner=%s dpkg-deb=%s fixture-deb=%s bytes=%d package=%s method=%s ordinary-inspection=%s", scanner.Digest, readerDigest, subject.Digest(), subject.Size(), wantPURL, inventory.Method, e.CodeProvenanceMissing)
		})
	}
	t.Run("non-archive bytes", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		filename := filepath.Join(t.TempDir(), "invalid.deb")
		if err := os.WriteFile(filename, []byte("not a Debian archive\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		subject, err := heldfile.OpenContext(ctx, filename, heldfile.Options{Label: "invalid-Debian-test-subject", MaxBytes: 1024})
		if err != nil {
			t.Fatal(err)
		}
		defer subject.Close()
		inventory, err := InspectDebianArchiveMetadata(ctx, filename, subject.Digest(), scanner)
		assertDependencyCode(t, err, e.CodeProvenanceMissing)
		if len(inventory.Components) != 0 {
			t.Fatal("filename hint accepted invalid package bytes")
		}
	})
}
