// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

func TestUnresolvedImageSourceRefusesCompilation(t *testing.T) {
	for _, source := range []string{"mirror.invalid/new-compiler", "nvkr-toolchain-legacy"} {
		t.Run(source, func(t *testing.T) {
			base := buildTransportPlan(t, "source")
			component := base.CatalogSnapshot.Components["containerd"]
			_, digest, _ := strings.Cut(component.ToolchainImage, "@")
			component.ToolchainImage = source + "@" + digest
			base.CatalogSnapshot.Components["containerd"] = component
			stack, err := catalog.ApplyStack(base.CatalogSnapshot)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := Compile(t.Context(), base.Definition, base.Environment, stack, "test")
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if plan != nil {
				t.Fatal("unresolved source emitted partial Plan")
			}
		})
	}
}

func TestDiagnosticImageAccessRefusesUnclassifiedOrPrivate(t *testing.T) {
	for _, private := range []bool{false, true} {
		t.Run(map[bool]string{false: "unclassified", true: "private"}[private], func(t *testing.T) {
			plan := recompileGitHubStabilityPlan(t, cliTestPlan(t, "amd64", "scalar", 1), func(in *githubStabilityInputs) {
				in.environment.ToolchainAccess = nil
				if private {
					in.environment.ToolchainAccess = &authored.ToolchainAccess{PrivateRoots: []string{"ghcr.io/example/cli"}}
					component := in.catalog.Components["nvkectl"]
					component.ToolchainImage = "ghcr.io/example/cli@sha256:" + strings.Repeat("a", 64)
					in.catalog.Components["nvkectl"] = component
				}
			})
			inputs, err := LoadCLIArtifactTestInputs(t.Context(), writeCLIPlan(t, plan), "")
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if inputs != nil {
				t.Fatal("refused diagnostic emitted executable inputs")
			}
		})
	}
}

func TestImageUseLocalSourcesNeverBecomeRegistryReferences(t *testing.T) {
	plan := &Plan{}
	classifier, err := plan.Environment.ToolchainAccess.ImageClassifier()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, engine string
		native       bool
		route        ImageRoute
	}{
		{"measured image", "local", false, ImageMeasuredLocal},
		{"native bundle", "local", true, ImageNativeBundle},
		{"hosted image refusal", "github", false, ""},
		{"hosted bundle refusal", "github", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reference := "sha256:" + strings.Repeat("a", 64)
			use, err := plan.resolveImageUse(classifier, tc.engine, ImageUse{ConsumerID: "producer", Role: "producer", Reference: reference}, tc.native)
			if tc.route == "" {
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
				return
			}
			if err != nil || use.Reference != reference || use.Route != tc.route || use.Reader != (authored.ToolchainReader{}) {
				t.Fatalf("local source was reinterpreted: %+v / %v", use, err)
			}
		})
	}
}

func TestImageUsesLocalHelperKeepsLocalIDsAndRequiresRegistryRoutes(t *testing.T) {
	base := cliTestPlan(t, "amd64", "local-image", 1)
	for _, declared := range []bool{true, false} {
		t.Run(map[bool]string{true: "declared utility", false: "unclassified utility"}[declared], func(t *testing.T) {
			plan := base
			if !declared {
				plan = recompileGitHubStabilityPlan(t, base, func(in *githubStabilityInputs) { in.environment.ToolchainAccess = nil })
			}
			_, err := admitOperation(t.Context(), plan, "renamed-cli/build-nvkectl")
			if !declared {
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			} else if err != nil {
				t.Fatal(err)
			}
			_, err = plan.ImageUses("github")
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
		})
	}
}

