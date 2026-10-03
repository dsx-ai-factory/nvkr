//go:build linux

package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

// The controlled ELF fixture drives the exact InspectHeld boundary, including
// real host admission and held subprocesses. It proves revalidation and cleanup
// contracts, not a Docker daemon, QCOW2 inspection or package-discovery result.
func TestDiskInspectionPostRunRevalidation(t *testing.T) {
	for _, tt := range []struct {
		name, mode, diagnostic string
		want                   e.Code
	}{
		{name: "unchanged"},
		{name: "image changed after scan", mode: "image", want: e.CodeVerifyFailed, diagnostic: "disk inspection image changed after admission"},
		{name: "daemon changed after scan", mode: "daemon", want: e.CodeEngineCapability, diagnostic: "container runtime identity changed after admission"},
		{name: "subject closed after streaming", mode: "subject", want: e.CodeVerifyFailed, diagnostic: "rechecking inspected disk"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			root := t.TempDir()
			path, _ := copyTestExecutable(t, "docker")
			heldRuntime, err := snapshotRuntimeExecutableContext(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer heldRuntime.capability.Close()
			// Fixture controls are private to this test's held executable. The
			// production runtime admission/scrubbed environment are unchanged.
			heldRuntime.environment = append(heldRuntime.environment,
				"NKD_DISK_INSPECTION_FIXTURE_ROOT="+root, "NKD_DISK_INSPECTION_FIXTURE_MODE="+tt.mode)
			heldRuntime.identity, err = observeHeldRuntimeIdentity(ctx, heldRuntime)
			if err != nil {
				t.Fatal(err)
			}
			selection := testDiskInspectionSelection(t)
			selection.Platform = "linux/" + runtime.GOARCH
			// A manifest pin remains constant while the observed config ID
			// changes, isolating the post-run image-ID comparison itself.
			selection.Image = "example.test/inspection@sha256:" + strings.Repeat("1", 64)
			selection.Resources = execution.ResourceRequest{CPUMillis: 1000, MemoryMiB: 2050, EphemeralStorageMiB: 1}
			selection.TimeoutSeconds = 20
			container := &DiskInspectionContainer{runtime: heldRuntime, selection: selection}
			defer container.Close()
			if err := container.observeBinding(ctx); err != nil {
				t.Fatal(err)
			}
			originalImage := container.imageID
			subject, err := heldfile.FromBytesContext(ctx, []byte("controlled held subject"), heldfile.Options{Label: "revalidation-subject", MaxBytes: 128})
			if err != nil {
				t.Fatal(err)
			}
			defer subject.Close()
			var observation *DiskInspectionObservation
			var inspectErr error
			done := make(chan struct{})
			go func() { observation, inspectErr = container.InspectHeld(ctx, subject); close(done) }()
			// Own the inspection through join even when a test assertion fails.
			defer func() { cancel(); <-done }()
			if tt.mode == "subject" {
				waitDiskInspectionFixtureMarker(t, ctx, done, root, "consumed")
				if err := subject.Close(); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "release"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("controlled inspection did not finish before deadline")
			}
			if code(inspectErr) != tt.want || (observation != nil) != (tt.want == "") {
				t.Fatalf("observation=%v error=%v, want %s", observation, inspectErr, tt.want)
			}
			if tt.diagnostic != "" && !strings.Contains(inspectErr.Error(), tt.diagnostic) {
				t.Fatalf("wrong rejection phase: %v", inspectErr)
			}
			for _, marker := range []string{"consumed", "start-complete", "removed"} {
				if _, err := os.Stat(filepath.Join(root, marker)); err != nil {
					t.Fatalf("successful scan/cleanup phase %s not observed: %v", marker, err)
				}
			}
			commands, err := os.ReadFile(filepath.Join(root, "commands"))
			if err != nil {
				t.Fatal(err)
			}
			// The subject case rejects before post-run image observation; all
			// others must reach concrete runtime re-observation after cleanup.
			if tt.mode != "subject" {
				afterCleanup := strings.SplitN(string(commands), "rm\t--force\t"+strings.Repeat("a", 64)+"\n", 2)
				if len(afterCleanup) != 2 || !strings.Contains(afterCleanup[1], "version\t--format\t{{json .}}") {
					t.Fatalf("post-cleanup runtime recheck not observed: %s", commands)
				}
			}
			if observation != nil {
				digest, size, image, inspector, codec := observation.Binding()
				if digest != subject.Digest() || size != subject.Size() || image != originalImage || inspector != selection.InspectorDigest || codec != selection.CodecDigest || string(observation.Report()) != "controlled-report" {
					t.Fatal("unchanged control has incorrect exact-subject binding")
				}
			}
		})
	}
}

func waitDiskInspectionFixtureMarker(t *testing.T, ctx context.Context, done <-chan struct{}, root, marker string) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(filepath.Join(root, marker)); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		select {
		case <-done:
			t.Fatal("inspection returned before controlled streaming phase")
		case <-ctx.Done():
			t.Fatal("controlled streaming phase did not arrive before deadline")
		case <-ticker.C:
		}
	}
}
