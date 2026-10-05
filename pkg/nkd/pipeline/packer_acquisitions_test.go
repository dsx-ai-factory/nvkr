package pipeline

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/material"
)

func TestAcquisitionWaitReturnsTheOwningCodedError(t *testing.T) {
	for _, operation := range []string{"observe", "snapshot"} {
		t.Run(operation, func(t *testing.T) {
			artifact := &PackerArtifact{}
			artifact.mu.Lock()
			defer artifact.mu.Unlock()
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			var err error
			if operation == "observe" {
				_, err = artifact.ObserveAcquisitionsContext(ctx)
			} else {
				_, err = artifact.snapshotAcquisitionsContext(ctx)
			}
			if code(err) != e.CodeTimeout || strings.Count(err.Error(), string(e.CodeTimeout)) != 1 || !strings.Contains(err.Error(), "waiting for inspection runtime") {
				t.Fatalf("wait error was lost or wrapped twice: %v", err)
			}
		})
	}
}

func testPackerAcquisitionBytes(t *testing.T, architecture string) []byte {
	t.Helper()
	packageBytes := []byte("captured bytes, not authenticated Debian metadata")
	hash := sha256.Sum256(packageBytes)
	digest := hex.EncodeToString(hash[:])
	manifest := material.AcquisitionManifest{SchemaVersion: material.AcquisitionSchemaVersion,
		Architecture: architecture, Scope: "apt-pre-install-pkgs",
		Packages: []material.AcquiredPackage{{Name: "fixture", Version: "1:2.0-1", Architecture: architecture,
			Digest: "sha256:" + digest, Size: int64(len(packageBytes)), Path: "packages/" + digest + ".deb",
			RepositoryURIs: []string{"https://example.invalid/pool/fixture.deb"}}}}
	raw, err := canonical.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, entry := range []struct {
		name    string
		content []byte
	}{{"manifest.json", raw}, {manifest.Packages[0].Path, packageBytes}} {
		if err := writer.WriteHeader(&tar.Header{Name: entry.name, Size: int64(len(entry.content)), Mode: 0o444, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(entry.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestCollectPackerAcquisitionsHoldsSameArtifactAndRevokesOnClose(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		t.Run(architecture, func(t *testing.T) {
			captureBytes := testPackerAcquisitionBytes(t, architecture)
			carrier := testPackerArtifactCarrier(t, "capture.tar", captureBytes)
			runtime := &recordingPackerRuntime{run: func(_ io.Reader, stdout, _ io.Writer, args []string) error {
				if !reflect.DeepEqual(args, []string{"cp", "build:/nkd/work/output/capture.tar", "-"}) {
					t.Fatalf("unexpected runtime operation: %v", args)
				}
				_, err := stdout.Write(carrier)
				return err
			}}
			capture, err := collectPackerContainerAcquisitions(t.Context(), runtime, "build", "/nkd/work/output/capture.tar", t.TempDir(), execution.ResourceRequest{MemoryMiB: 128, EphemeralStorageMiB: 4}, 1024)
			if err != nil {
				t.Fatal(err)
			}
			file, err := os.OpenFile(filepath.Join(t.TempDir(), "disk"), os.O_CREATE|os.O_RDWR, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			artifact := &PackerArtifact{file: file, acquisitions: capture}
			t.Cleanup(func() { _ = artifact.Close() })
			observation, err := artifact.ObserveAcquisitionsContext(t.Context())
			if err != nil || observation.Description().Architecture != architecture {
				t.Fatalf("capture = %v, %v", observation, err)
			}
			if err := artifact.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := artifact.ObserveAcquisitionsContext(t.Context()); code(err) != e.CodeGateUnmet {
				t.Fatalf("closed artifact retained acquisition authority: %v", err)
			}
		})
	}
}

func TestCollectPackerAcquisitionsFailsClosed(t *testing.T) {
	for _, tt := range []struct {
		name    string
		payload []byte
		storage uint64
		cancel  bool
	}{
		{name: "invalid capture", payload: []byte("not a capture"), storage: 4},
		{name: "wrong member", payload: testPackerAcquisitionBytes(t, "amd64"), storage: 4},
		{name: "absent capacity", storage: 1},
		{name: "canceled", storage: 4, cancel: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tt.cancel {
				cancel()
			}
			name := "capture.tar"
			if tt.name == "wrong member" {
				name = "sibling.tar"
			}
			runtime := &recordingPackerRuntime{run: func(_ io.Reader, stdout, _ io.Writer, _ []string) error {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				_, err := stdout.Write(testPackerArtifactCarrier(t, name, tt.payload))
				return err
			}}
			capture, err := collectPackerContainerAcquisitions(ctx, runtime, "build", "/nkd/work/output/capture.tar", t.TempDir(), execution.ResourceRequest{MemoryMiB: 128, EphemeralStorageMiB: tt.storage}, 1024)
			want := e.CodeVerifyFailed
			if tt.name == "absent capacity" {
				want = e.CodeEngineCapability
			}
			if tt.cancel {
				want = e.CodeTimeout
			}
			if capture != nil || code(err) != want {
				t.Fatalf("invalid capture = %v, %v, want %s", capture, err, want)
			}
		})
	}
}

func TestPackerAcquisitionCollectionBudgetsMemoryAndAllConcurrentCopies(t *testing.T) {
	for _, tt := range []struct {
		name            string
		memory, storage uint64
		disk            int64
		want            int64
		reject          bool
	}{
		{name: "low memory high storage", memory: 64, storage: 32768, disk: 1 << 20, reject: true},
		{name: "two sealed RAM copies", memory: 66, storage: 32768, disk: 1 << 20, want: 1 << 20},
		{name: "disk backed output exceeds RAM", memory: 66, storage: 32768, disk: 8 << 30, want: 1 << 20},
		{name: "scratch includes held disk plus three carrier copies", memory: 128, storage: 4, disk: 1 << 20, want: 1 << 20},
		{name: "no scratch after disk", memory: 128, storage: 1, disk: 1 << 20, reject: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := packerAcquisitionCollectionLimit(execution.ResourceRequest{MemoryMiB: tt.memory, EphemeralStorageMiB: tt.storage}, tt.disk)
			if tt.reject {
				if code(err) != e.CodeEngineCapability {
					t.Fatalf("missing admission: %v", err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("limit=%d,err=%v,want%d", got, err, tt.want)
			}
		})
	}
	called := false
	runtime := &recordingPackerRuntime{run: func(io.Reader, io.Writer, io.Writer, []string) error { called = true; return nil }}
	_, err := collectPackerContainerAcquisitions(t.Context(), runtime, "build", "/nkd/work/output/capture.tar", t.TempDir(), execution.ResourceRequest{MemoryMiB: 64, EphemeralStorageMiB: 32768}, 1024)
	if called || code(err) != e.CodeEngineCapability {
		t.Fatalf("low-memory collector started runtime: %t,%v", called, err)
	}
}
