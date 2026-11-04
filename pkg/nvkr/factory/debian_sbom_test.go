// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

func persistDebianSBOMFixture(t *testing.T, plan *Plan, artifacts map[string]Artifact, subject string, bom *sbom.BOM) {
	t.Helper()
	raw, err := bom.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := bom.Digest()
	if err != nil {
		t.Fatal(err)
	}
	index := sbom.Index{SchemaVersion: sbom.IndexSchemaVersion, BuildDigest: plan.Builds[0].BuildDigest,
		Entries: []sbom.IndexEntry{{Artifact: "agent", ArtifactSHA: subject, SBOMDigest: digest}}}
	indexRaw, err := canonical.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{"agent.cdx.json": raw, "index.json": indexRaw} {
		if err := writeFactoryFile(t.Context(), filepath.Join(artifacts["sbom/agent"].Path, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// Serialized evidence remains descriptive until publication rejoins its exact
// original producer and planned pins. Live package/scanner execution is separate.
func TestDebianPublicationSBOMRequiresOriginalAuthorityBindings(t *testing.T) {
	for _, scenario := range []string{"exact", "reader", "platform", "scanner", "subject"} {
		t.Run(scenario, func(t *testing.T) {
			d, environment, stack := debianObjectPublicationInputs(t, true)
			plan, err := Compile(t.Context(), d, environment, stack, "test")
			if err != nil {
				t.Fatal(err)
			}
			t.Chdir(t.TempDir())
			_, artifacts := planIndex(plan)
			payload := artifacts[plan.LifecycleOutputs[0].ArtifactID]
			if err := writeFactoryFile(t.Context(), payload.Path, []byte("original producer subject"), 0o600); err != nil {
				t.Fatal(err)
			}
			result := writeProducerRecordFixture(t, plan, payload, artifacts["result/agent/agent"].Path)
			bom, err := sbom.NewSubject(plan.Builds[0].BuildDigest, "agent", result.SHA256)
			if err != nil {
				t.Fatal(err)
			}
			profile := environment.DiskInspectionProfiles[environment.UtilityPlatform]
			bom, err = sbom.WithDependencies(bom, []sbom.Component{{Type: "library", Name: "payload", Version: "1", PURL: "pkg:generic/payload@1"}}, "syft-cyclonedx/v1", profile.ScannerDigest)
			if err != nil {
				t.Fatal(err)
			}
			for i := range bom.Metadata.Props {
				if bom.Metadata.Props[i].Name == "nvkr:dependencyInspection" {
					bom.Metadata.Props[i].Value = sbom.DebianPayloadMethod
				}
			}
			bom.Metadata.Props = append(bom.Metadata.Props, sbom.Property{Name: "nvkr:packageReaderDigest", Value: profile.PackageReaderDigest}, sbom.Property{Name: "nvkr:dependencyPlatform", Value: plan.Builds[0].Platform})
			property := map[string]string{"reader": "nvkr:packageReaderDigest", "platform": "nvkr:dependencyPlatform", "scanner": "nvkr:dependencyScannerDigest"}[scenario]
			for i := range bom.Metadata.Props {
				if bom.Metadata.Props[i].Name == property {
					bom.Metadata.Props[i].Value = "sha256:" + strings.Repeat("9", 64)
					if scenario == "platform" {
						bom.Metadata.Props[i].Value = "linux/arm64"
					}
				}
			}
			sort.Slice(bom.Metadata.Props, func(i, j int) bool { return bom.Metadata.Props[i].Name < bom.Metadata.Props[j].Name })
			if scenario == "subject" {
				bom, err = sbom.ForSubject(bom, "sha256:"+strings.Repeat("8", 64), "build-output", "")
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := bom.Validate(); err != nil {
				t.Fatal("substituted fixture must reach publication admission", err)
			}
			persistDebianSBOMFixture(t, plan, artifacts, result.SHA256, bom)
			objects, err := sbomPublicationObjects(t.Context(), plan, artifacts["sbom/agent"], artifacts, func(path string) (string, error) { return path, nil })
			if scenario == "exact" {
				if err != nil || len(objects) != 2 {
					t.Fatalf("exact Debian evidence refused: %v", err)
				}
			} else {
				assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
				if objects != nil {
					t.Fatal("substituted Debian evidence escaped publication admission")
				}
			}
		})
	}
}

// This follows the existing sbom Debian material fixture: real dpkg-deb exports
// a real bounded package, while the pinned controlled scanner emits no packages.
// It proves the factory/reader/scanner join, not native Syft or installation.
func TestDebianProducerSBOMExecutesPinnedPackageAndPayloadOwners(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("held package-reader execution requires Linux")
	}
	readerBytes, err := os.ReadFile("/usr/bin/dpkg-deb")
	if os.IsNotExist(err) {
		t.Skip("existing package reader is unavailable")
	}
	if err != nil {
		t.Fatal(err)
	}
	script := []byte("#!/bin/sh\nprintf '%s' '{\"schema\":{\"version\":\"16.1.10\"},\"artifacts\":[]}'\n")
	d, environment, original := debianObjectPublicationInputs(t, true)
	source := *original.Catalog
	source.Tools = maps.Clone(source.Tools)
	pin := source.Tools[d.SBOMTool]
	pin.ArchiveDigest, pin.ExecutableDigest = pipeline.FileDigest(script), pipeline.FileDigest(script)
	source.Tools[d.SBOMTool] = pin
	profile := environment.DiskInspectionProfiles[environment.UtilityPlatform]
	profile.ScannerDigest, profile.PackageReaderDigest, profile.MaxFilesystemBytes = pin.ExecutableDigest, pipeline.FileDigest(readerBytes), 1<<20
	environment.DiskInspectionProfiles[environment.UtilityPlatform] = profile
	stack, err := catalog.ApplyStack(&source)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), d, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	nodes, artifacts := planIndex(plan)
	payload := artifacts[plan.LifecycleOutputs[0].ArtifactID]
	for path, raw := range map[string][]byte{
		"package/DEBIAN/control":           []byte("Package: nvkr-test-payload\nVersion: 1:2.3-4\nArchitecture: all\nMaintainer: NVKR Test <test@example.invalid>\nDescription: controlled payload fixture\n"),
		"package/usr/share/nvkr/payload":   []byte("original package payload"),
		artifacts["tool/"+d.SBOMTool].Path: script,
	} {
		if err := writeFactoryFile(t.Context(), path, raw, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(payload.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/usr/bin/dpkg-deb", "--build", "package", payload.Path)
	command.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "SOURCE_DATE_EPOCH=0"}
	if diagnostic, err := command.CombinedOutput(); err != nil {
		t.Fatalf("existing package builder failed: %v: %s", err, diagnostic)
	}
	result := writeProducerRecordFixture(t, plan, payload, artifacts["result/agent/agent"].Path)
	result.InputDigest = pipeline.FileDigest([]byte("controlled original prepared source"))
	writeRawResult(t, artifacts["result/agent/agent"].Path, result)
	bom, err := sbom.NewSubject(plan.Builds[0].BuildDigest, "agent", result.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	bom, err = inspectDebianProducerSBOM(ctx, plan, payload, artifacts, sbom.Scanner{Path: artifacts["tool/"+d.SBOMTool].Path, Digest: pin.ExecutableDigest}, bom)
	if err != nil {
		t.Fatal(err)
	}
	if err := sbom.ValidateDebianPayloadEvidence(bom, plan.Builds[0].Platform, result.SHA256); err != nil || len(bom.Components) == 0 {
		t.Fatal("factory lost original package payload bindings", err)
	}
	if err := executeSBOM(ctx, plan, nodes["agent/sbom"], artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}
	objects, err := sbomPublicationObjects(ctx, plan, artifacts["sbom/agent"], artifacts, func(path string) (string, error) { return path, nil })
	if err != nil || len(objects) != 2 {
		t.Fatalf("executed Debian evidence failed publication admission: %v", err)
	}
}
