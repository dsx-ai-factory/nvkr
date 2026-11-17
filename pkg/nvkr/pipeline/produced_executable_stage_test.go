// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func producedToolStreamFixture(t *testing.T) (*ProducedExecutable, string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("held executable staging requires Linux")
	}
	raw := producedExecutableELFFixture()
	graph := producedExecutableGraph("linux/"+runtime.GOARCH, FileDigest(raw))
	root := t.TempDir()
	payload := filepath.Join(root, "fetch", "helper", "helper")
	if err := os.MkdirAll(filepath.Dir(payload), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, raw, 0o700); err != nil {
		t.Fatal(err)
	}
	writeResultForTest(t, root, "pull-helper", ArtifactResult{Component: "helper", OutputType: "host-package", SHA256: FileDigest(raw), Size: int64(len(raw)),
		Qualification: ArtifactProfileFreeAcquisition, EngineClass: ir.EnginePrebuiltAcquire, Path: "helper"})
	tool, err := captureProducedExecutableFixture(t.Context(), graph, mustGraphDigest(t, graph), payload, filepath.Join(root, "pull-helper.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tool.Close() })
	return tool, payload
}

// The mutation occurs after the held-byte writer has begun, so admission cannot
// detect it in advance. Restoring the path afterward cannot turn hostile bytes
// into the bytes executed by the container.
type swappingHelperWriter struct {
	output  bytes.Buffer
	path    string
	swapped bool
	t       *testing.T
}

func (w *swappingHelperWriter) Write(p []byte) (int, error) {
	if !w.swapped {
		w.swapped = true
		if err := os.Rename(w.path, w.path+"-original"); err != nil {
			w.t.Fatal(err)
		}
		if err := os.WriteFile(w.path, []byte("hostile executable"), 0o700); err != nil {
			w.t.Fatal(err)
		}
	}
	return w.output.Write(p)
}
func TestProducedHelperStreamRejectsMutablePaths(t *testing.T) {
	for _, variant := range []string{"exact", "swapped during stream", "changed digest", "changed size", "revoked", "canceled"} {
		t.Run(variant, func(t *testing.T) {
			tool, path := producedToolStreamFixture(t)
			binding, err := tool.BindingContext(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			switch variant {
			case "changed digest":
				binding.Digest = FileDigest([]byte("other"))
			case "changed size":
				binding.Size++
			case "revoked":
				if err := tool.Close(); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			var output bytes.Buffer
			if variant == "swapped during stream" {
				writer := &swappingHelperWriter{path: path, t: t}
				err = writeProducedExecutable(ctx, tool, binding, writer)
				if err != nil {
					t.Fatal(err)
				}
				if !writer.swapped || !bytes.Equal(writer.output.Bytes(), producedExecutableELFFixture()) {
					t.Fatal("mutable host bytes reached stream")
				}
				if err := os.Rename(path, path+"-hostile"); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(path+"-original", path); err != nil {
					t.Fatal(err)
				}
				if _, err := tool.BindingContext(ctx); err != nil {
					t.Fatal(err)
				}
				return
			}
			err = writeProducedExecutable(ctx, tool, binding, &output)
			if variant == "exact" {
				if err != nil || !bytes.Equal(output.Bytes(), producedExecutableELFFixture()) {
					t.Fatalf("stream=%x error=%v", output.Bytes(), err)
				}
			} else {
				want := e.CodeVerifyFailed
				if variant == "canceled" {
					want = e.CodeTimeout
				}
				if code(err) != want || output.Len() != 0 {
					t.Fatalf("invalid capability streamed bytes: %v", err)
				}
			}
		})
	}
}
func TestProducedHelperArgumentsPreserveContainment(t *testing.T) {
	tool, _ := producedToolStreamFixture(t)
	selection := testDiskInspectionSelection(t)
	selection.Tool, selection.ToolName = tool, "helper"
	selection.BootstrapInterpreterDigest, selection.BootstrapProgramDigest = selection.CodecDigest, InspectionBootstrapProgramDigest()
	var err error
	selection.streamBinding, err = tool.BindingContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	subject, err := heldfile.FromBytesContext(t.Context(), []byte("subject"), heldfile.Options{Label: "fixture", MaxBytes: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer subject.Close()
	args := diskInspectionCreateArgs(selection, selection.Image, "container", "attempt", subject)
	for _, want := range []string{"--network=none", "--read-only", "--user=65534:65534", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--entrypoint=/usr/bin/perl", inspectionBootstrapProgram, selection.streamBinding.Digest, subject.Digest()} {
		if !slices.Contains(args, want) {
			t.Fatalf("missing argument %q", want)
		}
	}
	if slices.Contains(args, "--mount") || slices.Contains(args, "--volume") {
		t.Fatal("helper selected a host mount")
	}
}
func TestProducedHelperResourceAccounting(t *testing.T) {
	for _, variant := range []string{"exact", "host memory", "workspace", "container memory", "declared storage"} {
		t.Run(variant, func(t *testing.T) {
			selection := DiskInspectionSelection{Platform: "linux/amd64", helperBytes: 1 << 20,
				Resources: execution.ResourceRequest{CPUMillis: 1000, MemoryMiB: 2051, EphemeralStorageMiB: 1}}
			facts := execution.ObservedHostFacts{Platform: execution.HostPlatformLinuxAMD64,
				Resources: execution.ResourceCapacity{CPUMillis: 1000, MemoryMiB: 2053, EphemeralStorageMiB: 2}}
			switch variant {
			case "host memory":
				facts.Resources.MemoryMiB--
			case "workspace":
				facts.Resources.EphemeralStorageMiB--
			case "container memory":
				selection.Resources.MemoryMiB--
			case "declared storage":
				selection.Resources.EphemeralStorageMiB = 0
			}
			err := validateDiskInspectionResources(selection, facts, 1<<20, false)
			if variant == "exact" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if code(err) != e.CodeEngineCapability {
				t.Fatalf("capacity error=%v", err)
			}
		})
	}
}
