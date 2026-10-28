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
	"strconv"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/runpresenter"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/releaseview"
	"github.com/urfave/cli/v2"
)

func releasePresentationTestApp(verify releaseVerifyOperation, out io.Writer, services runPresentationServices) *cli.App {
	command := cmdReleaseWithVerify(verify)
	configureCommandUsage(command)
	return &cli.App{Writer: out, Commands: []*cli.Command{command}, Metadata: map[string]interface{}{commandPresentationServicesKey: services}}
}

func releasePresentationVerifyArgs(flags ...string) []string {
	return append([]string{"nvkr", "release", "verify", "--reference", "registry.example.test/private-transport-sentinel@sha256:" + strings.Repeat("a", 64),
		"--lane", "pipeline-attested", "--scope", "artifact-publication", "--identity", "^private-identity-sentinel$", "--issuer", "private-issuer-sentinel"}, flags...)
}

func releasePresentationFixtureResult(exact bool) releaseVerificationResult {
	digest := "sha256:" + strings.Repeat("a", 64)
	result := releaseVerificationResult{Release: lifecycle.Release{SchemaVersion: lifecycle.ReleaseSchemaVersion,
		Subject: "registry.example.test/private-transport-sentinel@" + digest, Lane: lifecycle.LanePipeline, Scope: lifecycle.ScopeArtifactPublication,
		ContentSubject: digest, PolicyDigest: digest, Artifacts: []lifecycle.Artifact{{Name: "cli", Kind: lifecycle.OutputExecutable,
			Digest: digest, BuildDigest: digest, Reference: "private-artifact-reference-sentinel", Components: []string{"cli"}}}}}
	if exact {
		result.ExactInputs = &lifecycle.ExactInputDescription{ReleaseSubject: "private-exact-reference-sentinel", PolicyDigest: digest,
			DefinitionDigest: digest, EnvironmentDigest: digest, PlanDigest: digest, ContentSubject: digest}
	}
	return result
}

func assertReleasePresentationSafe(t *testing.T, snapshot progress.Snapshot) {
	t.Helper()
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private-transport-sentinel", "private-artifact-reference-sentinel", "private-exact-reference-sentinel", "private-identity-sentinel", "private-issuer-sentinel", "private-owner-error-sentinel", "private-evidence-reference-sentinel", "private-invocation-sentinel"} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("private value entered safe projection: %s", forbidden)
		}
	}
}

func TestReleasePresentationAdmissionPrecedesOwner(t *testing.T) {
	for _, test := range []struct {
		name     string
		args     []string
		eligible bool
	}{
		{"verify unknown UI", releasePresentationVerifyArgs("--ui=unknown"), true},
		{"verify redirected TUI", releasePresentationVerifyArgs("--ui=tui"), false},
		{"inspect unknown UI", []string{"nvkr", "release", "inspect", "--reference", "missing", "--layout", "missing", "--ui=unknown"}, true},
		{"browse JSON conflict", []string{"nvkr", "release", "browse", "--reference", "missing", "--ui=tui", "--format=json"}, true},
		{"browse Mermaid conflict", []string{"nvkr", "release", "browse", "--reference", "missing", "--ui=tui", "--format=mermaid"}, true},
		{"export unknown UI", []string{"nvkr", "release", "export", "--reference", "missing", "--ui=unknown"}, true},
		{"export redirected TUI", []string{"nvkr", "release", "export", "--reference", "missing", "--ui=tui"}, false},
		{"export JSON conflict", []string{"nvkr", "release", "export", "--reference", "missing", "--ui=tui", "--format=json"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			called, shown := false, false
			verify := func(context.Context, string, lifecycle.VerificationPolicy, lifecycle.ConsumerOptions, *lifecycle.ExactInputExpectation, releaseExportOptions) (releaseVerificationResult, error) {
				called = true
				return releaseVerificationResult{}, nil
			}
			services := runPresentationServices{eligible: func(io.Writer) bool { return test.eligible }, show: func(context.Context, runpresenter.Options, runSnapshotSource, func(context.Context) error) runpresenter.Result {
				shown = true
				return runpresenter.Result{}
			}}
			var out bytes.Buffer
			err := releasePresentationTestApp(verify, &out, services).RunContext(t.Context(), test.args)
			assertLifecycleCommandCode(t, err, e.CodeUsage)
			if called || shown || out.Len() != 0 {
				t.Fatal("rejected presentation reached owner or display")
			}
		})
	}
}

