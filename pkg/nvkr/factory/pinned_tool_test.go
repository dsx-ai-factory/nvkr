// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"errors"
	"io"
	"net/url"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/provenance"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

var pinnedToolFixturePin = catalog.ToolPin{Format: "tar-gzip", Platform: "linux/amd64", ExecutablePath: "aicr",
	URL:           "https://github.com/NVIDIA/aicr/releases/download/v1.2.0-rc1/aicr_1.2.0-rc1_linux_amd64.tar.gz",
	ArchiveDigest: "sha256:" + strings.Repeat("a", 64), ExecutableDigest: "sha256:" + strings.Repeat("b", 64)}

func pinnedToolFactoryInputs(t *testing.T, edit func(*catalog.Catalog, *authored.Definition)) (authored.Definition, authored.Environment, *catalog.Stack) {
	t.Helper()
	pin := pinnedToolFixturePin
	cat := &catalog.Catalog{Components: map[string]catalog.Component{
		"consumer": {
			Platforms: []string{"linux/amd64"}, FromSource: true, BuilderType: catalog.BuilderOCIHermetic, OutputType: "tarball",
			Repo: "https://example.test/consumer", Tag: "v1.0.0", Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40),
			SourceDate: 1, ToolchainImage: "example.test/consumer@sha256:" + strings.Repeat("c", 64),
			BuildCommand: []string{"sh", "render.sh"}, WriteDir: "out", ArtifactPath: "out/rendered.tar.gz",
			Materials: map[string]string{"recipe": "recipe"},
		},
		"recipe": {
			Platforms: []string{"linux/amd64"}, BuilderType: catalog.BuilderPinnedTool, Tool: "aicr", ToolExecutable: &pin,
			ToolArgs: []string{"recipe", "--output", "{{.Output}}/recipe.yaml"}, Tag: "v1.2.0-rc1",
			ToolchainImage: "example.test/runner@sha256:" + strings.Repeat("d", 64),
			OutputType:     catalog.PinnedToolOutputFile, ArtifactPath: "recipe.yaml",
		},
	}}
	definition := authored.Definition{
		APIVersion: authored.DefinitionAPIVersion, Kind: authored.DefinitionKind,
		Metadata: authored.Metadata{Name: "pinned-tool"}, Catalog: authored.Catalog{Base: "catalog"},
		Outputs: []authored.DefinitionOutput{{
			Name: "rendered", Platform: "linux/amd64", Format: authored.OutputExecutable,
			Components: []string{"consumer", "recipe"},
		}},
	}
	if edit != nil {
		edit(cat, &definition)
	}
	stack, err := catalog.ApplyStack(cat)
	if err != nil {
		t.Fatal(err)
	}
	environment := authored.Environment{
		APIVersion: authored.EnvironmentAPIVersion, Kind: authored.EnvironmentKind,
		Metadata: authored.Metadata{Name: "test"}, RunsOn: "ubuntu-24.04",
		ToolchainAccess:  &authored.ToolchainAccess{PublicRoots: []string{"docker.io/library/golang", "example.test/consumer", "example.test/runner"}},
		UtilityContainer: fixtureUtilityContainer, UtilityPlatform: "linux/amd64",
	}
	return definition, environment, stack
}

