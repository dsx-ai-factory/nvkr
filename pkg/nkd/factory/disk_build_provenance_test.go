package factory

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

func TestARCCompletesPreparedDiskPublication(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("held disk publication requires Linux")
	}
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		t.Run(platform, func(t *testing.T) {
			plan, owner := diskMaterialTraceFixtureWithPublicationMode(t, false, false, platform, "none", "test-acquisitions.tar", true, "per-output-v1")
			nodes, artifacts := planIndex(plan)
			if err := executeHeldDiskObservation(t.Context(), plan, nodes["node-image/terminal-observe"], artifacts, owner); err != nil {
				t.Fatal(err)
			}
			if err := completeGitHubDiskPublication(t.Context(), plan, plan.LifecycleOutputs[0], owner, nil, GitHubStepOptions{Stdout: io.Discard, Stderr: io.Discard}); err != nil {
				t.Fatal(err)
			}
			node := nodes["package/"+outputPublicationPackage("node-image")]
			inspection, err := delivery.Inspect(t.Context(), artifacts[node.Outputs[0]].Path)
			if err != nil {
				t.Fatal(err)
			}
			disks := 0
			for _, object := range inspection.Root.Objects {
				if object.Kind == PackageDiskImage && object.Name == "node-image" {
					disks++
				}
			}
			if disks != 1 {
				t.Fatalf("prepared publication contains %d selected disks", disks)
			}
		})
	}
}

