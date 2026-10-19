// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/runpresenter"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/releaseview"
	"github.com/urfave/cli/v2"
)

func TestReleaseExportCLIAdmission(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code e.Code
	}{
		{"consumer format", []string{"--format", "nvke-release"}, e.CodeUsage},
		{"timeout", []string{"--timeout", "0s"}, e.CodeUsage},
		{"negative timeout", []string{"--timeout", "-1s"}, e.CodeUsage},
		{"platform selection", []string{"--platform", "linux/amd64"}, e.CodeUsage},
		{"plan", []string{"--plan", "/must-not-open"}, e.CodeUsage},
		{"mapping", []string{"--mapping", "/must-not-open"}, e.CodeUsage},
		{"unpublished workspace", []string{"--workspace", "/must-not-open"}, e.CodeUsage},
		{"output selection", []string{"--output", "controller"}, e.CodeUsage},
		{"local registry options", []string{"--anonymous"}, e.CodeUsage},
		{"positional argument", []string{"unexpected"}, e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			app := newApp()
			app.Writer = &out
			args := []string{"nvkr", "release", "export", "--reference", "registry.example.test/releases@sha256:" + strings.Repeat("a", 64), "--layout", "/must-not-open"}
			err := app.RunContext(t.Context(), append(args, tc.args...))
			if cliErrorCode(err) != tc.code || out.Len() != 0 {
				t.Fatalf("invalid export options reached inspection or returned a description: %v, %q", err, out.String())
			}
		})
	}
}

func TestReleaseExportCLIReferenceOnlyAdmission(t *testing.T) {
	for _, reference := range []string{"", "registry.example.test/releases:latest", "registry.example.test/releases@sha256:bad"} {
		t.Run(reference, func(t *testing.T) {
			var out bytes.Buffer
			app := newApp()
			app.Writer = &out
			err := app.RunContext(t.Context(), []string{"nvkr", "release", "export", "--reference", reference})
			if cliErrorCode(err) != e.CodeUnverifiedRef || out.Len() != 0 {
				t.Fatalf("reference-only export should reach immutable-reference admission without other required flags: %v, %q", err, out.String())
			}
		})
	}
}

func TestReleaseExportCLIMatchesCompleteBrowseJSON(t *testing.T) {
	fixture := releaseBrowsePayloadFixture(t, "arm64")
	inspection, err := delivery.Inspect(t.Context(), fixture.layout)
	if err != nil {
		t.Fatal(err)
	}
	reference := "registry.example.test/product/releases@" + inspection.RootDigest
	var exported []byte
	for _, command := range []string{"export", "browse"} {
		var out bytes.Buffer
		app := newApp()
		app.Writer = &out
		args := []string{"nvkr", "release", command, "--reference", reference, "--layout", fixture.layout}
		if command == "browse" {
			args = append(args, "--format", "json")
		}
		if err := app.RunContext(t.Context(), args); err != nil {
			t.Fatal(err)
		}
		var document struct {
			SchemaVersion string `json:"schemaVersion"`
			TotalOutputs  int    `json:"totalOutputs"`
			Outputs       []struct {
				Name string `json:"name"`
			} `json:"outputs"`
		}
		if err := json.Unmarshal(out.Bytes(), &document); err != nil {
			t.Fatal(err)
		}
		if document.SchemaVersion != "nvkr.dev/release-description/v1" || document.TotalOutputs != 2 || len(document.Outputs) != 2 ||
			document.Outputs[0].Name != "node-image" || document.Outputs[1].Name != "tools" {
			t.Fatalf("%s omitted non-NVKE outputs or versioned schema: %s", command, out.String())
		}
		if command == "export" {
			exported = bytes.Clone(out.Bytes())
		} else if !bytes.Equal(exported, out.Bytes()) {
			t.Fatal("export and unfiltered JSON browsing disagree on the release description")
		}
	}
}

