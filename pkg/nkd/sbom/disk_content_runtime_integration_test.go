package sbom

import (
	"bytes"
	"context"
	"os"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

// This opt-in exercises real full-content helper operations and descriptive
// reconciliation. Independently held retained bytes cannot restore a factory
// Run, its PackerArtifact, a publication receipt or complete acquisition origin.
func TestDiskContentRuntimeReconciliationReal(t *testing.T) {
	manifestPath := os.Getenv("NKD_TEST_DISK_CONTENT_MANIFEST")
	if manifestPath == "" {
		t.Skip("requires an explicit complete retained-content matrix and admitted native runtime; ordinary CI is not real-disk proof")
	}
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		t.Fatal("full-content integration requires native Linux AMD64 or ARM64")
	}
	manifest, err := heldfile.OpenContext(t.Context(), manifestPath, heldfile.Options{Label: "content-test-selection", MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer manifest.Close()
	raw, err := manifest.ReadAllContext(t.Context(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var selected struct {
		Runtime     string                           `json:"runtime"`
		Platform    string                           `json:"platform"`
		Profile     definition.DiskInspectionProfile `json:"profile"`
		BuildDigest string                           `json:"buildDigest"`
		Output      string                           `json:"output"`
		Inputs      []struct {
			Kind   string `json:"kind"`
			Path   string `json:"path"`
			Digest string `json:"digest"`
		} `json:"inputs"`
	}
	if err := canonical.UnmarshalExact(raw, &selected); err != nil {
		t.Fatal(err)
	}
	if selected.Platform != "linux/"+runtime.GOARCH || !canonical.IsDigest(selected.BuildDigest) || selected.Output == "" || len(selected.Inputs) < 4 || len(selected.Inputs) > 16384 {
		t.Fatal("selection requires exact native platform, build/output descriptions and the complete bounded input matrix")
	}
	if err := selected.Profile.Validate(selected.Platform); err != nil {
		t.Fatal(err)
	}
	if selected.Profile.ScratchStorage != "private-volume" {
		t.Fatal("full-content validation requires the production private-volume scratch route")
	}
	classes := make(map[string]int)
	for _, input := range selected.Inputs {
		switch input.Kind {
		case "disk-image", "installer-iso", "apt-acquisition", "tarball", "host-package", "oci-image", "installer-content":
		default:
			t.Fatalf("unsupported input class %q", input.Kind)
		}
		if !canonical.IsDigest(input.Digest) || input.Path == "" {
			t.Fatal("every retained input requires its exact byte pin and explicit path")
		}
		classes[input.Kind]++
	}
	if classes["disk-image"] != 1 || classes["installer-iso"] != 1 || classes["apt-acquisition"] != 1 ||
		classes["tarball"]+classes["host-package"]+classes["oci-image"]+classes["installer-content"] == 0 {
		t.Fatal("matrix must contain one root, one base installer, one complete capture and every staged input")
	}
	// Keep every original held subject live across both scans and reconciliation.
	// Missing or substituted bytes fail before launching any content operation.
	held := make([]*heldfile.File, len(selected.Inputs))
	for i, input := range selected.Inputs {
		held[i], err = heldfile.OpenContext(t.Context(), input.Path, heldfile.Options{Label: "content-test-input", MaxBytes: MaxDiagnosticDiskBytes, ExpectedDigest: input.Digest})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = held[i].Close() })
	}
	workspace, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	profile := selected.Profile
	inspection, err := OpenDiskInspectionRuntime(t.Context(), pipeline.DiskInspectionSelection{RuntimeExecutable: selected.Runtime,
		Workspace: workspace, Image: profile.Image, Platform: selected.Platform,
		InspectorDigest: profile.InspectorDigest, CodecDigest: profile.CodecDigest, GuestfishDigest: profile.GuestfishDigest,
		ScannerDigest: profile.ScannerDigest, PackageReaderDigest: profile.PackageReaderDigest,
		Resources: profile.Resources, TimeoutSeconds: profile.TimeoutSeconds, MaxFilesystemBytes: profile.MaxFilesystemBytes, ScratchStorage: profile.ScratchStorage})
	if err != nil {
		t.Fatal(err)
	}
	defer inspection.Close()
	var first []byte
	var root *DiskContentInspection
	var inputs []*DiskContentInspection
	for pass := range 2 {
		root, inputs = nil, nil
		for i, input := range selected.Inputs {
			started := time.Now()
			observed, err := inspection.InspectHeldContent(t.Context(), held[i], input.Kind, selected.Platform)
			if err != nil {
				t.Fatalf("pass=%d kind=%s subject=%s: %v", pass, input.Kind, input.Digest, err)
			}
			subject, size, kind, platform, _, scanner := observed.Binding()
			if subject != held[i].Digest() || size != held[i].Size() || kind != input.Kind || platform != selected.Platform || scanner != profile.ScannerDigest {
				t.Fatal("content result lost its exact held subject or selected operation/tool binding")
			}
			t.Logf("pass=%d platform=%s kind=%s subject=%s bytes=%d components=%d files=%d elapsed=%s", pass, platform, kind, subject, size, len(observed.Components()), len(observed.Files()), time.Since(started))
			if kind == "disk-image" {
				root = observed
			} else {
				inputs = append(inputs, observed)
			}
		}
		if pass == 1 {
			slices.Reverse(inputs)
		}
		bom, err := ReconcileDiskContents(t.Context(), selected.BuildDigest, selected.Output, root, inputs)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := bom.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		subject, _, _, _, _, _ := root.Binding()
		if len(bom.Metadata.Component.Hashes) != 1 || "sha256:"+bom.Metadata.Component.Hashes[0].Content != subject {
			t.Fatal("reconciled CycloneDX is not bound to the inspected QCOW2")
		}
		if pass == 0 {
			first = raw
		} else if !bytes.Equal(first, raw) {
			t.Fatal("repeated exact-content inspection/input permutation changed canonical CycloneDX")
		}
		t.Logf("pass=%d canonical CycloneDX bytes=%d components=%d (retained-content description, not original-Run publication authority)", pass, len(raw), len(bom.Components))
	}
	for _, kind := range []string{"installer-iso", "apt-acquisition"} {
		t.Run("missing/"+kind, func(t *testing.T) {
			reduced := slices.DeleteFunc(slices.Clone(inputs), func(input *DiskContentInspection) bool { return input.report.Kind == kind })
			bom, err := ReconcileDiskContents(t.Context(), selected.BuildDigest, selected.Output, root, reduced)
			assertDependencyCode(t, err, e.CodeProvenanceMissing)
			if bom != nil {
				t.Fatal("missing mandatory material returned partial CycloneDX")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	observed, err := inspection.InspectHeldContent(ctx, held[0], selected.Inputs[0].Kind, selected.Platform)
	assertDependencyCode(t, err, e.CodeTimeout)
	if observed != nil {
		t.Fatal("canceled operation returned content evidence")
	}
	if err := inspection.Close(); err != nil {
		t.Fatal(err)
	}
	observed, err = inspection.InspectHeldContent(t.Context(), held[0], selected.Inputs[0].Kind, selected.Platform)
	assertDependencyCode(t, err, e.CodeEngineCapability)
	if observed != nil {
		t.Fatal("closed runtime returned content evidence")
	}
	for _, subject := range held {
		if err := subject.AssertUnchangedContext(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}
