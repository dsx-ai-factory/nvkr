// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/runpresenter"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/releaseview"
)

func TestReleaseBrowserMetadataDetailsAndExportParity(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	publication := delivery.PublicationDescription{Outputs: []delivery.PublicationOutput{
		{Output: delivery.Output{Name: "chart", Kind: "helm-oci", ChartVersion: "2.3.4", Components: []string{"service"}}},
		{Output: delivery.Output{Name: "disk", Kind: "disk-image", OS: "linux", Arch: "arm64", Disk: &delivery.DiskOutput{
			Format: "qcow2", Encryption: "luks2", EvidenceMode: "build-provenance-v1", PayloadDigest: digest, Size: 4096}}},
		{Output: delivery.Output{Name: "multi", Kind: "oci", Digest: digest, ImageIndex: &delivery.ImageIndexBinding{
			RequiredPlatforms: []string{"linux/amd64", "linux/arm64"},
			Children:          []delivery.ImageIndexChildBinding{{Platform: "linux/arm64", Component: "worker", BuildSubject: digest, ManifestDigest: digest}},
		}}},
		{Output: delivery.Output{Name: "tools", Kind: "executable", OS: "linux", Arch: "amd64", Reference: "private-artifact-reference-sentinel"},
			BuildSubjects: []delivery.BuildSubject{{Component: "cli", Digest: digest}}},
	}, Evidence: []delivery.PublicationEvidence{{Name: "shared", Kind: "sbom", Reference: "private-evidence-reference-sentinel",
		Binding: delivery.EvidenceBinding{Outputs: []string{"multi", "tools"}, Component: "worker", Platform: "linux/arm64", SubjectDigest: digest},
		Source:  &delivery.PublicationSource{Repo: "private-transport-sentinel", Commit: "private-invocation-sentinel"},
	}}}
	for _, test := range []struct {
		name, summary string
		fields        []progress.Field
	}{
		{"chart", "helm-oci · 2.3.4", commandFields("Chart version", "2.3.4", "Component", "service")},
		{"disk", "disk-image · linux/arm64", commandFields("Disk format", "qcow2", "Declared encryption", "luks2", "Payload digest", digest, "Payload bytes", "4096")},
		{"multi", "oci · linux/amd64, linux/arm64", commandFields("Required platform", "linux/amd64", "Required platform", "linux/arm64", "linux/arm64 component", "worker", "linux/arm64 build subject", digest)},
		{"tools", "executable · linux/amd64", commandFields("cli build subject", digest, "Recorded platform", "linux/amd64")},
	} {
		t.Run(test.name, func(t *testing.T) {
			display, err := releasePublicationDisplay(publication, test.name)
			if err != nil {
				t.Fatal(err)
			}
			assertReleasePresentationSafe(t, progress.Snapshot{Items: display.Items, Fields: display.Fields})
			item := display.Items[0]
			if item.Name != test.name || item.Summary != test.summary || item.State != "metadata" {
				t.Fatalf("wrong selected output or scope: %+v", item)
			}
			for _, field := range test.fields {
				if !slices.Contains(item.Fields, field) {
					t.Fatalf("missing metadata: %+v", field)
				}
			}
			if test.name == "chart" && slices.ContainsFunc(item.Fields, func(f progress.Field) bool { return f.Label == "Recorded platform" }) {
				t.Fatal("chart inherited a platform")
			}
			if test.name == "tools" || test.name == "multi" {
				evidence := display.Items[1]
				for _, name := range []string{"multi", "tools"} {
					if !slices.Contains(evidence.Fields, progress.Field{Label: "Bound output", Value: name}) {
						t.Fatal("filtered view dropped shared evidence membership")
					}
				}
				if !slices.Equal(evidence.Dependencies, []string{"output/" + test.name}) {
					t.Fatal("filtered evidence links a hidden output")
				}
			}
			raw, err := releaseview.Render(publication, releaseview.Options{Format: "json", Output: test.name})
			if err != nil {
				t.Fatal(err)
			}
			var description releaseview.Description
			if err := json.Unmarshal(raw, &description); err != nil {
				t.Fatal(err)
			}
			exported := releaseDescriptionDisplay(description)
			exported.Title = display.Title
			if !reflect.DeepEqual(display, exported) {
				t.Fatal("browse and export TUI metadata differ")
			}
		})
	}
}

func TestReleaseBrowserAutoPreservesExplicitExports(t *testing.T) {
	t.Setenv("CI", "")
	fixture := releaseBrowsePayloadFixture(t, "arm64")
	inspection, err := delivery.Inspect(t.Context(), fixture.layout)
	if err != nil {
		t.Fatal(err)
	}
	reference := "registry.example.test/releases@" + inspection.RootDigest
	description, err := lifecycle.InspectRelease(t.Context(), reference, lifecycle.InspectionOptions{LayoutPath: fixture.layout})
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"json", "mermaid"} {
		t.Run(format, func(t *testing.T) {
			services := runPresentationServices{eligible: func(io.Writer) bool { return true }, show: func(context.Context, runpresenter.Options, runSnapshotSource, func(context.Context) error) runpresenter.Result {
				t.Fatal("explicit export entered TUI")
				return runpresenter.Result{}
			}}
			var out bytes.Buffer
			app := releasePresentationTestApp(executeReleaseVerification, &out, services)
			err := app.RunContext(t.Context(), []string{"nvkr", "release", "browse", "--reference", reference, "--layout", fixture.layout, "--format", format, "--ui=auto"})
			if err != nil {
				t.Fatal(err)
			}
			want, err := releaseview.Render(description, releaseview.Options{Format: format})
			if err != nil || !bytes.Equal(out.Bytes(), want) {
				t.Fatalf("explicit export bytes changed: %v", err)
			}
		})
	}
}

func TestReleaseBrowserUnknownSelectionHasNoPartialResult(t *testing.T) {
	display, err := releasePublicationDisplay(delivery.PublicationDescription{}, "missing")
	assertLifecycleCommandCode(t, err, e.CodeUsage)
	if !reflect.DeepEqual(display, commandDisplay{}) {
		t.Fatal("failed selection returned a partial result")
	}
}