func TestReleaseVerificationTUIProjectsOwningResultOnce(t *testing.T) {
	for _, exact := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "exact inputs"}[exact], func(t *testing.T) {
			calls, shows := 0, 0
			verify := func(_ context.Context, _ string, policy lifecycle.VerificationPolicy, _ lifecycle.ConsumerOptions, _ *lifecycle.ExactInputExpectation, _ releaseExportOptions) (releaseVerificationResult, error) {
				calls++
				if policy.Lane != lifecycle.LanePipeline || policy.Scope != lifecycle.ScopeArtifactPublication {
					t.Fatal("policy changed")
				}
				return releasePresentationFixtureResult(exact), nil
			}
			services := runPresentationServices{eligible: func(io.Writer) bool { return true }, show: func(_ context.Context, opts runpresenter.Options, source runSnapshotSource, work func(context.Context) error) runpresenter.Result {
				shows++
				if calls != 1 || work != nil || opts.Live {
					t.Fatal("result browser reran or retained owning operation")
				}
				snapshot := source.Snapshot()
				assertReleasePresentationSafe(t, snapshot)
				if snapshot.Mode != "command" || snapshot.Status != "complete" || !strings.Contains(snapshot.Note, "runtime health and deployment were not evaluated") {
					t.Fatalf("verification scope lost: %+v", snapshot)
				}
				fields := map[string]string{}
				for _, field := range snapshot.Fields {
					fields[field.Label] = field.Value
				}
				if fields["Accepted lane"] != lifecycle.LanePipeline || fields["Accepted scope"] != lifecycle.ScopeArtifactPublication {
					t.Fatal("exact accepted lane/scope lost")
				}
				want := 1
				if exact {
					want++
				}
				if len(snapshot.Items) != want {
					t.Fatalf("result membership differs: %d want %d", len(snapshot.Items), want)
				}
				return runpresenter.Result{}
			}}
			var out bytes.Buffer
			if err := releasePresentationTestApp(verify, &out, services).RunContext(t.Context(), releasePresentationVerifyArgs("--ui=tui")); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || shows != 1 || out.Len() != 0 {
				t.Fatal("TUI changed operation count or emitted default JSON")
			}
		})
	}
}

func TestReleaseVerificationTUIKeepsOwnerFailureCode(t *testing.T) {
	for _, code := range []e.Code{e.CodeSignatureInvalid, e.CodeTimeout, e.CodeEngineCapability, e.CodeGateUnmet} {
		t.Run(string(code), func(t *testing.T) {
			owner := e.New(code, "private-owner-error-sentinel")
			reporting := e.New(e.CodeInternal, "renderer failure")
			verify := func(context.Context, string, lifecycle.VerificationPolicy, lifecycle.ConsumerOptions, *lifecycle.ExactInputExpectation, releaseExportOptions) (releaseVerificationResult, error) {
				return releaseVerificationResult{}, owner
			}
			shows := 0
			services := runPresentationServices{eligible: func(io.Writer) bool { return true }, show: func(_ context.Context, _ runpresenter.Options, source runSnapshotSource, work func(context.Context) error) runpresenter.Result {
				shows++
				snapshot := source.Snapshot()
				assertReleasePresentationSafe(t, snapshot)
				if work != nil || snapshot.FailureCode != string(code) || snapshot.Status != "failed" || len(snapshot.Items) != 0 {
					t.Fatal("failure display invented accepted artifacts")
				}
				return runpresenter.Result{PresentationErr: reporting}
			}}
			var out bytes.Buffer
			err := releasePresentationTestApp(verify, &out, services).RunContext(t.Context(), releasePresentationVerifyArgs("--ui=tui"))
			assertLifecycleCommandCode(t, err, code)
			if shows != 1 || !errors.Is(err, owner) || !errors.Is(err, reporting) {
				t.Fatal("original owner or rendering cause lost")
			}
		})
	}
}