func TestPinnedToolProducerConsumesSharedToolAcquisition(t *testing.T) {
	definition, environment, stack := pinnedToolFactoryInputs(t, nil)
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	nodes, artifacts := planIndex(plan)
	tool, ok := nodes["tool/aicr"]
	if !ok || tool.Kind != NodeAcquire || tool.URL != pinnedToolFixturePin.URL || tool.Digest != pinnedToolFixturePin.ArchiveDigest ||
		tool.ExecutableDigest != pinnedToolFixturePin.ExecutableDigest || tool.ToolFormat != ToolFormatTarGzip {
		t.Fatalf("pinned-tool producer did not compile the shared acquisition: %#v", tool)
	}
	producer := nodes["rendered/build-recipe"]
	if producer.Kind != NodeBuild || !slices.Equal(producer.Inputs, []string{"tool/aicr"}) {
		t.Fatalf("producer lacks its explicit tool input: %#v", producer)
	}
	if consumer := nodes["rendered/build-consumer"]; !slices.Contains(consumer.Inputs, "artifact/rendered/recipe") {
		t.Fatalf("consumer lacks its material input: %#v", consumer)
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("compiled Plan is invalid: %v", err)
	}
	if shared, err := withProducerTools(map[string]catalog.ToolPin{"aicr": pinnedToolFixturePin}, plan.Builds); err != nil || len(shared) != 1 {
		t.Fatalf("identical run-wide and producer pins did not share one acquisition: %v, %v", shared, err)
	}
	// Companions are delivery-only: a run-wide pin that differs only in them shares the
	// executable acquisition, which carries exactly the executable fields.
	for _, digest := range []string{"c", "d"} {
		withCompanion := pinnedToolFixturePin
		withCompanion.Companions = []catalog.ToolCompanion{{Path: "aicr-attestation.sigstore.json", Digest: "sha256:" + strings.Repeat(digest, 64)}}
		shared, err := withProducerTools(map[string]catalog.ToolPin{"aicr": withCompanion}, plan.Builds)
		if err != nil || len(shared) != 1 || !reflect.DeepEqual(shared["aicr"], pinnedToolFixturePin) || len(withCompanion.Companions) != 1 {
			t.Fatalf("a run-wide pin with companions did not share the executable acquisition: %+v, %v", shared, err)
		}
	}
	// One acquisition has one URL: the same bytes at another URL still conflict.
	for _, change := range []func(*catalog.ToolPin){
		func(p *catalog.ToolPin) { p.ExecutableDigest = "sha256:" + strings.Repeat("d", 64) },
		func(p *catalog.ToolPin) { p.URL = "https://mirror.example/aicr.tar.gz" },
	} {
		conflicting := pinnedToolFixturePin
		change(&conflicting)
		_, err = withProducerTools(map[string]catalog.ToolPin{"aicr": conflicting}, plan.Builds)
		assertFactoryErrorCode(t, err, e.CodeUnpinnedSource)
	}

	var received pipeline.RunOptions
	stop := errors.New("stop after admission")
	err = executeProducer(t.Context(), plan, producer, "docker", 1, io.Discard, io.Discard,
		func(_ ir.Node, opts pipeline.RunOptions) (*pipeline.ArtifactResult, error) {
			received = opts
			return nil, stop
		}, nil)
	if !errors.Is(err, stop) || received.ToolExecutable != artifacts["tool/aicr"].Path || received.BuildMaterials != nil {
		t.Fatalf("producer received %q, %v; want planned tool %q", received.ToolExecutable, err, artifacts["tool/aicr"].Path)
	}

	producer.Inputs = nil
	err = executeProducer(t.Context(), plan, producer, "docker", 1, io.Discard, io.Discard,
		func(ir.Node, pipeline.RunOptions) (*pipeline.ArtifactResult, error) { return nil, stop }, nil)
	assertFactoryErrorCode(t, err, e.CodeProvenanceMissing)

	tampered := *plan
	tampered.Graph.Nodes = slices.Clone(plan.Graph.Nodes)
	for i := range tampered.Graph.Nodes {
		if tampered.Graph.Nodes[i].ID == producer.ID {
			tampered.Graph.Nodes[i].Inputs = nil
		}
	}
	assertFactoryErrorCode(t, tampered.Validate(), e.CodePipelineDrift)
}

func TestPinnedToolPlanAdmission(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*catalog.Catalog, *authored.Definition)
		code e.Code
	}{
		{"run-wide selection with a different pin", func(cat *catalog.Catalog, definition *authored.Definition) {
			other := pinnedToolFixturePin
			other.ExecutableDigest = "sha256:" + strings.Repeat("e", 64)
			cat.Tools = map[string]catalog.ToolPin{"aicr": other}
			definition.SBOMTool = "aicr"
		}, e.CodeUnpinnedSource},
		// The shared pin is admitted; SBOM inspection then refuses the file payload kind.
		{"SBOM over a file result", func(cat *catalog.Catalog, definition *authored.Definition) {
			cat.Tools = map[string]catalog.ToolPin{"aicr": pinnedToolFixturePin}
			definition.SBOMTool = "aicr"
		}, e.CodeEngineCapability},
		{"KVM execution request without a matching class", func(cat *catalog.Catalog, _ *authored.Definition) {
			recipe := cat.Components["recipe"]
			recipe.BuildRequirements = &execution.Requirements{
				Resources:            &execution.ResourceRequest{CPUMillis: 2000, MemoryMiB: 4096, EphemeralStorageMiB: 8192},
				AllowedHostPlatforms: []execution.HostPlatform{execution.HostPlatformLinuxAMD64},
				Capabilities:         []execution.CapabilityRequirement{{Name: execution.CapabilityKVM, Policy: execution.CapabilityRequired}},
				TimeoutSeconds:       600,
			}
			cat.Components["recipe"] = recipe
		}, e.CodeEngineCapability},
		{"zip asset", func(cat *catalog.Catalog, _ *authored.Definition) {
			recipe := cat.Components["recipe"]
			pin := *recipe.ToolExecutable
			pin.Format, pin.URL = "zip", strings.TrimSuffix(pin.URL, ".tar.gz")+".zip"
			recipe.ToolExecutable = &pin
			cat.Components["recipe"] = recipe
		}, e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			definition, environment, stack := pinnedToolFactoryInputs(t, test.edit)
			_, err := Compile(t.Context(), definition, environment, stack, "test")
			assertFactoryErrorCode(t, err, test.code)
		})
	}
}

