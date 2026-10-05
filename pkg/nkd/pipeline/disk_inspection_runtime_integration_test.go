package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"runtime"
	"strings"
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
	runtimeExecutable := os.Getenv("NKD_TEST_DISK_INSPECT_RUNTIME")
	if runtimeExecutable == "" {
		runtimeExecutable = "podman"
	}
	selection := DiskInspectionSelection{RuntimeExecutable: runtimeExecutable, Image: image, Platform: "linux/" + runtime.GOARCH,
		InspectorDigest: os.Getenv("NKD_TEST_DISK_INSPECTOR_SHA256"), CodecDigest: os.Getenv("NKD_TEST_DISK_CODEC_SHA256"),
		Resources: execution.ResourceRequest{CPUMillis: 1000, MemoryMiB: 10 * 1024, EphemeralStorageMiB: 512}, TimeoutSeconds: 600, Workspace: workspace}
	container, err := OpenDiskInspectionContainer(t.Context(), selection)
	if err != nil {
		t.Fatal(err)
	}
	defer container.Close()
	t.Logf("held runtime: selected=%s executable=%s daemon=%s", runtimeExecutable, container.runtime.capability.Digest(), container.runtime.identity)
	path := os.Getenv("NKD_TEST_DISK_QCOW2")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := container.reserveSubject(t.Context(), info.Size(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.Close()
	subject, err := heldfile.OpenContext(t.Context(), path, heldfile.Options{Label: "runtime-test-subject", MaxBytes: 8 << 30, ExpectedDigest: os.Getenv("NKD_TEST_DISK_SHA256")})
	if err != nil {
		t.Fatal(err)
	}
	defer subject.Close()
	observation, err := reservation.InspectHeld(t.Context(), subject)
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
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		lookup := func() ([]byte, error) {
			return heldExecutionOutput(ctx, container.runtime, "ps", "--all", "--no-trunc", "--filter", "label=nkd.dev/inspection-attempt", "--format", "{{.ID}}")
		}
		if raw, err := lookup(); err != nil || len(bytes.TrimSpace(raw)) != 0 {
			t.Fatalf("real cancellation test requires an isolated inspector daemon: %q, %v", raw, err)
		}
		var observation *DiskInspectionObservation
		var inspectErr error
		done := make(chan struct{})
		go func() { observation, inspectErr = reservation.InspectHeld(ctx, subject); close(done) }()
		defer func() { cancel(); <-done }()
		// Do not mistake pre-launch cancellation for appliance cancellation.
		// This opt-in acceptance test requires one isolated running inspector.
		for {
			raw, err := heldExecutionOutput(ctx, container.runtime, "ps", "--no-trunc", "--filter", "label=nkd.dev/inspection-attempt", "--format", "{{.ID}}")
			if err != nil {
				t.Fatal(err)
			}
			ids := strings.Fields(string(raw))
			if len(ids) > 1 {
				t.Fatal("another inspection started during the isolated cancellation test")
			}
			if len(ids) == 1 {
				t.Logf("canceling a running inspection container: %s", ids[0])
				break
			}
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-done:
				timer.Stop()
				t.Fatalf("inspection returned before a running container was observed: %v", inspectErr)
			case <-ctx.Done():
				timer.Stop()
				t.Fatal("inspection container did not start before the bounded deadline")
			case <-timer.C:
			}
		}
		cancel()
		<-done
		if observation != nil || code(inspectErr) != e.CodeTimeout {
			t.Fatalf("canceled inspection returned observation=%v error=%v", observation, inspectErr)
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cleanupCancel()
		raw, err := heldExecutionOutput(cleanupCtx, container.runtime, "ps", "--all", "--filter", "label=nkd.dev/inspection-attempt", "--format", "{{.ID}}")
		if err != nil || len(bytes.TrimSpace(raw)) != 0 {
			t.Fatalf("canceled inspection leaked a container: %q, %v", raw, err)
		}
	})
}