func TestReleaseVerificationPlainRetainsExactJSONBytes(t *testing.T) {
	for _, exact := range []bool{false, true} {
		result := releasePresentationFixtureResult(exact)
		for _, explicit := range []bool{false, true} {
			t.Run(map[bool]string{false: "normal", true: "exact"}[exact]+"/"+map[bool]string{false: "default", true: "plain"}[explicit], func(t *testing.T) {
				verify := func(context.Context, string, lifecycle.VerificationPolicy, lifecycle.ConsumerOptions, *lifecycle.ExactInputExpectation, releaseExportOptions) (releaseVerificationResult, error) {
					return result, nil
				}
				app := &cli.App{Commands: []*cli.Command{cmdReleaseWithVerify(verify)}}
				args := releasePresentationVerifyArgs()
				if explicit {
					args = append(args, "--ui=plain")
				}
				raw, err := runCLIWithCapturedStdout(t, app, args...)
				if err != nil {
					t.Fatal(err)
				}
				var expected []byte
				if exact {
					copy := result
					copy.SchemaVersion = releaseVerificationResultSchemaVersion
					expected, err = canonical.Marshal(copy)
				} else {
					expected, err = canonical.Marshal(result.Release)
				}
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(raw, append(expected, '\n')) {
					t.Fatal("plain output changed its exact canonical JSON contract")
				}
			})
		}
	}
}

func TestReleaseMetadataTUIUsesTypedSafeMembership(t *testing.T) {
	fixture := releaseBrowsePayloadFixture(t, "amd64")
	inspection, err := delivery.Inspect(t.Context(), fixture.layout)
	if err != nil {
		t.Fatal(err)
	}
	reference := "registry.example.test/private-transport-sentinel@" + inspection.RootDigest
	for _, command := range []string{"inspect", "browse", "export"} {
		for _, selection := range []string{"", "tools"} {
			if command != "browse" && selection != "" {
				continue
			}
			t.Run(command+"/"+selection, func(t *testing.T) {
				shows := 0
				displayMismatch := ""
				expectedNote := "payloads, signatures and policy were not verified"
				if command == "inspect" {
					expectedNote = "Local closure membership and payload integrity checked; signatures and policy were not verified"
				}
				services := runPresentationServices{eligible: func(io.Writer) bool { return true }, show: func(_ context.Context, _ runpresenter.Options, source runSnapshotSource, work func(context.Context) error) runpresenter.Result {
					shows++
					snapshot := source.Snapshot()
					assertReleasePresentationSafe(t, snapshot)
					if work != nil || !strings.Contains(snapshot.Note, expectedNote) {
						// Let the action return its original owner error before failing
						// this assertion, rather than masking a scratch/admission error.
						displayMismatch = snapshot.Note + " (" + snapshot.FailureCode + ")"
						return runpresenter.Result{}
					}
					raw, _ := json.Marshal(snapshot)
					if bytes.Contains(raw, []byte(fixture.layout)) {
						t.Fatal("layout path leaked")
					}
					outputs, evidence := 0, 0
					ids := map[string]bool{}
					for _, item := range snapshot.Items {
						ids[item.ID] = true
						if strings.HasPrefix(item.ID, "output/") {
							outputs++
						} else if strings.HasPrefix(item.ID, "evidence/") {
							evidence++
						}
						if selection != "" && item.Name == "node-image" {
							t.Fatal("output focus lost")
						}
					}
					want := 2
					if selection != "" {
						want = 1
					}
					if outputs != want || command != "inspect" && evidence != want {
						t.Fatalf("metadata membership differs: outputs=%d evidence=%d", outputs, evidence)
					}
					for _, item := range snapshot.Items {
						if command != "inspect" && len(item.Dependencies) == 0 {
							t.Fatalf("output/evidence cannot be drilled into: %s", item.ID)
						}
						for _, dependency := range item.Dependencies {
							if !ids[dependency] {
								t.Fatal("evidence dependency has no browse target")
							}
						}
					}
					return runpresenter.Result{}
				}}
				var out bytes.Buffer
				app := releasePresentationTestApp(executeReleaseVerification, &out, services)
				args := []string{"nvkr", "release", command, "--reference", reference, "--layout", fixture.layout, "--ui=tui"}
				if selection != "" {
					args = append(args, "--output", selection)
				}
				if err := app.RunContext(t.Context(), args); err != nil {
					t.Fatalf("owning %s operation failed: %v; display=%s", command, err, displayMismatch)
				}
				if displayMismatch != "" {
					t.Fatalf("result browser changed owning inspection scope: %s", displayMismatch)
				}
				if shows != 1 || out.Len() != 0 {
					t.Fatal("metadata TUI changed invocation or stdout")
				}
			})
		}
	}
}