func TestPinnedToolProvenanceRecordsWrappedBuildAndToolDependencies(t *testing.T) {
	definition, environment, stack := pinnedToolFactoryInputs(t, nil)
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	if got := productionBuildType(plan); got != provenance.BuildTypeWrappedBuild {
		t.Fatalf("plan with a pinned-tool build has buildType %q, want %q", got, provenance.BuildTypeWrappedBuild)
	}
	dependencies := resolvedDependencies(plan)
	logical := "nvkr:tool/aicr?" + url.Values{"archive": {pinnedToolFixturePin.URL}, "member": {"aicr"}}.Encode()
	want := map[string]string{
		pinnedToolFixturePin.URL: strings.Repeat("a", 64),
		logical:                  strings.Repeat("b", 64),
	}
	for _, dependency := range dependencies {
		if digest, ok := want[dependency.URI]; ok && dependency.Digest["sha256"] == digest {
			delete(want, dependency.URI)
		}
	}
	if len(want) != 0 {
		t.Fatalf("resolved dependencies omit tool subjects %v: %#v", want, dependencies)
	}
	digest := "sha256:" + strings.Repeat("f", 64)
	started := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	run, err := provenance.NewLocalRunContext(digest, started, started.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	predicate := provenance.ProductionPredicate(run, provenance.ProductionInputs{
		BuildType: productionBuildType(plan), PlanDigest: digest, RunPlanReference: "pinned-tool-fixture",
		DefinitionDigest: digest, EnvironmentDigest: digest, CatalogDigest: digest,
		GeneratorVersion: "fixture", SigningMode: provenance.SigningModeDryRun, ResolvedDependencies: dependencies,
	})
	if err := provenance.ValidateProduction(predicate); err != nil {
		t.Fatalf("production owner rejected pinned-tool dependencies: %v", err)
	}
	for _, test := range []struct {
		name string
		edit func([]provenance.Dependency) []provenance.Dependency
	}{
		{"omitted logical tool", func(d []provenance.Dependency) []provenance.Dependency {
			return slices.DeleteFunc(d, func(x provenance.Dependency) bool { return x.URI == logical })
		}},
		{"altered executable digest", func(d []provenance.Dependency) []provenance.Dependency {
			for i := range d {
				if d[i].URI == logical {
					d[i].Digest = map[string]string{"sha256": strings.Repeat("e", 64)}
				}
			}
			return d
		}},
		{"omitted release transport", func(d []provenance.Dependency) []provenance.Dependency {
			return slices.DeleteFunc(d, func(x provenance.Dependency) bool { return x.URI == pinnedToolFixturePin.URL })
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			edited := test.edit(slices.Clone(resolvedDependencies(plan)))
			assertFactoryErrorCode(t, requirePinnedToolDependencies(plan, edited), e.CodeProvenanceMissing)
		})
	}
}

func TestPinnedToolRendersOnDefaultHostedRouting(t *testing.T) {
	base, err := catalog.LoadContext(t.Context(), testCatalogProvider(t))
	if err != nil {
		t.Fatal(err)
	}
	definition, environment, stack := pinnedToolFactoryInputs(t, func(cat *catalog.Catalog, _ *authored.Definition) {
		cat.Actions = base.Actions
	})
	plan, err := Compile(t.Context(), definition, environment, stack, "v0.0.0 (test)")
	if err != nil {
		t.Fatal(err)
	}
	plan.DefinitionPath, plan.EnvironmentPath = "examples/definition.yaml", "examples/environment.yaml"
	for _, id := range []string{"tool/aicr", "rendered/build-recipe"} {
		nodes, _ := planIndex(plan)
		execution, err := plan.ExecutionFor(nodes[id])
		if err != nil {
			t.Fatal(err)
		}
		if execution.RunsOn != environment.RunsOn {
			t.Fatalf("%s runs on %q, want default hosted %q", id, execution.RunsOn, environment.RunsOn)
		}
	}
	first, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Files) != len(second.Files) || first.WorkflowDigest != second.WorkflowDigest {
		t.Fatal("pinned-tool bundle is not byte-stable")
	}
	for path, raw := range first.Files {
		if string(second.Files[path]) != string(raw) {
			t.Fatalf("pinned-tool bundle file %s is not byte-stable", path)
		}
	}
	if err := ValidateGitHubBundle(t.Context(), plan, first); err != nil {
		t.Fatalf("generated bundle fails its drift check: %v", err)
	}
	workflow := string(expandedGitHubWorkflow(t, first))
	for _, job := range []string{"Acquire pinned aicr", "Build recipe (rendered)"} {
		hosted := regexp.MustCompile(`name: '` + regexp.QuoteMeta(job) + `'\s+runs-on: '` + regexp.QuoteMeta(environment.RunsOn) + `'`)
		if !hosted.MatchString(workflow) {
			t.Fatalf("generated job %q is not on default hosted runner %q", job, environment.RunsOn)
		}
	}
}

