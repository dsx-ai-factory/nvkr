package sbom

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/internal/transportarchive"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
)

// Existing OCI writer and transport packer own all fixture grammar.
func nestedOCIFixture(t *testing.T, architectures []string, compressed bool, change func(string)) *generatedInspectionFile {
	t.Helper()
	root := t.TempDir()
	lp, err := layout.Write(root, empty.Index)
	if err != nil {
		t.Fatal(err)
	}
	for index, architecture := range architectures {
		configuration, err := empty.Image.ConfigFile()
		if err != nil {
			t.Fatal(err)
		}
		configuration.OS, configuration.Architecture = "linux", architecture
		configuration.Config.Labels = map[string]string{"fixture": strconv.Itoa(index)}
		image, err := mutate.ConfigFile(empty.Image, configuration)
		if err != nil {
			t.Fatal(err)
		}
		if err := lp.AppendImage(image); err != nil {
			t.Fatal(err)
		}
	}
	if change != nil {
		change(root)
	}
	carrier := filepath.Join(root, "layout.tar")
	if err := transportarchive.Pack(t.Context(), carrier, root, []string{"oci-layout", "index.json", "blobs"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(carrier)
	if err != nil {
		t.Fatal(err)
	}
	if compressed {
		return compressedMaterialBytes(t, raw)
	}
	file, err := createGeneratedInspectionFile(t.Context(), 1<<20, func(destination io.Writer) error { _, err := destination.Write(raw); return err })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func TestNestedOCIUsesCompleteClosureAndAllPlatforms(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		for _, compressed := range []bool{false, true} {
			for _, test := range []struct {
				name          string
				architectures []string
				change        func(string)
				code          e.Code
			}{
				{"two images reach scanner", []string{architecture, architecture}, nil, e.CodeUnverifiedRef},
				{"other platform not dropped", []string{architecture, "unsupported"}, nil, e.CodeVerifyFailed},
				{"unreferenced blob rejected", []string{architecture}, func(root string) {
					raw := []byte("unreferenced")
					if err := os.WriteFile(filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(rawDigest(raw), "sha256:")), raw, 0o644); err != nil {
						t.Fatal(err)
					}
				}, e.CodeVerifyFailed},
			} {
				t.Run(architecture+"/"+strconv.FormatBool(compressed)+"/"+test.name, func(t *testing.T) {
					inspection, err := inspectCompressedImages(t.Context(), nestedOCIFixture(t, test.architectures, compressed, test.change), "linux/"+architecture, Scanner{Path: "/missing/selected-syft", Digest: "sha256:" + strings.Repeat("f", 64)}, 1<<20, 0)
					assertDependencyCode(t, err, test.code)
					if inspection != nil {
						t.Fatal("rejected OCI returned partial inventory")
					}
				})
			}
		}
	}
}

func TestNestedMaterialCumulativeAdmission(t *testing.T) {
	for _, test := range []struct {
		name            string
		remaining       int64
		members, images int
		code            e.Code
	}{
		{"shared expansion exhaustion", 1024, 0, 0, e.CodeEngineCapability},
		{"shared image count", 1 << 20, 0, 256, e.CodeEngineCapability},
		{"shared member count", 1 << 20, maxDiskInstalledPackages, 0, e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			inspection, err := inspectNestedImagesBudget(t.Context(), nestedOCIFixture(t, []string{"amd64"}, true, nil), "linux/amd64", Scanner{}, 1<<20, 0, &materialInspectionBudget{remaining: test.remaining, members: test.members, images: test.images})
			assertDependencyCode(t, err, test.code)
			if inspection != nil {
				t.Fatal("budget failure returned inventory")
			}
		})
	}
	subject := compressedMaterialFixture(t, map[string]string{"docs": "ordinary"})
	budget := &materialInspectionBudget{remaining: 2048}
	if inspection, err := inspectNestedImagesBudget(t.Context(), subject, "linux/amd64", Scanner{}, 1<<20, 0, budget); err != nil || inspection != nil {
		t.Fatalf("first ordinary expansion: %v", err)
	}
	inspection, err := inspectNestedImagesBudget(t.Context(), subject, "linux/amd64", Scanner{}, 1<<20, 0, budget)
	assertDependencyCode(t, err, e.CodeEngineCapability)
	if inspection != nil {
		t.Fatal("second sibling exceeded aggregate budget")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	inspection, err = inspectCompressedImages(ctx, subject, "linux/amd64", Scanner{}, 1<<20, 0)
	assertDependencyCode(t, err, e.CodeTimeout)
	if inspection != nil {
		t.Fatal("canceled operation returned inventory")
	}
}

// Actual held child execution is Linux-only. This proves all projections are
// scanned, not real Syft discovery or a native full-disk join.
func TestNestedOCIInspectsEveryImage(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("held scanner execution requires Linux")
	}
	for _, architecture := range []string{"amd64", "arm64"} {
		t.Run(architecture, func(t *testing.T) {
			counter := filepath.Join(t.TempDir(), "scans")
			script := []byte("#!/bin/sh\nprintf x >> '" + counter + "'\nprintf '%s' '{\"schema\":{\"version\":\"16.1.10\"},\"artifacts\":[{\"name\":\"observed\",\"version\":\"1\",\"type\":\"deb\",\"metadata\":{\"architecture\":\"all\"}}]}'\n")
			scannerPath := filepath.Join(t.TempDir(), "scanner")
			if err := os.WriteFile(scannerPath, script, 0o755); err != nil {
				t.Fatal(err)
			}
			subject := nestedOCIFixture(t, []string{architecture, architecture}, true, nil)
			inspection, err := inspectCompressedImages(t.Context(), subject, "linux/"+architecture, Scanner{Path: scannerPath, Digest: rawDigest(script)}, 1<<20, 0)
			if err != nil || inspection == nil {
				t.Fatalf("scan failed: %v", err)
			}
			calls, err := os.ReadFile(counter)
			if err != nil || !bytes.Equal(calls, []byte("xx")) {
				t.Fatalf("not every image scanned: %q %v", calls, err)
			}
			if len(inspection.Components()) != 2 || inspection.subject != subject.Digest() {
				t.Fatal("components or original carrier binding lost")
			}
			for _, component := range inspection.Components() {
				class, err := diskComponentClass(component)
				if err != nil || class != diskClassContainer {
					t.Fatalf("class=%q error=%v", class, err)
				}
			}
		})
	}
}
