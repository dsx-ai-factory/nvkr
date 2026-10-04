package sbom

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

// This opt-in retained-disk exercise covers the public SBOM wrapper and its
// opaque inventory mapping, not the original factory Run or complete disk SBOM.
func TestDiskInspectionRuntimeInventoryReal(t *testing.T) {
	image := os.Getenv("NKD_TEST_DISK_INSPECT_IMAGE")
	if image == "" {
		t.Skip("requires exact local inspector image and retained QCOW2")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("runtime integration requires native Linux")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Minute)
	defer cancel()
	workspace, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	runtimeExecutable := os.Getenv("NKD_TEST_DISK_INSPECT_RUNTIME")
	if runtimeExecutable == "" {
		runtimeExecutable = "podman"
	}
	selection := pipeline.DiskInspectionSelection{RuntimeExecutable: runtimeExecutable,
		Image: image, Platform: "linux/" + runtime.GOARCH,
		InspectorDigest: os.Getenv("NKD_TEST_DISK_INSPECTOR_SHA256"), CodecDigest: os.Getenv("NKD_TEST_DISK_CODEC_SHA256"),
		Resources: execution.ResourceRequest{CPUMillis: 1000, MemoryMiB: 10 * 1024, EphemeralStorageMiB: 512}, TimeoutSeconds: 600, Workspace: workspace}
	capability, err := OpenDiskInspectionRuntime(ctx, selection)
	if err != nil {
		t.Fatal(err)
	}
	defer capability.Close()
	expectedImage, expectedInspector, expectedCodec, err := capability.BindingContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	path := os.Getenv("NKD_TEST_DISK_QCOW2")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := capability.AdmitSubject(ctx, info.Size())
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.Close()
	subject, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "inventory-runtime-test", MaxBytes: MaxDiagnosticDiskBytes,
		ExpectedDigest: os.Getenv("NKD_TEST_DISK_SHA256")})
	if err != nil {
		t.Fatal(err)
	}
	defer subject.Close()
	inventory, err := reservation.InspectHeldInstalledPackages(ctx, subject, "qcow2", "none")
	if err != nil {
		t.Fatal(err)
	}
	digest, size, inspector, codec := inventory.Binding()
	if digest != subject.Digest() || size != subject.Size() || inspector != expectedInspector || codec != expectedCodec || inventory.RuntimeBinding() != expectedImage {
		t.Fatalf("issued inventory has incorrect subject/image/tools: %s %d %s %s %s", digest, size, inspector, codec, inventory.RuntimeBinding())
	}
	packages := inventory.Packages()
	if len(packages) == 0 {
		t.Fatal("issued inventory has no installed packages")
	}
	first := packages[0]
	packages[0].Name = "changed caller description"
	if inventory.Packages()[0] != first {
		t.Fatal("caller description mutated issued inventory")
	}
	t.Logf("runtime-only SBOM inventory: selected=%s image=%s subject=%s bytes=%d packages=%d inspector=%s codec=%s",
		runtimeExecutable, expectedImage, digest, size, len(packages), inspector, codec)
}
