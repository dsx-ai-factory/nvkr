// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
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

func producedToolStageFixture(t *testing.T) (*ProducedExecutable, *stagedProducedExecutable) {
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
	workspace, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	stage, err := stageProducedExecutable(t.Context(), tool, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stage.close() })
	return tool, stage
}

func TestProducedToolStageRejectsMutablePaths(t *testing.T) {
	for _, variant := range []string{"valid", "changed bytes", "replaced inode", "writable", "revoked", "replaced directory"} {
		t.Run(variant, func(t *testing.T) {
			tool, stage := producedToolStageFixture(t)
			file := filepath.Join(stage.root, producedToolFilename)
			switch variant {
			case "changed bytes":
				if err := os.Chmod(file, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte("changed"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(file, 0o555); err != nil {
					t.Fatal(err)
				}
			case "replaced inode":
				if err := os.Chmod(stage.root, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(file, file+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, producedExecutableELFFixture(), 0o555); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(stage.root, 0o555); err != nil {
					t.Fatal(err)
				}
			case "writable":
				if err := os.Chmod(file, 0o755); err != nil {
					t.Fatal(err)
				}
			case "revoked":
				if err := tool.Close(); err != nil {
					t.Fatal(err)
				}
			case "replaced directory":
				if err := os.Rename(stage.root, stage.root+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(stage.root, 0o555); err != nil {
					t.Fatal(err)
				}
			}
			err := stage.verify(t.Context())
			if variant == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if code(err) != e.CodeVerifyFailed {
				t.Fatalf("mutation error=%v", err)
			}
			if variant == "replaced directory" {
				if code(stage.close()) != e.CodeVerifyFailed {
					t.Fatal("cleanup accepted a substituted directory")
				}
				if err := os.Rename(stage.root, stage.root+"-replacement"); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(stage.root+"-original", stage.root); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestProducedHelperMountAdmission(t *testing.T) {
	for _, variant := range []string{"valid", "missing", "changed source", "writable", "entrypoint", "extra mount"} {
		t.Run(variant, func(t *testing.T) {
			tool, stage := producedToolStageFixture(t)
			selection := DiskInspectionSelection{Tool: tool, ToolName: "helper", toolStage: stage}
			var observed toolSmokeInspection
			observed.Config.Entrypoint = []string{producedToolContainerDirectory + "/" + producedToolFilename}
			observed.Mounts = append(observed.Mounts, struct {
				Type, Source, Destination string
				RW                        bool
			}{
				Type: "bind", Source: stage.root, Destination: producedToolContainerDirectory})
			switch variant {
			case "missing":
				observed.Mounts = nil
			case "changed source":
				observed.Mounts[0].Source += "-other"
			case "writable":
				observed.Mounts[0].RW = true
			case "entrypoint":
				observed.Config.Entrypoint[0] = "/bin/sh"
			case "extra mount":
				observed.Mounts = append(observed.Mounts, observed.Mounts[0])
			}
			err := verifyProducedHelperMount(t.Context(), selection, observed)
			if variant == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if code(err) != e.CodeVerifyFailed {
				t.Fatalf("mount substitution error=%v", err)
			}
		})
	}
}

func TestProducedHelperArgumentsPreserveContainment(t *testing.T) {
	tool, stage := producedToolStageFixture(t)
	selection := testDiskInspectionSelection(t)
	selection.Tool, selection.ToolName, selection.toolStage = tool, "helper", stage
	subject, err := heldfile.FromBytesContext(t.Context(), []byte("subject"), heldfile.Options{Label: "fixture", MaxBytes: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer subject.Close()
	args := diskInspectionCreateArgs(selection, selection.Image, "container", "attempt", subject)
	for _, want := range []string{"--network=none", "--read-only", "--user=65534:65534", "--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--entrypoint=" + producedToolContainerDirectory + "/" + producedToolFilename,
		"type=bind,source=" + stage.root + ",target=" + producedToolContainerDirectory + ",readonly", subject.Digest()} {
		if !slices.Contains(args, want) {
			t.Fatalf("missing containment argument %q", want)
		}
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