func TestReleaseExportPresentationPreservesSingleOperationAndWireBytes(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	description := releaseview.Description{SchemaVersion: releaseview.DescriptionSchemaVersion,
		Reference: "private-transport-sentinel", RootDigest: digest, TotalOutputs: 2,
		Metadata: releaseview.Metadata{InvocationID: "private-invocation-sentinel"},
		Outputs: []releaseview.Output{
			{Name: "aggregate", Kind: "container-image", Digest: digest, Reference: "private-artifact-reference-sentinel", ImageIndex: &releaseview.ImageIndex{
				RequiredPlatforms: []string{"linux/amd64", "linux/arm64"}, Children: []releaseview.ImageIndexChild{{Platform: "linux/amd64", ManifestDigest: digest}, {Platform: "linux/arm64", ManifestDigest: digest}}}},
			{Name: "tools", Kind: "executable", Digest: digest, OS: "linux", Arch: "arm64"},
		},
		Evidence: []releaseview.Evidence{{Name: "inventory", Reference: "private-evidence-reference-sentinel", Binding: releaseview.EvidenceBinding{Outputs: []string{"aggregate"}, Platform: "linux/arm64"}}},
	}
	raw, err := json.MarshalIndent(description, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	for _, mode := range []string{"default", "plain", "auto JSON", "tui"} {
		t.Run(mode, func(t *testing.T) {
			calls, shows := 0, 0
			operation := func(_ context.Context, reference string, options lifecycle.InspectionOptions) ([]byte, error) {
				calls++
				if reference != "registry.example.test/releases@"+digest || options.Timeout != 23*time.Second || options.LayoutPath != "private-layout-sentinel" {
					t.Fatalf("export inputs changed: %s %+v", reference, options)
				}
				return raw, nil
			}
			services := runPresentationServices{eligible: func(io.Writer) bool { return true }, show: func(_ context.Context, options runpresenter.Options, source runSnapshotSource, work func(context.Context) error) runpresenter.Result {
				shows++
				if calls != 1 || options.Live || work != nil {
					t.Fatal("export browser owns or repeats work")
				}
				snapshot := source.Snapshot()
				assertReleasePresentationSafe(t, snapshot)
				if snapshot.Status != "complete" || !strings.Contains(snapshot.Note, "payloads, signatures and policy were not verified") || len(snapshot.Items) != 3 {
					t.Fatalf("export membership or scope changed: %+v", snapshot)
				}
				for _, item := range snapshot.Items {
					if item.ID == "output/aggregate" {
						platforms, children := []string{}, []string{}
						for _, field := range item.Fields {
							if field.Label == "Required platform" {
								platforms = append(platforms, field.Value)
							}
							if field.Label == "Child platform" {
								children = append(children, field.Value)
							}
						}
						if !slices.Equal(platforms, []string{"linux/amd64", "linux/arm64"}) || !slices.Equal(children, platforms) || !slices.Equal(item.Dependencies, []string{"evidence/inventory"}) {
							t.Fatalf("aggregate platform membership or drill-down changed: %+v", item)
						}
					}
					if item.ID == "evidence/inventory" && !slices.Equal(item.Dependencies, []string{"output/aggregate"}) {
						t.Fatal("evidence linked an unbound sibling")
					}
				}
				return runpresenter.Result{}
			}}
			var out bytes.Buffer
			app := &cli.App{Writer: &out, Commands: []*cli.Command{{Name: "release", Subcommands: []*cli.Command{releaseExportCmdWithOperation(operation)}}}, Metadata: map[string]interface{}{commandPresentationServicesKey: services}}
			args := []string{"nvkr", "release", "export", "--reference", "registry.example.test/releases@" + digest, "--layout", "private-layout-sentinel", "--timeout", "23s"}
			switch mode {
			case "plain":
				args = append(args, "--ui=plain")
			case "auto JSON":
				args = append(args, "--ui=auto", "--format=json")
			case "tui":
				args = append(args, "--ui=tui")
			}
			if err := app.RunContext(t.Context(), args); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || mode == "tui" && (shows != 1 || out.Len() != 0) || mode != "tui" && (shows != 0 || !bytes.Equal(out.Bytes(), raw)) {
				t.Fatalf("export operation count or exact output contract changed: calls=%d shows=%d stdout=%q", calls, shows, out.String())
			}
		})
	}
}

func TestReleaseExportPresentationKeepsOwningFailure(t *testing.T) {
	for _, mode := range []string{"tui", "plain"} {
		t.Run(mode, func(t *testing.T) {
			ownerErr := e.New(e.CodeTimeout, "private-owner-error-sentinel")
			shows := 0
			operation := func(context.Context, string, lifecycle.InspectionOptions) ([]byte, error) { return nil, ownerErr }
			services := runPresentationServices{eligible: func(io.Writer) bool { return true }, show: func(_ context.Context, _ runpresenter.Options, source runSnapshotSource, _ func(context.Context) error) runpresenter.Result {
				shows++
				snapshot := source.Snapshot()
				assertReleasePresentationSafe(t, snapshot)
				if snapshot.FailureCode != string(e.CodeTimeout) || len(snapshot.Items) != 0 {
					t.Fatal("failed export invented membership or changed code")
				}
				return runpresenter.Result{}
			}}
			var out bytes.Buffer
			app := &cli.App{Writer: &out, Commands: []*cli.Command{{Name: "release", Subcommands: []*cli.Command{releaseExportCmdWithOperation(operation)}}}, Metadata: map[string]interface{}{commandPresentationServicesKey: services}}
			err := app.RunContext(t.Context(), []string{"nvkr", "release", "export", "--reference", "unopened", "--ui=" + mode})
			if !errors.Is(err, ownerErr) || cliErrorCode(err) != e.CodeTimeout || out.Len() != 0 || mode == "tui" && shows != 1 || mode == "plain" && shows != 0 {
				t.Fatalf("owner error changed: %v shows=%d stdout=%q", err, shows, out.String())
			}
		})
	}
}

func TestReleaseExportPresentationAdmissionBeforeOperation(t *testing.T) {
	for _, test := range []struct {
		name     string
		flags    []string
		eligible bool
	}{
		{"unknown UI", []string{"--ui=unknown"}, true},
		{"redirected TUI", []string{"--ui=tui"}, false},
		{"explicit JSON", []string{"--ui=tui", "--format=json"}, true},
		{"invalid format", []string{"--ui=tui", "--format=yaml"}, true},
		{"invalid timeout", []string{"--ui=tui", "--timeout=0s"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			operation := func(context.Context, string, lifecycle.InspectionOptions) ([]byte, error) {
				t.Fatal("rejected presentation started export")
				return nil, nil
			}
			services := runPresentationServices{eligible: func(io.Writer) bool { return test.eligible }, show: func(context.Context, runpresenter.Options, runSnapshotSource, func(context.Context) error) runpresenter.Result {
				t.Fatal("rejected presentation opened a browser")
				return runpresenter.Result{}
			}}
			var out bytes.Buffer
			app := &cli.App{Writer: &out, Commands: []*cli.Command{{Name: "release", Subcommands: []*cli.Command{releaseExportCmdWithOperation(operation)}}}, Metadata: map[string]interface{}{commandPresentationServicesKey: services}}
			args := append([]string{"nvkr", "release", "export", "--reference", "unopened"}, test.flags...)
			if err := app.RunContext(t.Context(), args); cliErrorCode(err) != e.CodeUsage || out.Len() != 0 {
				t.Fatalf("rejected presentation changed code/output: %v %q", err, out.String())
			}
		})
	}
}

func TestReleaseExportPlainWritePreservesExistingContract(t *testing.T) {
	for _, short := range []bool{false, true} {
		t.Run(map[bool]string{false: "write error", true: "short write"}[short], func(t *testing.T) {
			operation := func(context.Context, string, lifecycle.InspectionOptions) ([]byte, error) {
				return []byte("description\n"), nil
			}
			app := &cli.App{Writer: failingRunWriter{err: io.ErrClosedPipe, short: short}, Commands: []*cli.Command{{Name: "release", Subcommands: []*cli.Command{releaseExportCmdWithOperation(operation)}}}}
			err := app.RunContext(t.Context(), []string{"nvkr", "release", "export", "--reference", "unopened", "--ui=plain"})
			if short {
				if err != nil {
					t.Fatal("plain export changed its existing short-write behavior", err)
				}
			} else if cliErrorCode(err) != e.CodeUnavailable || !errors.Is(err, io.ErrClosedPipe) {
				t.Fatalf("plain write lost original code or cause: %v", err)
			}
		})
	}
}

func TestReleaseExportTUIRejectsDescriptionSchemaDrift(t *testing.T) {
	for _, raw := range []string{
		`{"schemaVersion":"nvkr.dev/release-description/v1","private-unknown-field":"secret"}`,
		`{"schemaVersion":"first","schemaVersion":"second"}`,
		`{} {}`,
		`{"outputs":"private-type-error"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			calls, shows := 0, 0
			operation := func(context.Context, string, lifecycle.InspectionOptions) ([]byte, error) {
				calls++
				return []byte(raw), nil
			}
			services := runPresentationServices{eligible: func(io.Writer) bool { return true }, show: func(_ context.Context, _ runpresenter.Options, source runSnapshotSource, _ func(context.Context) error) runpresenter.Result {
				shows++
				if source.Snapshot().FailureCode != string(e.CodeInternal) || len(source.Snapshot().Items) != 0 {
					t.Error("invalid description invented result membership")
				}
				return runpresenter.Result{}
			}}
			var out bytes.Buffer
			app := &cli.App{Writer: &out, Commands: []*cli.Command{{Name: "release", Subcommands: []*cli.Command{releaseExportCmdWithOperation(operation)}}}, Metadata: map[string]interface{}{commandPresentationServicesKey: services}}
			err := app.RunContext(t.Context(), []string{"nvkr", "release", "export", "--reference", "unopened", "--ui=tui"})
			if cliErrorCode(err) != e.CodeInternal || calls != 1 || shows != 1 || out.Len() != 0 || strings.Contains(err.Error(), "private-") {
				t.Fatalf("schema drift was ignored, leaked, or repeated the owner: %v", err)
			}
		})
	}
}