func TestImageUsesRefreshedPlanRefusesUnknownRootBeforeWrites(t *testing.T) {
	baseline := githubRuntimePlanFixture(t)
	root := t.TempDir()
	writeGitHubRuntimeBaseline(t, root, baseline)
	prior := githubRuntimeTree(t, root)
	fresh := recompileGitHubStabilityPlan(t, baseline, func(in *githubStabilityInputs) {
		component := in.catalog.Components["containerd"]
		_, digest, _ := strings.Cut(component.ToolchainImage, "@")
		component.ToolchainImage = "unclassified.example/compiler@" + digest
		in.catalog.Components["containerd"] = component
	})
	assertFactoryErrorCode(t, PrepareGitHubRuntimeBundle(t.Context(), fresh, root, false), e.CodeEngineCapability)
	if !reflect.DeepEqual(prior, githubRuntimeTree(t, root)) {
		t.Fatal("refused runtime refresh wrote output")
	}
}

func TestImageUsesPartitionReaderCannotBeOmitted(t *testing.T) {
	plan := recompileGitHubStabilityPlan(t, privateImageTestPlan(t, ProductionRegistryWriteCredential), func(in *githubStabilityInputs) { in.environment.GitHubExecutionPartitions = true })
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"nvkr-toolchain-read-token", "nvkr-toolchain-read-user"} {
		t.Run(field, func(t *testing.T) {
			member := ""
			for _, path := range sortedFileNames(bundle.Files) {
				if githubPartitionWorkflowSuffix.MatchString(path) && strings.Contains(string(bundle.Files[path]), field+":") {
					member = path
					break
				}
			}
			if member == "" {
				t.Fatal("no private reader in partition fixture")
			}
			changed := mutateGitHubBundleYAML(t, bundle, member, func(root *yamlv3.Node) {
				jobs := githubTestYAMLField(t, root, "jobs")
				for i := 1; i < len(jobs.Content); i += 2 {
					steps := githubYAMLField(jobs.Content[i], "steps")
					if steps == nil {
						continue
					}
					for _, step := range steps.Content {
						with := githubYAMLField(step, "with")
						if githubYAMLField(with, field) != nil {
							githubYAMLRemove(with, field)
							return
						}
					}
				}
				t.Fatal("no reader caller argument to mutate")
			})
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, changed), e.CodeEngineCapability)
		})
	}
}

