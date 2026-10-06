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

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/material"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// This opt-in regression executes the selected digest-pinned scanner on a small
// real Docker archive. It proves both scanner command paths accept the selected
// cataloger, not libguestfs execution or complete NKE image coverage.
func TestDiskContentPinnedScannerReal(t *testing.T) {
	path := os.Getenv("NKD_SBOM_SCANNER")
	if path == "" {
		t.Skip("requires a native digest-pinned Syft executable")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("held scanner integration requires native Linux")
	}
	scanner := Scanner{Path: path, Digest: os.Getenv("NKD_SBOM_SCANNER_DIGEST")}
	status := []byte("Package: nkd-scanner-fixture\nStatus: install ok installed\nArchitecture: all\nVersion: 2:1.0-1ubuntu4\nDescription: scanner regression\n\n")
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
		return tarball.Write(name.MustParseReference("nkd.invalid/scanner:regression"), image, destination)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	for _, mode := range []string{"generated", "held"} {
		t.Run(mode, func(t *testing.T) {
			var inventory DependencyInventory
			var err error
			if mode == "generated" {
				inventory, err = inspectGeneratedDependencies(t.Context(), transport, scanner)
			} else {
				subject, openErr := heldfile.FromReaderContext(t.Context(), io.NewSectionReader(transport, 0, transport.Size()), heldfile.Options{Label: "real-scanner-archive", MaxBytes: 1 << 20})
				if openErr != nil {
					t.Fatal(openErr)
				}
				defer subject.Close()
				inventory, err = inspectHeldDependencyReport(t.Context(), subject, "docker-image", scanner, true)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, component := range inventory.Components {
				key, err := diskComponentKey(component, "linux/"+runtime.GOARCH)
				if err != nil {
					t.Fatal(err)
				}
				if key == (diskPackageKey{name: "nkd-scanner-fixture", version: "2:1.0-1ubuntu4", format: "deb", architecture: "all"}) {
					t.Logf("scanner=%s subject=%s exact package=%s version=%s", scanner.Digest, transport.Digest(), component.Name, component.Version)
					return
				}
			}
			t.Fatalf("real scanner omitted exact fixture package: %+v", inventory.Components)
		})
	}
}

// Unlike the pure checker fixture, this regression builds real small Debian
// packages with the pinned reader and exercises per-member metadata propagation
// through the real archive scanner before comparing a source capture.
func TestDiskCapturedPackagesPinnedToolsReal(t *testing.T) {
	scanner := Scanner{Path: os.Getenv("NKD_SBOM_SCANNER"), Digest: os.Getenv("NKD_SBOM_SCANNER_DIGEST")}
	readerDigest := os.Getenv("NKD_SBOM_PACKAGE_READER_DIGEST")
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
	for _, name := range []string{"nkd-capture-a", "nkd-capture-b"} {
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
