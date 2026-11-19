// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"reflect"
	"strings"
	"testing"
)

// The dedicated NVIDIA Workload Identity catalog owns one source-built host-package
// producer for the public v1.15.0 DSX IMDS NodeAttestor agent. These checks protect
// the authored declarations and the resolved pin identities; they do not execute a
// producer or establish hosted acceptance.
func TestWLIIMDSAgentCatalogIdentity(t *testing.T) {
	dir := Dir("../../../inputs/catalogs/nvidia-workload-identity")
	cat, err := LoadContext(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := LoadLockContext(t.Context(), "../../../inputs/catalogs/nvidia-workload-identity")
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Components) != 1 {
		t.Fatalf("catalog declares %d components, want exactly the agent producer", len(cat.Components))
	}
	component, ok := cat.Components["dsx-imds-node-attestor-agent"]
	if !ok {
		t.Fatal("catalog does not declare the dsx-imds-node-attestor-agent producer")
	}
	if !component.FromSource || component.Prebuilt != nil || component.ExclusionReason != "" {
		t.Fatal("agent producer is not a plain from-source selection")
	}
	if component.BuilderType != "oci-hermetic" || component.OutputType != "host-package" || component.CGO {
		t.Fatalf("unexpected producer route: %+v", component)
	}
	if want := []string{"linux/amd64", "linux/arm64"}; !reflect.DeepEqual(component.Platforms, want) {
		t.Fatalf("platforms = %v, want %v", component.Platforms, want)
	}
	if component.Helm != nil || component.Disk != nil || component.Packer != nil || len(component.Materials) != 0 || component.Tool != "" {
		t.Fatal("agent producer declares an image, chart, disk or tool route outside the executable scope")
	}
	if component.ArtifactPath != "nvkr-output/dsx-imds-node-attestor-agent" || component.WriteDir != "nvkr-output" {
		t.Fatalf("unexpected artifact placement %q in %q", component.ArtifactPath, component.WriteDir)
	}
	build := strings.Join(component.BuildCommand, "\n")
	for _, required := range []string{
		"./cmd/dsx-imds-node-attestor-agent", "CGO_ENABLED=0", "-mod=vendor", "-trimpath",
		"-buildvcs=false", "GOPROXY=off", `test "$(go env GOARCH)" = "{{.Arch}}"`,
	} {
		if !strings.Contains(build, required) {
			t.Fatalf("build command omits %q", required)
		}
	}
	vendor := strings.Join(component.VendorCommand, "\n")
	for _, required := range []string{"go mod download", "go mod verify", "go mod vendor", `test "$before" = "$after"`} {
		if !strings.Contains(vendor, required) {
			t.Fatalf("vendor command omits %q", required)
		}
	}

	pin, ok := lock.Pins["nvidia-workload-identity"]
	if !ok {
		t.Fatal("lock does not pin the workload-identity source")
	}
	want := Pin{Tag: "v1.15.0", Commit: "cd76f1d5ab4a887b9577ff601ce7cda52d5003b4",
		TreeHash: "b9c85948e417d42d7529d2306f1b80db61c629ae", SourceDateEpoch: 1792708538}
	if !reflect.DeepEqual(pin, want) {
		t.Fatalf("agent source pin = %+v, want %+v", pin, want)
	}
	if len(cat.Sources) != 5 {
		t.Fatalf("catalog declares %d sources, want the agent source plus four admitted actions", len(cat.Sources))
	}
	source := cat.Sources["nvidia-workload-identity"]
	if source.Repo != "https://github.com/dsx-ai-factory/nvidia-workload-identity" || source.ToolchainBaseImage != "" || source.Kind != "" {
		t.Fatalf("unexpected source declaration: %+v", source)
	}
	wantImages := map[string]string{
		"amd64": "docker.io/library/golang@sha256:78773c59d6d4ecedc0845b1a8e5379e7bf0e0be62f4ea796e689bbc97dab3ceb",
		"arm64": "docker.io/library/golang@sha256:7117c9e4e2c0becd23d1dbede3aea73ba91d084847c68e12d7e1fa3d9b3abf20",
	}
	if !reflect.DeepEqual(source.ToolchainImagesByArch, wantImages) {
		t.Fatalf("toolchain images = %v, want %v", source.ToolchainImagesByArch, wantImages)
	}

	// The generated workflow actions must stay on the maintained shared cohort: the
	// dedicated lock resolves the same exact pins the maintained NVKE catalog records.
	cohort, err := LoadLockContext(t.Context(), "../../../inputs/catalogs/nvke")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"checkout", "download-artifact", "setup-go", "upload-artifact"} {
		selected, selectedOK := lock.Pins[name]
		maintained, maintainedOK := cohort.Pins[name]
		if !selectedOK || !maintainedOK {
			t.Fatalf("action pin %q is missing: selected=%t maintained=%t", name, selectedOK, maintainedOK)
		}
		if !reflect.DeepEqual(selected, maintained) {
			t.Fatalf("action pin %q drifted from the maintained cohort: %+v != %+v", name, lock.Pins[name], cohort.Pins[name])
		}
	}
}
