// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"archive/tar"
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

// This opt-in runs the real helper through concrete acquisition and container
// injection against a pinned appliance. It proves that handoff and a bounded
// archive scan or synthetic QCOW2 inventory, not a source build, complete disk
// reconciliation or qualification.
func TestProducedInspectorInjectionReal(t *testing.T) {
	helper := os.Getenv("NVKR_TEST_PRODUCED_INSPECTOR")
	if helper == "" {
		t.Skip("requires a built native helper, local pinned appliance and native Docker/Podman")
	}
	qcow := os.Getenv("NVKR_TEST_DISK_QCOW2")
	if missing := missingProducedInspectorVariables(os.Getenv, qcow != ""); len(missing) != 0 {
		t.Fatalf("injection probe requires companion variables: %s", strings.Join(missing, ", "))
	}
	if runtime.GOOS != "linux" {
		t.Fatal("injection probe requires native Linux")
	}
	raw, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(raw) }))
	defer server.Close()
	graph := producedExecutableGraph("linux/"+runtime.GOARCH, FileDigest(raw))
	graph.Nodes[0].Artifact = server.URL + "/helper"
	_, tool, err := (Local{}).ExecuteProducedExecutable(graph, mustGraphDigest(t, graph), "helper", RunOptions{Ctx: t.Context(), Launch: true, SourceRoot: filepath.Join(t.TempDir(), "src")})
	if err != nil {
		t.Fatal(err)
	}
	defer tool.Close()
	workspace, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	selection := DiskInspectionSelection{
		RuntimeExecutable: os.Getenv("NVKR_TEST_DISK_INSPECT_RUNTIME"), Workspace: workspace,
		BootstrapInterpreterDigest: os.Getenv("NVKR_TEST_BOOTSTRAP_INTERPRETER_SHA256"), BootstrapProgramDigest: InspectionBootstrapProgramDigest(),
		Image: os.Getenv("NVKR_TEST_DISK_INSPECT_IMAGE"), Platform: "linux/" + runtime.GOARCH,
		InspectorDigest: os.Getenv("NVKR_TEST_DISK_INSPECTOR_SHA256"), CodecDigest: os.Getenv("NVKR_TEST_DISK_CODEC_SHA256"),
		GuestfishDigest: os.Getenv("NVKR_TEST_DISK_GUESTFISH_SHA256"), ScannerDigest: os.Getenv("NVKR_TEST_DISK_SCANNER_SHA256"),
		PackageReaderDigest: os.Getenv("NVKR_TEST_DISK_PACKAGE_READER_SHA256"), ToolName: "helper", Tool: tool,
		Resources:      execution.ResourceRequest{CPUMillis: 1000, MemoryMiB: 6144, EphemeralStorageMiB: 64},
		TimeoutSeconds: 120, MaxFilesystemBytes: 1 << 20, ScratchStorage: "memory",
	}
	if qcow != "" {
		info, err := os.Stat(qcow)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() <= 0 || info.Size() > 16<<20 {
			t.Fatal("QCOW2 probe requires a bounded small synthetic fixture")
		}
		selection.GuestfishDigest, selection.ScannerDigest, selection.PackageReaderDigest = "", "", ""
		selection.MaxFilesystemBytes, selection.ScratchStorage = 0, ""
		selection.Resources.MemoryMiB = 2048 + 2*uint64((info.Size()+(1<<20)-1)>>20) + uint64((len(raw)+(1<<20)-1)>>20) + 16
	}
	container, err := OpenDiskInspectionContainer(t.Context(), selection)
	if err != nil {
		t.Fatal(err)
	}
	defer container.Close()
	before, err := container.AuthenticateProducedTool(t.Context(), "helper", tool)
	if err != nil {
		t.Fatal(err)
	}
	description, err := container.SelectionContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if description.Tool != nil || description.Workspace != nil || description.bootstrapCommand != "" {
		t.Fatal("description leaked borrowed authority")
	}
	var archive bytes.Buffer
	w := tar.NewWriter(&archive)
	data := []byte("native injected helper probe\n")
	if err := w.WriteHeader(&tar.Header{Name: "payload.txt", Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	subjectPath := qcow
	if subjectPath == "" {
		subjectPath = filepath.Join(t.TempDir(), "subject.tar")
		if err := os.WriteFile(subjectPath, archive.Bytes(), 0o400); err != nil {
			t.Fatal(err)
		}
	}
	subjectBytes, err := os.ReadFile(subjectPath)
	if err != nil {
		t.Fatal(err)
	}
	subject, err := heldfile.OpenContext(t.Context(), subjectPath, heldfile.Options{Label: "injected-helper-probe", MaxBytes: 16 << 20, ExpectedDigest: FileDigest(subjectBytes)})
	if err != nil {
		t.Fatal(err)
	}
	defer subject.Close()
	var observation *DiskInspectionObservation
	if qcow == "" {
		observation, err = container.InspectHeldContent(t.Context(), subject, "tarball", selection.Platform)
	} else {
		observation, err = container.InspectHeld(t.Context(), subject)
	}
	if err != nil {
		t.Fatal(err)
	}
	name, binding := observation.ToolBinding()
	if name != "helper" || binding != before {
		t.Fatal("operation changed its exact produced helper receipt")
	}
	digest, size, image, inspector, codec := observation.Binding()
	if digest != subject.Digest() || size != subject.Size() || image == "" || inspector != selection.InspectorDigest || codec != selection.CodecDigest {
		t.Fatal("operation lost its exact subject/appliance binding")
	}
	expected := "payload.txt"
	if qcow != "" {
		expected = "probe-package"
	}
	if !bytes.Contains(observation.Report(), []byte(expected)) {
		t.Fatal("helper omitted expected fixture content")
	}
	if err := tool.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := container.BindingContext(t.Context()); code(err) != e.CodeVerifyFailed {
		t.Fatalf("revoked helper remained usable: %v", err)
	}
	t.Logf("native injected helper: platform=%s helper=%s terminal=%s appliance=%s subject=%s bytes=%d", selection.Platform, binding.Digest, binding.TerminalDigest, image, digest, size)
}

// Inventory-only QCOW2 inspection needs the runtime, appliance, inspector and
// codec. Archive content inspection additionally authenticates all content tools.
func missingProducedInspectorVariables(getenv func(string) string, qcow bool) []string {
	required := []string{"NVKR_TEST_BOOTSTRAP_INTERPRETER_SHA256", "NVKR_TEST_DISK_INSPECT_RUNTIME", "NVKR_TEST_DISK_INSPECT_IMAGE",
		"NVKR_TEST_DISK_INSPECTOR_SHA256", "NVKR_TEST_DISK_CODEC_SHA256"}
	if !qcow {
		required = append(required, "NVKR_TEST_DISK_GUESTFISH_SHA256", "NVKR_TEST_DISK_SCANNER_SHA256", "NVKR_TEST_DISK_PACKAGE_READER_SHA256")
	}
	var missing []string
	for _, name := range required {
		if strings.TrimSpace(getenv(name)) == "" {
			missing = append(missing, name)
		}
	}
	slices.Sort(missing)
	return missing
}

func TestProducedInspectorVariablePreflight(t *testing.T) {
	for _, tc := range []struct {
		name    string
		qcow    bool
		values  map[string]string
		missing []string
	}{
		{"archive missing all", false, nil, []string{"NVKR_TEST_BOOTSTRAP_INTERPRETER_SHA256", "NVKR_TEST_DISK_CODEC_SHA256", "NVKR_TEST_DISK_GUESTFISH_SHA256", "NVKR_TEST_DISK_INSPECTOR_SHA256", "NVKR_TEST_DISK_INSPECT_IMAGE", "NVKR_TEST_DISK_INSPECT_RUNTIME", "NVKR_TEST_DISK_PACKAGE_READER_SHA256", "NVKR_TEST_DISK_SCANNER_SHA256"}},
		{"qcow missing all", true, nil, []string{"NVKR_TEST_BOOTSTRAP_INTERPRETER_SHA256", "NVKR_TEST_DISK_CODEC_SHA256", "NVKR_TEST_DISK_INSPECTOR_SHA256", "NVKR_TEST_DISK_INSPECT_IMAGE", "NVKR_TEST_DISK_INSPECT_RUNTIME"}},
		{"qcow needs no content tools", true, map[string]string{"NVKR_TEST_BOOTSTRAP_INTERPRETER_SHA256": "bootstrap", "NVKR_TEST_DISK_INSPECT_RUNTIME": "docker", "NVKR_TEST_DISK_INSPECT_IMAGE": "image", "NVKR_TEST_DISK_INSPECTOR_SHA256": "inspector", "NVKR_TEST_DISK_CODEC_SHA256": "codec"}, nil},
		{"archive incomplete content tools", false, map[string]string{"NVKR_TEST_BOOTSTRAP_INTERPRETER_SHA256": "bootstrap", "NVKR_TEST_DISK_INSPECT_RUNTIME": "docker", "NVKR_TEST_DISK_INSPECT_IMAGE": "image", "NVKR_TEST_DISK_INSPECTOR_SHA256": "inspector", "NVKR_TEST_DISK_CODEC_SHA256": "codec", "NVKR_TEST_DISK_GUESTFISH_SHA256": "guestfish", "NVKR_TEST_DISK_SCANNER_SHA256": " "}, []string{"NVKR_TEST_DISK_PACKAGE_READER_SHA256", "NVKR_TEST_DISK_SCANNER_SHA256"}},
		{"archive complete", false, map[string]string{"NVKR_TEST_BOOTSTRAP_INTERPRETER_SHA256": "bootstrap", "NVKR_TEST_DISK_INSPECT_RUNTIME": "docker", "NVKR_TEST_DISK_INSPECT_IMAGE": "image", "NVKR_TEST_DISK_INSPECTOR_SHA256": "inspector", "NVKR_TEST_DISK_CODEC_SHA256": "codec", "NVKR_TEST_DISK_GUESTFISH_SHA256": "guestfish", "NVKR_TEST_DISK_SCANNER_SHA256": "scanner", "NVKR_TEST_DISK_PACKAGE_READER_SHA256": "reader"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := missingProducedInspectorVariables(func(name string) string { return tc.values[name] }, tc.qcow)
			if !slices.Equal(got, tc.missing) {
				t.Fatalf("missing=%v want=%v", got, tc.missing)
			}
		})
	}
}