func TestReleaseBrowseLinksOnlyExactVisibleEvidenceBindings(t *testing.T) {
	description := delivery.PublicationDescription{
		Outputs: []delivery.PublicationOutput{
			{Output: lifecycle.Artifact{Name: "a"}},
			{Output: lifecycle.Artifact{Name: "ab"}},
		},
		Evidence: []delivery.PublicationEvidence{
			{Name: "shared", Kind: "sbom", ManifestDigest: "manifest", BlobDigest: "blob", Binding: delivery.EvidenceBinding{Outputs: []string{"a", "ab"}, Stage: "build-output", SubjectKind: "file", SubjectDigest: "subject", Platform: "linux/arm64"}},
			{Name: "only-ab", Kind: "sbom", Binding: delivery.EvidenceBinding{Outputs: []string{"ab"}}},
		},
	}
	for _, selection := range []string{"", "a", "ab"} {
		t.Run("selection="+selection, func(t *testing.T) {
			browse, err := releasePublicationDisplay(description, selection)
			if err != nil {
				t.Fatal(err)
			}
			exported := releaseview.Description{TotalOutputs: len(description.Outputs)}
			for _, output := range description.Outputs {
				if selection == "" || selection == output.Name {
					exported.Outputs = append(exported.Outputs, releaseview.Output{Name: output.Name})
				}
			}
			for _, evidence := range description.Evidence {
				if selection == "" || slices.Contains(evidence.Binding.Outputs, selection) {
					exported.Evidence = append(exported.Evidence, releaseview.Evidence{Name: evidence.Name, Kind: evidence.Kind, ManifestDigest: evidence.ManifestDigest, BlobDigest: evidence.BlobDigest,
						Binding: releaseview.EvidenceBinding{Outputs: evidence.Binding.Outputs, Stage: evidence.Binding.Stage, SubjectKind: evidence.Binding.SubjectKind, SubjectDigest: evidence.Binding.SubjectDigest, Platform: evidence.Binding.Platform}})
				}
			}
			for _, display := range []commandDisplay{browse, releaseDescriptionDisplay(exported)} {
				items := map[string]progress.Item{}
				for _, item := range display.Items {
					items[item.ID] = item
				}
				for _, item := range display.Items {
					for _, dependency := range item.Dependencies {
						target, found := items[dependency]
						if !found || !slices.Contains(target.Dependencies, item.ID) {
							t.Fatalf("%s: nonreciprocal or invisible link: %s -> %s", display.Title, item.ID, dependency)
						}
					}
				}
				if selection != "ab" && !slices.Equal(items["output/a"].Dependencies, []string{"evidence/shared"}) {
					t.Fatalf("%s: a linked sibling evidence: %+v", display.Title, items["output/a"])
				}
				if selection == "a" {
					if len(items) != 2 || !slices.Equal(items["evidence/shared"].Dependencies, []string{"output/a"}) {
						t.Fatalf("%s: selected evidence linked hidden output: %+v", display.Title, items)
					}
				}
				shared := slices.IndexFunc(browse.Items, func(item progress.Item) bool { return item.ID == "evidence/shared" })
				if !slices.Equal(items["evidence/shared"].Fields, browse.Items[shared].Fields) {
					t.Fatalf("%s: browse/export evidence fields differ", display.Title)
				}
			}
		})
	}
}