func TestImageUsesOrdinaryHostedAdmission(t *testing.T) {
	base := buildTransportPlan(t, "source")
	for _, tc := range []struct {
		name   string
		change func(*githubStabilityInputs)
		denied bool
	}{
		{"declared public", func(*githubStabilityInputs) {}, false},
		{"absent access", func(in *githubStabilityInputs) { in.environment.ToolchainAccess = nil }, true},
		{"public startup", func(in *githubStabilityInputs) {
			in.environment.Runtime.RuntimeImage = fixtureUtilityContainer
			in.environment.Runtime.Services = []string{fixtureUtilityContainer}
		}, false},
		{"public service only", func(in *githubStabilityInputs) {
			in.environment.Runtime.Services = []string{fixtureUtilityContainer}
		}, false},
		{"unknown startup", func(in *githubStabilityInputs) {
			in.environment.Runtime.RuntimeImage = "ghcr.io/example/start@sha256:" + strings.Repeat("a", 64)
		}, true},
		{"private startup", func(in *githubStabilityInputs) {
			in.environment.Runtime.RuntimeImage = "ghcr.io/example/start@sha256:" + strings.Repeat("a", 64)
			in.environment.ToolchainAccess.PrivateRoots = []string{"ghcr.io/example/start"}
			in.environment.ToolchainAccess.Reader = &authored.ToolchainReader{CredentialRef: "github:READER", UsernameRef: "github:ACCOUNT"}
		}, true},
		{"unknown service only", func(in *githubStabilityInputs) {
			in.environment.Runtime.Services = []string{"ghcr.io/example/service@sha256:" + strings.Repeat("a", 64)}
		}, true},
		{"private service only", func(in *githubStabilityInputs) {
			in.environment.Runtime.Services = []string{"ghcr.io/example/service@sha256:" + strings.Repeat("a", 64)}
			in.environment.ToolchainAccess.PrivateRoots = []string{"ghcr.io/example/service"}
			in.environment.ToolchainAccess.Reader = &authored.ToolchainReader{CredentialRef: "github:READER", UsernameRef: "github:ACCOUNT"}
		}, true},
		{"private ordinary producer", func(in *githubStabilityInputs) {
			in.environment.ToolchainAccess.PrivateRoots = []string{"ghcr.io/example/compiler"}
			in.environment.ToolchainAccess.Reader = &authored.ToolchainReader{CredentialRef: "github:READER", UsernameRef: "github:ACCOUNT"}
			component := in.catalog.Components["containerd"]
			component.ToolchainImage = "ghcr.io/example/compiler@sha256:" + strings.Repeat("a", 64)
			in.catalog.Components["containerd"] = component
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := recompileGitHubStabilityPlan(t, base, tc.change)
			uses, err := plan.ImageUses("github")
			if tc.denied {
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
				if uses != nil {
					t.Fatal("refusal returned partial inventory")
				}
				bundle, err := RenderGitHub(t.Context(), plan)
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
				if bundle != nil {
					t.Fatal("refusal emitted partial bundle")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(uses) == 0 {
				t.Fatal("empty inventory")
			}
			if (tc.name == "public startup" || tc.name == "public service only") && !slices.ContainsFunc(uses, func(use ImageUse) bool {
				return use.Role == "service" && use.Reference == fixtureUtilityContainer
			}) {
				t.Fatal("missing public service use")
			}
			for _, use := range uses {
				if use.Route != ImageAnonymous || use.Reader != (authored.ToolchainReader{}) {
					t.Fatalf("unexpected route: %+v", use)
				}
			}
			if _, err := RenderGitHub(t.Context(), plan); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestImageUsesPrivateUtilityAndVendor(t *testing.T) {
	for _, role := range []string{"utility", "vendor"} {
		t.Run(role, func(t *testing.T) {
			plan := recompileGitHubStabilityPlan(t, arcTestPlan(t, "linux/amd64"), func(in *githubStabilityInputs) {
				image := "ghcr.io/new-owner/selected@sha256:" + strings.Repeat("a", 64)
				in.environment.ToolchainAccess.PrivateRoots = []string{"ghcr.io/new-owner/selected"}
				if role == "utility" {
					in.environment.UtilityContainer = image
				} else {
					// Vendor is independently owned by source preparation. Bind its
					// selected image through the catalog's vendor-only declaration.
					component := buildTransportPlan(t, "nix").CatalogSnapshot.Components["containerd"]
					component.ToolchainImage = image
					in.catalog.Components["vendor-only"] = component
					in.definition.Outputs = append(in.definition.Outputs, authored.DefinitionOutput{Name: "vendor-only", Platform: "linux/amd64", Format: authored.OutputExecutable, Components: []string{"vendor-only"}})
					in.environment.ToolchainAccess.PublicRoots = append(in.environment.ToolchainAccess.PublicRoots, "docker.io/nixos/nix")
					slices.Sort(in.environment.ToolchainAccess.PublicRoots)
				}
			})
			uses, err := plan.ImageUses("github")
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, use := range uses {
				if use.Role == role && use.Route == ImageScopedReader {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing private %s use", role)
			}
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			assertImageUsePreparation(t, plan, bundle)
		})
	}
}

func TestImageUsesNamingAndPublicationIndependence(t *testing.T) {
	base := privateImageTestPlan(t, ProductionRegistryWriteCredential)
	changed := recompileGitHubStabilityPlan(t, base, func(in *githubStabilityInputs) {
		in.definition.Metadata.Description = "Different display label"
		in.environment.Publication.RepositoryPrefix = "ghcr.io/other/publication"
	})
	a, err := base.ImageUses("github")
	if err != nil {
		t.Fatal(err)
	}
	b, err := changed.ImageUses("github")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) || base.Builds[0].BuildDigest != changed.Builds[0].BuildDigest {
		t.Fatal("display/publication altered input access or logical build")
	}
}

// This oracle reads the owning producer IR and utility fields. It deliberately
// does not use GitHubExecutionFor, ImageUses, stage containers or the renderer's
// private-image filter to choose expected subjects.
func expectedImageSubjects(t *testing.T, plan *Plan) map[string]string {
	t.Helper()
	want := map[string]string{}
	for _, build := range plan.Builds {
		for _, node := range build.Graph.Nodes {
			image := ""
			switch node.Kind {
			case "build":
				image = node.Toolchain
			case "fetch":
				image = node.VendorImage
			}
			if image != "" {
				want[build.Name+"/"+node.ID] = image
			}
		}
	}
	for _, node := range plan.Graph.Nodes {
		if node.IRNode == "" && node.Container != "" {
			want[node.ID] = node.Container
		}
	}
	return want
}

func assertImageUsePreparation(t *testing.T, plan *Plan, bundle *GeneratedBundle) {
	t.Helper()
	want := expectedImageSubjects(t, plan)
	uses, err := plan.ImageUses("github")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, use := range uses {
		got[use.ConsumerID] = use.Reference
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("image inventory differs from owner fields/IR")
	}
	stages, _, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	required := map[string]bool{}
	classifier, err := plan.Environment.ToolchainAccess.ImageClassifier()
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range stages {
		for _, node := range stage.Nodes {
			image := want[node.ID]
			if image == "" {
				continue
			}
			private, err := classifier.ClassifyImage(image)
			if err != nil {
				t.Fatal(err)
			}
			if private {
				required[stage.ID] = true
			}
		}
	}
	for _, doc := range githubEmittedWorkflows(t, plan, bundle) {
		for id, job := range doc.Jobs {
			if job.Env[githubImageToken] != "" || job.Env[githubImageUser] != "" {
				t.Fatal("reader at job scope")
			}
			prepared, execution := -1, -1
			preflight, observed := strings.HasPrefix(id, "preflight-images-"), false
			for index, step := range job.Steps {
				if strings.Contains(step.Run, "factory github prepare-images ") {
					if !required[id] || prepared >= 0 || step.If != "" || step.ContinueOnError || !reflect.DeepEqual(step.Env, githubImageEnvironment(plan)) {
						t.Fatalf("%s: preparation must run once with exact declared reader", id)
					}
					prepared = index
				} else if strings.Contains(step.Run, "factory github preflight-images ") {
					var wantEnv map[string]string
					for _, use := range uses {
						if use.Placement == job.RunsOn && use.Route == ImageScopedReader {
							wantEnv = githubImageEnvironment(plan)
						}
					}
					if !preflight || observed || step.If != "" || step.ContinueOnError || step.TimeoutMinutes != 3 || !reflect.DeepEqual(step.Env, wantEnv) {
						t.Fatalf("%s: preflight must run once with only its placement's declared reader", id)
					}
					observed = true
				} else if step.Env[githubImageToken] != "" || step.Env[githubImageUser] != "" {
					t.Fatalf("%s: reader escaped preparation", id)
				}
				if execution < 0 && (strings.Contains(step.Run, "factory github run-node ") || strings.Contains(step.Run, "factory github run-disk ")) {
					execution = index
				}
			}
			if preflight && (!observed || execution >= 0 || job.Permissions["packages"] != "" || job.Permissions["id-token"] != "") {
				t.Fatalf("%s: preflight missing or mixed with producer/writer authority", id)
			}
			if required[id] && (prepared < 0 || execution <= prepared) {
				t.Errorf("%s: preparation missing or after execution", id)
			}
			delete(required, id)
		}
	}
	if len(required) != 0 {
		t.Fatalf("missing worker jobs: %v", required)
	}
	if !slices.IsSortedFunc(uses, func(a, b ImageUse) int { return strings.Compare(a.ConsumerID, b.ConsumerID) }) {
		t.Fatal("unsorted inventory")
	}
}