func TestDiskBuildProvenancePublication(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		for _, failure := range []string{"", "missing consumption", "closed owner", "changed result"} {
			t.Run(platform+"/"+failure, func(t *testing.T) {
				plan, owner := diskMaterialTraceFixtureWithCapture(t, false, false, platform, "none", "test-acquisitions.tar", true)
				expected := plan.LifecycleOutputs[0]
				nodes, artifacts := planIndex(plan)
				if err := executeHeldDiskObservation(t.Context(), plan, nodes["node-image/terminal-observe"], artifacts, owner); err != nil {
					t.Fatal(err)
				}
				switch failure {
				case "missing consumption":
					delete(owner.materialConsumptions, "node-image/build-agent")
				case "closed owner":
					if err := owner.Close(); err != nil {
						t.Fatal(err)
					}
				case "changed result":
					if err := os.WriteFile(artifacts["result/node-image/agent"].Path, []byte("{}"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				err := CompleteHostedDiskPublication(t.Context(), plan, expected, owner, nil)
				if failure == "" && runtime.GOOS != "linux" {
					assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
					return
				}
				if failure != "" {
					want := e.CodeVerifyFailed
					switch failure {
					case "closed owner":
						want = e.CodeGateUnmet
					case "changed result":
						want = e.CodePipelineDrift
					}
					assertFactoryErrorCode(t, err, want)
					if _, err := os.Stat(artifacts["package/node-image"].Path); !os.IsNotExist(err) {
						t.Fatal("failed publication left a package")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				out, err := ObserveDiskPublicationPayload(t.Context(), plan, expected, artifacts)
				if err != nil {
					t.Fatal(err)
				}
				if out.Disk.EvidenceMode != sbom.DiskBuildProvenance {
					t.Fatal("release lost evidence scope")
				}
				held, err := delivery.OpenSingleDiskArtifact(t.Context(), artifacts["package/node-image"].Path, platform, 64<<30)
				if err != nil {
					t.Fatal(err)
				}
				if err := held.Close(); err != nil {
					t.Fatal(err)
				}
				for _, component := range expected.Components {
					raw, err := os.ReadFile(filepath.Join(artifacts["sbom/node-image"].Path, component+".cdx.json"))
					if err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(string(raw), "not-performed") || strings.Contains(string(raw), "disk-reconciled") {
						t.Fatal("dishonest scope")
					}
				}
				assertFactoryErrorCode(t, CompleteHostedDiskPublication(t.Context(), plan, expected, owner, nil), e.CodeImmutableViolation)
			})
		}
	}
}

func TestARCBuildProvenanceNeedsNoInspector(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		t.Run(platform, func(t *testing.T) {
			base := arcTestPlan(t, platform)
			environment := base.Environment.Clone()
			environment.DiskEvidence = sbom.DiskBuildProvenance
			environment.DiskInspectionProfiles = nil
			stack, err := catalog.ApplyStack(base.CatalogSnapshot)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := Compile(t.Context(), base.Definition, environment, stack, "dev (unknown)")
			if err != nil {
				t.Fatal(err)
			}
			plan.DefinitionPath, plan.EnvironmentPath = base.DefinitionPath, base.EnvironmentPath
			if _, err := RenderGitHub(t.Context(), plan); err != nil {
				t.Fatal(err)
			}
			stages, _, err := githubStages(plan)
			if err != nil {
				t.Fatal(err)
			}
			for _, stage := range stages {
				for _, image := range githubStagePrivateImages(plan, stage) {
					if strings.Contains(image, "inspector") {
						t.Fatal("deferred inspector pull")
					}
				}
			}
		})
	}
}

func TestPersistedDiskBuildProvenanceBindsPlannedSourceAndSubject(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		for _, test := range []struct {
			name string
			want e.Code
		}{
			{"valid", ""}, {"source changed", e.CodeVerifyFailed},
			{"subject changed", e.CodeVerifyFailed}, {"unknown component", e.CodeProvenanceMissing},
			{"unknown output", e.CodeProvenanceMissing},
		} {
			t.Run(platform+"/"+test.name, func(t *testing.T) {
				plan := arcTestPlan(t, platform)
				build := plan.Builds[0]
				component := build.ResolvedComponents[0].Name
				subject := "sha256:" + strings.Repeat("a", 64)
				hashes := map[string]string{}
				for _, member := range build.ResolvedComponents {
					hashes[member.Name] = subject
				}
				documents, err := diskBuildDocuments(build, hashes)
				if err != nil {
					t.Fatal(err)
				}
				document := documents[component]
				if test.name == "source changed" {
					document.Metadata.Component.Version += "-substituted"
				}
				raw, err := document.Marshal()
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(t.TempDir(), "component.cdx.json")
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				raw, err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var persisted sbom.BOM
				if err := canonical.UnmarshalExact(raw, &persisted); err != nil {
					t.Fatal(err)
				}
				output := build.Name
				switch test.name {
				case "subject changed":
					subject = "sha256:" + strings.Repeat("b", 64)
				case "unknown component":
					component = "not-planned"
				case "unknown output":
					output = "not-planned"
				}
				err = validateDiskBuildDocument(plan, output, component, subject, &persisted)
				if test.want == "" {
					if err != nil {
						t.Fatal(err)
					}
				} else {
					assertFactoryErrorCode(t, err, test.want)
				}
			})
		}
	}
}

func TestDiskBuildProvenanceAdmission(t *testing.T) {
	base := arcTestPlan(t, "linux/amd64")
	for _, name := range []string{"explicit", "unknown", "no ARC", "no publication"} {
		t.Run(name, func(t *testing.T) {
			env := base.Environment.Clone()
			env.DiskEvidence = sbom.DiskBuildProvenance
			switch name {
			case "unknown":
				env.DiskEvidence = "skip"
			case "no ARC":
				env.GitHubARC = ""
			case "no publication":
				env.Publication = nil
			}
			err := env.Validate()
			if name != "explicit" {
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			stack, err := catalog.ApplyStack(base.CatalogSnapshot)
			if err != nil {
				t.Fatal(err)
			}
			changed, err := Compile(t.Context(), base.Definition, env, stack, "dev (unknown)")
			if err != nil {
				t.Fatal(err)
			}
			before, err := base.IdentityDigest()
			if err != nil {
				t.Fatal(err)
			}
			after, err := changed.IdentityDigest()
			if err != nil {
				t.Fatal(err)
			}
			if before == after {
				t.Fatal("evidence mode did not affect Plan identity")
			}
			if base.Builds[0].BuildDigest != changed.Builds[0].BuildDigest {
				t.Fatal("evidence mode changed producer identity")
			}
		})
	}
}