func BenchmarkReleasePublicationProjection(b *testing.B) {
	var description delivery.PublicationDescription
	for index := 0; index < 64; index++ {
		description.Outputs = append(description.Outputs, delivery.PublicationOutput{Output: lifecycle.Artifact{Name: strconv.Itoa(index), Kind: "image", OS: "linux", Arch: "arm64"}})
	}
	for index := 0; index < 256; index++ {
		var outputs []string
		for offset := 0; offset < 8; offset++ {
			outputs = append(outputs, strconv.Itoa((index+offset)%len(description.Outputs)))
		}
		description.Evidence = append(description.Evidence, delivery.PublicationEvidence{Name: strconv.Itoa(index), Kind: "sbom", Binding: delivery.EvidenceBinding{Outputs: outputs, Stage: "build-output", Platform: "linux/arm64"}})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := releasePublicationDisplay(description, ""); err != nil {
			b.Fatal(err)
		}
	}
}

func TestReleaseBrowseAndExportDiskEvidenceDeclarationParity(t *testing.T) {
	for _, test := range []struct {
		name, mode, want string
	}{
		{name: "omitted inspected contract", mode: "", want: "inspected"},
		{name: "producer provenance", mode: "build-provenance-v1", want: "build-provenance-v1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			digest := "sha256:" + strings.Repeat("a", 64)
			artifact := lifecycle.Artifact{Name: "node-image", Kind: "disk-image", Digest: digest, BuildDigest: digest, Components: []string{"node"},
				Disk: &delivery.DiskOutput{EvidenceMode: test.mode, PayloadDigest: digest, Producer: "node-producer", Component: "node", Format: "qcow2", Encryption: "none", Size: 1, PlanDigest: digest, Outcome: "succeeded"}}
			if err := artifact.Validate(); err != nil {
				t.Fatalf("fixture does not satisfy delivery's owning disk contract: %v", err)
			}
			publication := delivery.PublicationDescription{Outputs: []delivery.PublicationOutput{{Output: artifact}}}
			description := releaseview.Description{TotalOutputs: 1, Outputs: []releaseview.Output{{Name: artifact.Name, Kind: artifact.Kind, Digest: artifact.Digest, BuildDigest: artifact.BuildDigest,
				Disk: &releaseview.Disk{EvidenceMode: test.mode}}}}
			browse, err := releasePublicationDisplay(publication, "")
			if err != nil {
				t.Fatal(err)
			}
			for _, display := range []commandDisplay{browse, releaseDescriptionDisplay(description)} {
				if len(display.Items) != 1 || display.Items[0].State != "metadata" || display.Note != "Metadata inspection only; payloads, signatures and policy were not verified." {
					t.Fatalf("disk declaration acquired verification or lost scope: %+v", display)
				}
				declared := ""
				for _, field := range display.Items[0].Fields {
					if field.Label == "Declared disk evidence" {
						declared = field.Value
					}
				}
				if declared != test.want {
					t.Fatalf("%s declaration = %q, want %q", display.Title, declared, test.want)
				}
			}
			if publication.Outputs[0].Disk.EvidenceMode != test.mode || description.Outputs[0].Disk.EvidenceMode != test.mode {
				t.Fatal("presentation changed the owning evidence-mode metadata")
			}
		})
	}
}

func TestReleaseBrowsePlainFormatsKeepExistingBytes(t *testing.T) {
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
	for _, format := range []string{"text", "json", "mermaid"} {
		t.Run(format, func(t *testing.T) {
			want, err := releaseview.Render(description, releaseview.Options{Format: format})
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			app := &cli.App{Writer: &out, Commands: []*cli.Command{cmdRelease()}}
			if err := app.RunContext(t.Context(), []string{"nvkr", "release", "browse", "--reference", reference, "--layout", fixture.layout, "--format", format, "--ui=plain"}); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out.Bytes(), want) {
				t.Fatal("plain browse bytes changed")
			}
		})
	}
}