// Hosted SBOM publication requires the index to cover every producer result, and
// release evidence requires one inventory per selected component, so a pinned-tool
// result needs its own wrapped-build subject rather than an exclusion.
func TestPinnedToolResultIsCoveredByHostedSBOM(t *testing.T) {
	definition, environment, stack := pinnedToolFactoryInputs(t, nil)
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	_, artifacts := planIndex(plan)
	for _, component := range []string{"consumer", "recipe"} {
		body := []byte(component + " bytes")
		encoded, err := canonical.Marshal(pipeline.ArtifactResult{Component: component, SHA256: pipeline.FileDigest(body), Size: int64(len(body))})
		if err != nil {
			t.Fatal(err)
		}
		if err := writeFactoryFile(t.Context(), artifacts["result/rendered/"+component].Path, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	node := planNode(plan, "rendered/sbom")
	if err := executeSBOM(t.Context(), plan, node, artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}
	local := func(filename string) (string, error) { return filename, nil }
	objects, err := sbomPublicationObjects(t.Context(), plan, artifacts[node.Outputs[0]], artifacts, local)
	if err != nil || len(objects) != 3 {
		t.Fatalf("hosted SBOM publication: objects=%d error=%v", len(objects), err)
	}
	var recipe sbom.BOM
	for _, object := range objects[1:] {
		var bom sbom.BOM
		if err := canonical.UnmarshalExact(bytes.TrimSpace(object.Bytes), &bom); err != nil {
			t.Fatal(err)
		}
		if bom.Metadata.Component.Name == "recipe" {
			recipe = bom
		}
	}
	lane := map[string]string{}
	for _, property := range recipe.Metadata.Component.Properties {
		lane[property.Name] = property.Value
	}
	if recipe.Metadata.Component.Type != "file" || lane["nvkr:lane"] != "wrapped-build" || lane["nvkr:fromSource"] != "false" ||
		lane["nvkr:toolExecutable"] != pinnedToolFixturePin.ExecutableDigest {
		t.Fatalf("pinned-tool SBOM subject = %+v", recipe.Metadata.Component)
	}
}

func TestLocalRunAdmitsPinnedToolHostBeforeWork(t *testing.T) {
	definition, environment, stack := pinnedToolFactoryInputs(t, nil)
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	var want e.Code
	if runtime.GOOS+"/"+runtime.GOARCH != "linux/amd64" {
		want = e.CodeEngineCapability // held executables are Linux-only and never emulated
	}
	if got := factoryErrorCode(admitLocalPinnedTools(plan)); got != want {
		t.Fatalf("local pinned-tool host admission code %q, want %q", got, want)
	}
}
