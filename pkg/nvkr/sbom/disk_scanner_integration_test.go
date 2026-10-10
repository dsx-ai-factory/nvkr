// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/material"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// This opt-in regression executes the selected digest-pinned scanner on a small
// real Docker archive and the plain/gzip nested-image owner. Once opted in with
// NVKR_SBOM_SCANNER, all five native tool pins are required for the OCI helper
// platform/report-binding rows. It
// does not prove libguestfs execution or complete NVKE image coverage.
func TestDiskContentPinnedScannerReal(t *testing.T) {
	path := os.Getenv("NVKR_SBOM_SCANNER")
	if path == "" {
		t.Skip("requires a native digest-pinned Syft executable")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("held scanner integration requires native Linux")
	}
	scanner := Scanner{Path: path, Digest: os.Getenv("NVKR_SBOM_SCANNER_DIGEST")}
	status := []byte("Package: nvkr-scanner-fixture\nStatus: install ok installed\nArchitecture: all\nVersion: 2:1.0-1ubuntu4\nDescription: scanner regression\n\n")
	var filesystem bytes.Buffer
	writer := tar.NewWriter(&filesystem)
	if err := writer.WriteHeader(&tar.Header{Name: "var/lib/dpkg/status", Mode: 0o644, Size: int64(len(status))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(status); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(filesystem.Bytes())), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	image, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := image.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	configuration.OS, configuration.Architecture = "linux", runtime.GOARCH
	image, err = mutate.ConfigFile(image, configuration)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := createGeneratedInspectionFile(t.Context(), 1<<20, func(destination io.Writer) error {
		return tarball.Write(name.MustParseReference("nvkr.invalid/scanner:regression"), image, destination)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	t.Run("generated", func(t *testing.T) {
		var inventory DependencyInventory
		var err error
		inventory, err = inspectGeneratedDependencies(t.Context(), transport, scanner)
		if err != nil {
			t.Fatal(err)
		}
		for _, component := range inventory.Components {
			key, err := diskComponentKey(component, "linux/"+runtime.GOARCH)
			if err != nil {
				t.Fatal(err)
			}
			if key == (diskPackageKey{name: "nvkr-scanner-fixture", version: "2:1.0-1ubuntu4", format: "deb", architecture: "all"}) {
				t.Logf("scanner=%s subject=%s exact package=%s version=%s", scanner.Digest, transport.Digest(), component.Name, component.Version)
				return
			}
		}
		t.Fatalf("real scanner omitted exact fixture package: %+v", inventory.Components)
	})
	for _, test := range []struct {
		name       string
		compressed bool
	}{{"plain", false}, {"gzip", true}} {
		t.Run("nested/"+test.name, func(t *testing.T) {
			subject := transport
			if test.compressed {
				raw, err := io.ReadAll(io.NewSectionReader(transport, 0, transport.Size()))
				if err != nil {
					t.Fatal(err)
				}
				subject = compressedMaterialBytes(t, raw)
			}
			inspection, err := inspectCompressedImages(t.Context(), subject, "linux/"+runtime.GOARCH, scanner, 1<<20, 0)
			if err != nil {
				t.Fatal(err)
			}
			if inspection == nil || inspection.subject != subject.Digest() || inspection.size != subject.Size() || inspection.scanner != scanner.Digest || len(inspection.files) != 0 {
				t.Fatalf("nested scan lost its exact carrier/tool binding or invented root files: %+v", inspection)
			}
			for _, component := range inspection.Components() {
				key, err := diskComponentKey(component, "linux/"+runtime.GOARCH)
				if err != nil {
					t.Fatal(err)
				}
				if key == (diskPackageKey{name: "nvkr-scanner-fixture", version: "2:1.0-1ubuntu4", format: "deb", architecture: "all"}) {
					class, err := diskComponentClass(component)
					if err != nil || class != diskClassContainer {
						t.Fatalf("nested container package changed observation class: %q, %v", class, err)
					}
					t.Logf("scanner=%s carrier=%s bytes=%d exact nested package=%s version=%s", scanner.Digest, subject.Digest(), subject.Size(), component.Name, component.Version)
					return
				}
			}
			t.Fatal("real nested image scanner omitted exact fixture package")
		})
	}
	// Unlike nested archive discovery, the top-level material operation must not
	// trust Syft's selected image as proof of complete native OCI membership.
	for _, architecture := range []string{runtime.GOARCH, map[string]string{"amd64": "arm64", "arm64": "amd64"}[runtime.GOARCH]} {
		t.Run("oci-helper/"+architecture, func(t *testing.T) {
			for _, variable := range []string{"NVKR_SBOM_INSPECTOR_DIGEST", "NVKR_SBOM_CODEC_DIGEST", "NVKR_SBOM_GUESTFISH_DIGEST", "NVKR_SBOM_PACKAGE_READER_DIGEST"} {
				if os.Getenv(variable) == "" {
					t.Fatalf("opted-in OCI helper regression requires selected native tool pin %s", variable)
				}
			}
			config := configuration.DeepCopy()
			config.Architecture = architecture
			selected, err := mutate.ConfigFile(image, config)
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			lp, err := layout.Write(root, empty.Index)
			if err != nil {
				t.Fatal(err)
			}
			if err := lp.AppendImage(selected); err != nil {
				t.Fatal(err)
			}
			carrier := filepath.Join(root, "image.tar")
			if err := transportarchive.Pack(t.Context(), carrier, root, []string{"oci-layout", "index.json", "blobs"}); err != nil {
				t.Fatal(err)
			}
			subject, err := heldfile.OpenContext(t.Context(), carrier, heldfile.Options{Label: "real-native-oci-material", MaxBytes: 1 << 20})
			if err != nil {
				t.Fatal(err)
			}
			defer subject.Close()
			var report bytes.Buffer
			err = RunDiskContentHelper(t.Context(), os.Getenv("NVKR_SBOM_INSPECTOR_DIGEST"), os.Getenv("NVKR_SBOM_CODEC_DIGEST"), subject.Digest(), subject.Size(),
				"oci-image", os.Getenv("NVKR_SBOM_GUESTFISH_DIGEST"), scanner.Digest, os.Getenv("NVKR_SBOM_PACKAGE_READER_DIGEST"), 1<<20, "linux/"+runtime.GOARCH,
				io.NewSectionReader(subject, 0, subject.Size()), &report)
			if architecture != runtime.GOARCH {
				assertDependencyCode(t, err, e.CodeVerifyFailed)
				if report.Len() != 0 || !strings.Contains(err.Error(), "nested OCI image platform differs from consumer") {
					t.Fatalf("foreign OCI configuration reached the wrong boundary or returned a partial report: %v", err)
				}
				return
			}
			if err != nil || report.Len() == 0 {
				t.Fatalf("native OCI helper failed: %v", err)
			}
			var observed diskContentReport
			if err := canonical.UnmarshalExact(report.Bytes(), &observed); err != nil {
				t.Fatal(err)
			}
			if observed.Subject != subject.Digest() || observed.Size != subject.Size() || observed.Platform != "linux/"+runtime.GOARCH || observed.Kind != "oci-image" || observed.Scanner != scanner.Digest ||
				observed.Inspector != os.Getenv("NVKR_SBOM_INSPECTOR_DIGEST") || observed.Codec != os.Getenv("NVKR_SBOM_CODEC_DIGEST") || observed.Guestfish != os.Getenv("NVKR_SBOM_GUESTFISH_DIGEST") ||
				observed.PackageReader != os.Getenv("NVKR_SBOM_PACKAGE_READER_DIGEST") || observed.MaxFilesystemBytes != 1<<20 {
				t.Fatal("OCI helper lost exact carrier, platform, operation or scanner binding")
			}
			for _, component := range observed.Components {
				key, err := diskComponentKey(component, observed.Platform)
				if err != nil {
					t.Fatal(err)
				}
				if key == (diskPackageKey{name: "nvkr-scanner-fixture", version: "2:1.0-1ubuntu4", format: "deb", architecture: "all"}) {
					class, err := diskComponentClass(component)
					if err != nil || class != diskClassContainer {
						t.Fatalf("OCI helper package class=%q error=%v", class, err)
					}
					return
				}
			}
			t.Fatal("OCI helper omitted exact fixture package")
		})
	}
}

// Unlike the pure checker fixture, this regression builds real small Debian
// packages with the pinned reader and exercises per-member metadata propagation
// through the real archive scanner before comparing a source capture.
func TestDiskCapturedPackagesPinnedToolsReal(t *testing.T) {
	scanner := Scanner{Path: os.Getenv("NVKR_SBOM_SCANNER"), Digest: os.Getenv("NVKR_SBOM_SCANNER_DIGEST")}
	readerDigest := os.Getenv("NVKR_SBOM_PACKAGE_READER_DIGEST")
	if scanner.Path == "" || readerDigest == "" {
		t.Skip("requires native pinned Syft and dpkg-deb tools")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("held package reader requires native Linux")
	}
	reader, err := heldfile.OpenContext(t.Context(), "/usr/bin/dpkg-deb", heldfile.Options{Label: "capture-test-package-reader", Executable: true, MaxBytes: 512 << 20, ExpectedDigest: readerDigest})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	manifest := material.AcquisitionManifest{SchemaVersion: material.AcquisitionSchemaVersion, Architecture: runtime.GOARCH, Scope: "apt-pre-install-pkgs"}
	payloads := make(map[string][]byte)
	for _, name := range []string{"nvkr-capture-a", "nvkr-capture-b"} {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "DEBIAN"), 0o755); err != nil {
			t.Fatal(err)
		}
		control := "Package: " + name + "\nVersion: 2:1.0-1ubuntu4\nArchitecture: all\nMaintainer: Fixture <fixture@example.invalid>\nDescription: real capture regression\n"
		if err := os.WriteFile(filepath.Join(root, "DEBIAN/control"), []byte(control), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "payload"), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		filename := filepath.Join(t.TempDir(), "package.deb")
		var output, diagnostic boundedScanBuffer
		if err := heldfile.RunContext(t.Context(), reader, []string{"--build", root, filename}, heldfile.RunOptions{Stdout: &output, Stderr: &diagnostic, Env: []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "TZ=UTC"}}); err != nil {
			t.Fatalf("building actual fixture package: %v, %s, %s", err, output.String(), diagnostic.String())
		}
		payload, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		digest := rawDigest(payload)
		path := "packages/" + strings.TrimPrefix(digest, "sha256:") + ".deb"
		payloads[path] = payload
		manifest.Packages = append(manifest.Packages, material.AcquiredPackage{Name: name, Version: "2:1.0-1ubuntu4", Architecture: "all", Digest: digest, Size: int64(len(payload)), Path: path,
			RepositoryURIs: []string{"https://example.invalid/" + name + ".deb"}})
	}
	slices.SortFunc(manifest.Packages, func(a, b material.AcquiredPackage) int { return strings.Compare(a.Digest, b.Digest) })
	for _, scenario := range []string{"valid", "sibling metadata"} {
		t.Run(scenario, func(t *testing.T) {
			selected := manifest
			selected.Packages = slices.Clone(manifest.Packages)
			if scenario == "sibling metadata" {
				selected.Packages[1].Name = selected.Packages[0].Name
			}
			raw := capturedPackageTestArchive(t, selected, payloads)
			subject, err := heldfile.FromBytesContext(t.Context(), raw, heldfile.Options{Label: "actual-capture-packages", MaxBytes: int64(len(raw))})
			if err != nil {
				t.Fatal(err)
			}
			defer subject.Close()
			capture, err := material.ObserveAcquisitionArchive(t.Context(), subject)
			if err != nil {
				t.Fatal(err)
			}
			inspection, err := inspectMaterialArchive(t.Context(), subject, "linux/"+runtime.GOARCH, scanner, readerDigest, 4<<20)
			if err != nil {
				t.Fatal(err)
			}
			err = checkCapturedPackageMetadata(capture, inspection.packageMetadataByDigest, "linux/"+runtime.GOARCH)
			if scenario == "valid" {
				if err != nil || len(inspection.packageMetadataByDigest) != 2 {
					t.Fatalf("actual package metadata lost: %+v, %v", inspection.packageMetadataByDigest, err)
				}
				t.Logf("scanner=%s reader=%s archive=%s exact package subjects=%d", scanner.Digest, readerDigest, subject.Digest(), len(inspection.packageMetadataByDigest))
			} else {
				assertDependencyCode(t, err, e.CodeVerifyFailed)
			}
		})
	}
}
