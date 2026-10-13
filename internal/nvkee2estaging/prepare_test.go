// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkee2estaging

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

const testDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const testCommit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func requireCode(t *testing.T, err error, code e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func testInput(t *testing.T, root string) mapping {
	t.Helper()
	fixturePath := filepath.Join(root, "fixture")
	if err := os.WriteFile(fixturePath, []byte("bounded fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	digest, err := fileDigest(t.Context(), fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var result mapping
	result.SuiteConfig = map[string]json.RawMessage{"site": json.RawMessage(`{"deployPathRef":"` + testCommit + `"}`)}
	result.Bindings.CLI = binding{Output: "cli", InstallPath: "/usr/local/bin/nvkectl"}
	result.Bindings.Images = map[string]binding{}
	result.Bindings.Charts = map[string]binding{}
	for _, role := range imageRoles {
		result.Bindings.Images[role] = binding{Fixture: &fixture{Identity: "reviewed fixture", Path: fixturePath, SHA256: digest}, Repository: "registry.example.test/" + role, Digest: testDigest}
	}
	for _, role := range chartRoles {
		result.Bindings.Charts[role] = binding{Fixture: &fixture{Identity: "reviewed fixture", Path: fixturePath, SHA256: digest}}
	}
	return result
}

func writeTestJSON(t *testing.T, filename string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func runDocument() map[string]any {
	return map[string]any{"schemaVersion": lifecycle.RunSchemaVersion, "attempt": "123-2", "status": "complete", "lane": lifecycle.LanePipeline,
		"definitionDigest": testDigest, "environmentDigest": testDigest, "planDigest": testDigest, "contentSubject": testDigest,
		"expected": []map[string]any{{"name": "cli", "kind": lifecycle.OutputExecutable, "buildDigest": testDigest, "components": []string{"cli"}}},
		"observed": []map[string]any{{"name": "cli", "kind": lifecycle.OutputExecutable, "buildDigest": testDigest, "components": []string{"cli"}, "digest": testDigest, "reference": "registry.example.test/cli@" + testDigest}}}
}

func testOptions(t *testing.T) (Options, mapping) {
	t.Helper()
	root := t.TempDir()
	input := testInput(t, root)
	options := Options{Run: filepath.Join(root, "run.json"), Plan: filepath.Join(root, "unused-plan.json"), Definition: filepath.Join(root, "unused-definition.yaml"), Environment: filepath.Join(root, "unused-environment.yaml"), Inputs: filepath.Join(root, "inputs.json"), Out: filepath.Join(root, "cycle"), Repository: "example/nvkr", RunID: "123", RunAttempt: "2", Reference: "registry.example.test/release@" + testDigest, SourceNode: "exact-fetch", StreamName: "forge-gb200-amd64-ubuntu-24.04-kube-1.36", GPU: "gb200", Arch: "amd64", Ubuntu: "24.04", K8s: "1.36", Versions: "os-image=pinned", PlanOnly: true}
	writeTestJSON(t, options.Run, runDocument())
	writeTestJSON(t, options.Inputs, input)
	return options, input
}

func TestPlanOnlyDescribesFrozenRunWithoutSourceOrVerifier(t *testing.T) {
	options, _ := testOptions(t)
	// Plan, Definition, Environment, source and verification tools are absent.
	report, err := Prepare(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if report.RunRecordTrust != "run-record-unauthenticated" || report.SourceTrust != "not-checked" || report.ValidationOutcome != "input-descriptions-checked" || report.CompilationOutcome != "not-run" {
		t.Fatalf("plan-only trust overclaimed: %+v", report)
	}
	if artifact, ok := report.Artifacts["cli"].(map[string]any); !ok || artifact["trust"] != "run-record-unauthenticated" {
		t.Fatal("unauthenticated Run description was labeled as verified")
	}
	if report.LiveCommandAdmitted || report.QualificationAuthority != "none" || report.MaterializationOutcome != "not-run" || report.TestOutcome != "not-run" || report.CleanupOutcome != "not-run" {
		t.Fatalf("overclaimed report: %+v", report)
	}
	if !slices.Contains(report.GoTestCommand, "-ginkgo.fail-on-empty") || !slices.Contains(report.GoTestCommand, "-e2e.keep-site=false") {
		t.Fatal("descriptive suite command loses safety flags")
	}
	var persisted Report
	if err := readDocument(t.Context(), filepath.Join(options.Out, "e2e-staging.json"), &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.InputIdentities["planDigest"] != testDigest || persisted.Source.Commit != "not-inspected" {
		t.Fatal("plan-only fabricated source inspection")
	}
	_, err = Prepare(t.Context(), options)
	requireCode(t, err, e.CodeImmutableViolation)
	if _, err := os.Stat(filepath.Join(options.Out, "e2e-staging-failure.json")); !os.IsNotExist(err) {
		t.Fatal("rejected reuse changed existing output")
	}
}

func TestAdmissionBeforeOutput(t *testing.T) {
	for _, scenario := range []string{"nil context", "canceled", "compile plan", "unsupported stream", "invalid origin", "invalid run id", "stage platform"} {
		t.Run(scenario, func(t *testing.T) {
			options, _ := testOptions(t)
			ctx := t.Context()
			code := e.CodeUsage
			switch scenario {
			case "nil context":
				ctx = nil
			case "canceled":
				bounded, cancel := context.WithCancel(ctx)
				cancel()
				ctx = bounded
				code = e.CodeTimeout
			case "compile plan":
				options.CompileOnly = true
			case "unsupported stream":
				options.GPU = "unknown"
				code = e.CodeEngineCapability
			case "invalid origin":
				options.GitHubServerURL = "https://user:private@example.test"
			case "invalid run id":
				options.RunID = "0"
			case "stage platform":
				if runtime.GOOS == "linux" {
					t.Skip("non-Linux admission contract")
				}
				options.PlanOnly = false
				code = e.CodeEngineCapability
			}
			_, err := Prepare(ctx, options)
			requireCode(t, err, code)
			if _, err := os.Lstat(options.Out); !os.IsNotExist(err) {
				t.Fatal("rejected admission created output")
			}
		})
	}
}

func TestProtectedConfigRejectsGoCaseAliasesAndRetention(t *testing.T) {
	for _, config := range []string{
		`{"site":{"deployPathRef":"` + testCommit + `"},"Lifecycle":{"skipDelete":true}}`,
		`{"site":{"deployPathRef":"` + testCommit + `"},"lifecycle":{"skipdelete":true}}`,
		`{"site":{"deployPathRef":"` + testCommit + `"},"lifecycle":{"ſkipDelete":true}}`,
		`{"site":{"deployPathRef":"` + testCommit + `"},"ſite":{"deployPathRef":"main"}}`,
		`{"site":{"deployPathRef":"` + testCommit + `","deploypathref":"main"}}`,
		`{"site":{"deployPathRef":"` + testCommit + `"},"lifecycle":{"reuseCluster":"existing"}}`,
		`{"site":{"deployPathRef":"` + testCommit + `"},"lifecycle":{"preserveOnFailure":true}}`,
		`{"site":{"deployPathRef":"` + testCommit + `"},"nvkectl":[]}`, `{"site":null}`, `{"site":{"deployPathRef":"main"}}`,
	} {
		t.Run(config, func(t *testing.T) {
			options, input := testOptions(t)
			if err := json.Unmarshal([]byte(config), &input.SuiteConfig); err != nil {
				t.Fatal(err)
			}
			writeTestJSON(t, options.Inputs, input)
			_, err := Prepare(t.Context(), options)
			requireCode(t, err, e.CodeUsage)
			if _, err := os.Lstat(options.Out); !os.IsNotExist(err) {
				t.Fatal("unsafe config created cycle")
			}
		})
	}
}

func TestDuplicateDocumentsRejectedByOwningCodec(t *testing.T) {
	for _, raw := range []string{`{"site":{},"site":{}}`, `{"site":{"deployPathRef":"a","deployPathRef":"b"}}`} {
		t.Run(raw, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "duplicate.json")
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			var value any
			requireCode(t, readDocument(t.Context(), path, &value), e.CodeVerifyFailed)
		})
	}
}

func TestRunMembershipFailuresBeforeOutput(t *testing.T) {
	for _, scenario := range []string{"attempt", "missing", "duplicated", "substituted"} {
		t.Run(scenario, func(t *testing.T) {
			options, _ := testOptions(t)
			run := runDocument()
			observed := run["observed"].([]map[string]any)
			switch scenario {
			case "attempt":
				run["attempt"] = "123-3"
			case "missing":
				run["observed"] = []any{}
			case "duplicated":
				run["observed"] = append(observed, observed[0])
			case "substituted":
				observed[0]["kind"] = "oci"
			case "failed":
				run["status"] = "failed"
			}
			writeTestJSON(t, options.Run, run)
			_, err := Prepare(t.Context(), options)
			requireCode(t, err, e.CodeVerifyFailed)
			if _, err := os.Stat(options.Out); !os.IsNotExist(err) {
				t.Fatal("invalid Run created output")
			}
		})
	}
}

func TestFixtureSnapshotNoFollowPinAndNoReplace(t *testing.T) {
	for _, scenario := range []string{"copy", "symlink", "bad pin", "existing", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			input := testInput(t, root).Bindings.Images["profiles"].Fixture
			out := filepath.Join(root, "cycle")
			if err := os.Mkdir(out, 0700); err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			switch scenario {
			case "symlink":
				link := filepath.Join(root, "link")
				if err := os.Symlink(input.Path, link); err != nil {
					t.Fatal(err)
				}
				input.Path = link
			case "bad pin":
				input.SHA256 = testDigest
			case "existing":
				if _, err := copyFixture(ctx, out, "profiles", *input); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				bounded, cancel := context.WithCancel(ctx)
				cancel()
				ctx = bounded
			}
			path, err := copyFixture(ctx, out, "profiles", *input)
			if scenario == "copy" {
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(input.Path, []byte("operator replaced original"), 0600); err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(path)
				if err != nil || string(raw) != "bounded fixture" {
					t.Fatal("suite copy changed with original")
				}
				return
			}
			code := e.CodeVerifyFailed
			if scenario == "existing" {
				code = e.CodeImmutableViolation
			}
			if scenario == "canceled" {
				code = e.CodeTimeout
			}
			requireCode(t, err, code)
			entries, err := os.ReadDir(filepath.Join(out, "fixtures"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".nvkr-") {
					t.Fatal("failed copy leaked temporary")
				}
			}
		})
	}
}

func TestBundleEntryUsesFlatNameAndExactInstallPath(t *testing.T) {
	for _, scenario := range []string{"valid", "missing", "duplicated", "unsafe", "not executable", "wrong platform"} {
		t.Run(scenario, func(t *testing.T) {
			bundle := delivery.BinaryBundle{OS: "linux", Arch: "amd64", Entries: []delivery.BinaryEntry{{Name: "nvkectl", InstallPath: "/usr/local/bin/nvkectl", Mode: 0o755}}}
			switch scenario {
			case "missing":
				bundle.Entries = nil
			case "duplicated":
				bundle.Entries = append(bundle.Entries, bundle.Entries[0])
			case "unsafe":
				bundle.Entries[0].Name = "../outside"
			case "not executable":
				bundle.Entries[0].Mode = 0o644
			case "wrong platform":
				bundle.Arch = "unsupported"
			}
			path, err := bundleExecutable(bundle, "/usr/local/bin/nvkectl", "/private/cycle/binaries", "amd64")
			if scenario != "valid" {
				code := e.CodeVerifyFailed
				if scenario == "wrong platform" {
					code = e.CodeEngineCapability
				}
				requireCode(t, err, code)
				return
			}
			if err != nil || path != "/private/cycle/binaries/nvkectl" {
				t.Fatalf("flat selected path: %s %v", path, err)
			}
		})
	}
}

func TestEnterpriseOriginAndCompileEnvironment(t *testing.T) {
	options, _ := testOptions(t)
	options.GitHubServerURL = "https://github.example.test/"
	value, err := invocation(options)
	if err != nil || value != "https://github.example.test/example/nvkr/actions/runs/123/attempts/2" {
		t.Fatalf("origin: %s %v", value, err)
	}
	for _, origin := range []string{"http://github.example.test", "https://user:secret@example.test", "https://example.test/path", "https://example.test?query", "https://example.test#fragment"} {
		options.GitHubServerURL = origin
		_, err := invocation(options)
		requireCode(t, err, e.CodeUsage)
	}
	env := compileEnvironment([]string{"PATH=/go/bin", "GOCACHE=/cache", "SOURCE_TOKEN=private", "FORGE_API_KEY=private", "COSIGN_TOKEN=private", "GOFLAGS=-mod=mod", "GOWORK=/external/go.work", "GOENV=/external/go.env", "GOPROXY=https://external.test", "GOTOOLCHAIN=auto", "CGO_ENABLED=1"})
	if !slices.Equal(env, []string{"PATH=/go/bin", "GOCACHE=/cache", "GOWORK=off", "GOENV=off", "GOFLAGS=-mod=vendor", "GOPROXY=off", "GOTOOLCHAIN=local", "CGO_ENABLED=0"}) {
		t.Fatalf("compile credential isolation: %v", env)
	}
}

func TestMaterialSelectionUsesVerifiedDescriptionsInsteadOfRunLocators(t *testing.T) {
	options, _ := testOptions(t)
	identities := map[string]string{"definitionDigest": testDigest, "environmentDigest": testDigest, "planDigest": testDigest, "contentSubject": testDigest}
	exact := lifecycle.ExactInputDescription{ReleaseSubject: options.Reference, DefinitionDigest: testDigest, EnvironmentDigest: testDigest, PlanDigest: testDigest, ContentSubject: testDigest}
	accepted := lifecycle.Artifact{Name: "controller", Kind: lifecycle.OutputOCI, Digest: testDigest, BuildDigest: testDigest, Reference: "registry.example.test/accepted@" + testDigest, OS: "linux", Arch: "amd64"}
	raw := accepted
	raw.Reference = "another.example.test/untrusted@" + testDigest
	raw.Arch = "arm64"
	raw.ImageIndex = &delivery.ImageIndexBinding{}
	release := lifecycle.Release{Subject: options.Reference, Artifacts: []lifecycle.Artifact{accepted}}
	material, err := checkedArtifacts(map[string]lifecycle.Artifact{raw.Name: raw}, release, identities, exact, options.Reference)
	if err != nil {
		t.Fatal(err)
	}
	if material[accepted.Name].Reference != accepted.Reference || material[accepted.Name].Arch != accepted.Arch || material[accepted.Name].ImageIndex != nil {
		t.Fatal("Run metadata replaced verified material description")
	}
	for _, scenario := range []string{"wrong origin", "missing output", "wrong content"} {
		t.Run(scenario, func(t *testing.T) {
			description := release
			claim := exact
			switch scenario {
			case "wrong origin":
				description.Subject = "registry.example.test/other@" + testDigest
			case "missing output":
				description.Artifacts = nil
			case "wrong content":
				claim.ContentSubject = "sha256:" + strings.Repeat("c", 64)
			}
			_, err := checkedArtifacts(map[string]lifecycle.Artifact{raw.Name: raw}, description, identities, claim, options.Reference)
			requireCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func TestCompileOnlyNeverExecutesTestMain(t *testing.T) {
	t.Setenv("GOFLAGS", "-invalid-parent-flag")
	t.Setenv("GOWORK", "/unowned/external/go.work")
	t.Setenv("GOTOOLCHAIN", "go999.0.0")
	t.Setenv("GOPROXY", "https://unowned.invalid")
	root := t.TempDir()
	source := filepath.Join(root, "source")
	suite := filepath.Join(source, "controller", "test", "e2e")
	if err := os.MkdirAll(suite, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suite, "go.mod"), []byte("module test.example/compileonly\n\ngo 1.27\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "test-main-executed")
	code := fmt.Sprintf("package suite\nimport (\"os\";\"testing\")\nfunc TestMain(m *testing.M){ _ = os.WriteFile(%q, []byte(\"executed\"), 0600);os.Exit(m.Run())}\nfunc TestNothing(t *testing.T) {}\n", marker)
	if err := os.WriteFile(filepath.Join(suite, "suite_test.go"), []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "cycle")
	if err := os.Mkdir(out, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := compileSource(t.Context(), source, Options{Out: out, Diagnostics: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("compile-only executed TestMain")
	}
	if info, err := os.Stat(filepath.Join(out, "nvke-e2e.test")); err != nil || !info.Mode().IsRegular() {
		t.Fatal("compilation did not retain its binary")
	}
}

// Exercise actual publication/extraction owners with a diagnostic fixture.
// This test does not bypass Prepare's public authentication boundary or claim
// signature/registry/runtime acceptance.
func TestMaterializationUsesRealCLIAndHelmOwners(t *testing.T) {
	root := t.TempDir()
	input := testInput(t, root)
	binary := filepath.Join(root, "nvkectl")
	if err := os.WriteFile(binary, []byte("exact candidate CLI"), 0755); err != nil {
		t.Fatal(err)
	}
	cliLayout := filepath.Join(root, "cli-layout")
	cliResult, err := delivery.BuildBinaryBundle(t.Context(), cliLayout, delivery.BinaryBundleRequest{OS: "linux", Arch: "amd64", Purpose: "installation", Files: []delivery.BinaryFileInput{{Name: "nvkectl", Path: binary, InstallPath: "/usr/local/bin/nvkectl", Mode: 0755}}})
	if err != nil {
		t.Fatal(err)
	}
	var chartBuffer bytes.Buffer
	zipper := gzip.NewWriter(&chartBuffer)
	writer := tar.NewWriter(zipper)
	chartMetadata := []byte("apiVersion: v2\nname: candidate\nversion: 1.2.3\n")
	if err := writer.WriteHeader(&tar.Header{Name: "candidate/Chart.yaml", Mode: 0644, Size: int64(len(chartMetadata)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(chartMetadata); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zipper.Close(); err != nil {
		t.Fatal(err)
	}
	chartLayout := filepath.Join(root, "chart-layout")
	chartType := "application/vnd.nvkr.helm-bundle.v1"
	chartResult, err := delivery.BuildHelmChartArtifact(t.Context(), chartLayout, chartType, chartBuffer.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	cliDescriptor, err := delivery.PayloadDescriptor(t.Context(), cliLayout)
	if err != nil {
		t.Fatal(err)
	}
	chartDescriptor, err := delivery.PayloadDescriptor(t.Context(), chartLayout)
	if err != nil {
		t.Fatal(err)
	}
	layout := filepath.Join(root, "publication")
	outputs := []delivery.Output{
		{Name: "cli", Kind: lifecycle.OutputExecutable, Digest: cliDescriptor.Digest, BuildDigest: testDigest, Components: []string{"nvkectl"}},
		{Name: "controller-chart", Kind: lifecycle.OutputHelm, Digest: chartDescriptor.Digest, BuildDigest: testDigest},
	}
	content, err := delivery.ContentDigest(outputs)
	if err != nil {
		t.Fatal(err)
	}
	cliPayload, err := fileDigest(t.Context(), binary)
	if err != nil {
		t.Fatal(err)
	}
	chartPayload := fmt.Sprintf("sha256:%x", sha256.Sum256(chartBuffer.Bytes()))
	objects := []delivery.ObjectInput{
		{Name: "cli", Kind: "binary-bundle", Path: cliLayout, Layout: true, ArtifactType: delivery.MediaBinaryBundle, SemanticDigest: cliResult.SemanticDigest,
			Output: &delivery.OutputBinding{Repository: "cli", Format: lifecycle.OutputExecutable, BuildDigest: testDigest, Components: []string{"nvkectl"}, BuildSubjects: []delivery.BuildSubject{{Component: "nvkectl", Digest: cliPayload}}}},
		{Name: "controller-chart", Kind: lifecycle.OutputHelm, Path: chartLayout, Layout: true, ArtifactType: chartType, SemanticDigest: chartResult.SemanticDigest, BlobDigest: chartResult.RootDigest,
			Output: &delivery.OutputBinding{Repository: "controller-chart", Format: lifecycle.OutputHelm, BuildDigest: testDigest}},
	}
	for component, subject := range map[string]string{"nvkectl": cliPayload, "controller-chart": chartDescriptor.Digest} {
		output := component
		if component == "nvkectl" {
			output = "cli"
		}
		stage, kind, inspection := "build-output", "file", subject
		if component == "controller-chart" {
			stage, kind, inspection = "packaging", "helm-bundle", chartPayload
		}
		bom, err := sbom.NewSubject(testDigest, component, subject)
		if err != nil {
			t.Fatal(err)
		}
		if component == "controller-chart" {
			archive := filepath.Join(root, "chart.tgz")
			if err := os.WriteFile(archive, chartBuffer.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			inventory, err := sbom.InspectDependencies(t.Context(), archive, chartPayload, "tarball", sbom.Scanner{})
			if err != nil {
				t.Fatal(err)
			}
			bom, err = sbom.WithDependencies(bom, inventory.Components, inventory.Method, inventory.ScannerDigest)
			if err != nil {
				t.Fatal(err)
			}
		}
		bom, err = sbom.ForSubject(bom, subject, stage, inspection)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := bom.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		semantic, err := bom.Digest()
		if err != nil {
			t.Fatal(err)
		}
		inspectionBinding := ""
		if stage == "packaging" {
			inspectionBinding = inspection
		}
		objects = append(objects, delivery.ObjectInput{Name: "sbom-" + strings.TrimPrefix(semantic, "sha256:"), Kind: "sbom", ArtifactType: "application/vnd.cyclonedx+json", LayerMediaType: "application/vnd.cyclonedx+json", SemanticDigest: semantic, Bytes: raw,
			Evidence: &delivery.EvidenceBinding{Outputs: []string{output}, Component: component, Stage: stage, SubjectKind: kind, SubjectDigest: subject, InspectionInputDigest: inspectionBinding}})
	}
	publicationResult, err := delivery.BuildPublicationRoot(t.Context(), layout, delivery.PublicationRequest{
		Release: &delivery.ReleaseMetadata{DefinitionDigest: testDigest, EnvironmentDigest: testDigest, PlanDigest: testDigest, ContentSubject: content}, Objects: objects,
	})
	if err != nil {
		t.Fatal(err)
	}
	input.Bindings.Charts["controller"] = binding{Output: "controller-chart"}
	out := filepath.Join(root, "cycle")
	if err := os.Mkdir(out, 0700); err != nil {
		t.Fatal(err)
	}
	named := map[string]lifecycle.Artifact{"cli": {Name: "cli", Kind: lifecycle.OutputExecutable, Digest: cliResult.RootDigest}, "controller-chart": {Name: "controller-chart", Kind: lifecycle.OutputHelm, Digest: chartResult.RootDigest}}
	held, err := delivery.OpenPublication(t.Context(), layout, publicationResult.RootDigest)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	report := Report{Artifacts: map[string]any{}}
	if err := stageMaterials(t.Context(), Options{Out: out, Arch: "amd64"}, input, named, layout, held, &report); err != nil {
		t.Fatal(err)
	}
	var config map[string]json.RawMessage
	if err := readDocument(t.Context(), filepath.Join(out, "e2e-config.json"), &config); err != nil {
		t.Fatal(err)
	}
	cliConfig, err := section(config, "nvkectl", false)
	if err != nil {
		t.Fatal(err)
	}
	var selectedPath string
	if err := json.Unmarshal(cliConfig["binary"], &selectedPath); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(selectedPath)
	if err != nil || string(raw) != "exact candidate CLI" || selectedPath != filepath.Join(out, "binaries", "nvkectl") {
		t.Fatalf("actual extracted CLI: %s %v", selectedPath, err)
	}
	chart, err := os.ReadFile(filepath.Join(out, "chart-controller.tgz"))
	if err != nil || !bytes.Equal(chart, chartBuffer.Bytes()) {
		t.Fatal("native Helm owner did not preserve package bytes")
	}
	for role, trust := range map[string]string{"cli": "publication-verified", "chart:controller": "publication-verified", "profiles": "fixture", "chart:spire": "fixture", "deploy_assets": "source-checked-unsigned-receipt"} {
		entry, ok := report.Artifacts[role].(map[string]string)
		if !ok || entry["trust"] != trust {
			t.Fatalf("%s trust = %v, want %s", role, report.Artifacts[role], trust)
		}
	}
}

func TestFailureRecordSeparatesValidationMaterializationAndCompilation(t *testing.T) {
	for _, tc := range []struct {
		name, validation, materialization, compilation, wantValidation, wantMaterialization, wantCompilation string
	}{
		{"validation rejection", "input-descriptions-checked", "not-run", "not-run", "failed", "not-run", "not-run"},
		{"copy rejection", "source-and-publication-checked", "in-progress", "not-run", "source-and-publication-checked", "failed", "not-run"},
		{"compile rejection", "source-and-publication-checked", "core-artifacts-staged", "in-progress", "source-and-publication-checked", "core-artifacts-staged", "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := Report{ValidationOutcome: tc.validation, MaterializationOutcome: tc.materialization, CompilationOutcome: tc.compilation}
			recordFailure(&report, e.New(e.CodeVerifyFailed, "owner rejection"))
			if report.ValidationOutcome != tc.wantValidation || report.MaterializationOutcome != tc.wantMaterialization || report.CompilationOutcome != tc.wantCompilation || report.ErrorCode != string(e.CodeVerifyFailed) {
				t.Fatalf("failure conflated outcomes or discarded owner code: %+v", report)
			}
		})
	}
}

func TestAdapterErrorPreservesOwningCodesAndCancellation(t *testing.T) {
	owner := e.New(e.CodeImmutableViolation, "immutable fixture")
	requireCode(t, inputError(t.Context(), e.CodeInternal, owner, "copying fixture"), e.CodeImmutableViolation)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	requireCode(t, inputError(ctx, e.CodeInternal, context.Canceled, "copying fixture"), e.CodeTimeout)
}

func TestRunStatusIsUnauthenticatedDiagnosticOnly(t *testing.T) {
	options, _ := testOptions(t)
	run := runDocument()
	run["status"] = "failed"
	writeTestJSON(t, options.Run, run)
	report, err := Prepare(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if report.RunRecordStatus != "failed" || report.RunRecordTrust != "run-record-unauthenticated" || report.QualificationAuthority != "none" || report.MaterializationOutcome != "not-run" {
		t.Fatalf("Run status became authority: %+v", report)
	}
}
