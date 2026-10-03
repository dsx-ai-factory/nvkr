package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

// This opt-in test exercises the concrete complete-closure adapter. Opening a
// retained disk is runtime evidence only, not factory live-Run/trace acceptance.
func TestDiskInspectionRuntimeReal(t *testing.T) {
	image := os.Getenv("NKD_TEST_DISK_INSPECT_IMAGE")
	if image == "" {
		t.Skip("requires exact local inspector image and retained QCOW2")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("runtime integration requires native Linux")
	}
	workspace, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	selection := DiskInspectionSelection{RuntimeExecutable: "podman", Image: image, Platform: "linux/" + runtime.GOARCH,
		InspectorDigest: os.Getenv("NKD_TEST_DISK_INSPECTOR_SHA256"), CodecDigest: os.Getenv("NKD_TEST_DISK_CODEC_SHA256"),
		Resources: execution.ResourceRequest{CPUMillis: 1000, MemoryMiB: 10 * 1024, EphemeralStorageMiB: 512}, TimeoutSeconds: 600, Workspace: workspace}
	container, err := OpenDiskInspectionContainer(t.Context(), selection)
	if err != nil {
		t.Fatal(err)
	}
	defer container.Close()
	t.Logf("held runtime: executable=%s daemon=%s", container.runtime.capability.Digest(), container.runtime.identity)
	path := os.Getenv("NKD_TEST_DISK_QCOW2")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := container.AdmitSubject(t.Context(), info.Size()); err != nil {
		t.Fatal(err)
	}
	subject, err := heldfile.OpenContext(t.Context(), path, heldfile.Options{Label: "runtime-test-subject", MaxBytes: 8 << 30, ExpectedDigest: os.Getenv("NKD_TEST_DISK_SHA256")})
	if err != nil {
		t.Fatal(err)
	}
	defer subject.Close()
	observation, err := container.InspectHeld(t.Context(), subject)
	if err != nil {
		t.Fatal(err)
	}
	got, size, closure, inspector, codec := observation.Binding()
	if got != subject.Digest() || size != subject.Size() || closure == "" || inspector != selection.InspectorDigest || codec != selection.CodecDigest {
		t.Fatal("runtime observation differs from exact subject/closure")
	}
	var report struct{ Packages []json.RawMessage }
	if err := json.Unmarshal(observation.Report(), &report); err != nil || len(report.Packages) == 0 {
		t.Fatalf("package report: %v", err)
	}
	t.Logf("runtime-only: image=%s subject=%s bytes=%d packages=%d inspector=%s codec=%s", closure, got, size, len(report.Packages), inspector, codec)
	t.Run("canceled inspection joins and removes container", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		observation, err := container.InspectHeld(ctx, subject)
		if observation != nil || code(err) != e.CodeTimeout {
			t.Fatalf("canceled inspection returned observation=%v error=%v", observation, err)
		}
		// InspectHeld does not return until the input writer and runtime command
		// have joined and the adapter-owned container has been removed.
		filter := fmt.Sprintf("name=nkd-inspect-%d-", os.Getpid())
		raw, err := heldExecutionOutput(t.Context(), container.runtime, "ps", "--all", "--filter", filter, "--format", "{{.Names}}")
		if err != nil || len(bytes.TrimSpace(raw)) != 0 {
			t.Fatalf("canceled inspection leaked a container: %q, %v", raw, err)
		}
	})
}
