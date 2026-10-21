// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const nvkeFixtureDeploy = "controller/deploy/nke-sbom/nke-deploy.yaml"
const nvkeBetaFixtureDeploy = "deploy/nico/nvke-deploy.yaml"

type nvkeFixtureTree struct {
	files map[string][]byte
	reads []string
}

func (tree *nvkeFixtureTree) ReadFile(ctx context.Context, name string) ([]byte, error) {
	if err := pinContextError(ctx); err != nil {
		return nil, err
	}
	if err := validatePinSetFilePath(name); err != nil {
		return nil, err
	}
	tree.reads = append(tree.reads, name)
	body, ok := tree.files[name]
	if !ok {
		return nil, e.New(e.CodeInvalidCatalog, "fixture tree has no regular blob %q", name)
	}
	return body, nil
}

func (*nvkeFixtureTree) Close() error { return nil }

func nvkePinFixture(t *testing.T) (*registryDoc, *nvkeFixtureTree) {
	t.Helper()
	set := &PinSet{File: nvkeFixtureDeploy, Format: "nvke-deploy-v1", Members: map[string]PinSetMember{}, Accounted: map[string]PinSetItem{
		"nke-base-config":              {Type: "template"},
		"spire-tpm-pubhashes":          {Type: "manifest"},
		"spire-server-nodeport":        {Type: "manifest"},
		"spire-oidc-provider-nodeport": {Type: "manifest"},
		"capnico":                      {Type: "manifest"},
		"controller":                   {Type: "helm", Path: "../charts/controller"},
	}}
	reg := &registryDoc{Sources: map[string]Source{"nvke": {PinSet: set}}, Components: map[string]rawComponent{}}
	tree := &nvkeFixtureTree{files: map[string][]byte{}}
	files := []string{nvkeFixtureDeploy, "controller/deploy/nke-sbom/controller/release.yaml", "controller/deploy/charts/controller/Chart.yaml"}
	for _, member := range []struct{ name, repository string }{
		{"spire", "https://spiffe.github.io/helm-charts-hardened/"},
		{"spire-crds", "https://spiffe.github.io/helm-charts-hardened/"},
		{"temporal", "https://go.temporal.io/helm-charts"},
		{"postgresql", "oci://registry-1.docker.io/bitnamicharts"},
	} {
		artifact := member.name + "-chart"
		set.Members[member.name] = PinSetMember{Artifact: artifact}
		reg.Components[artifact] = rawComponent{PinnedBy: "nvke", Prebuilt: &Prebuilt{Chart: &HelmChart{Name: member.name, Repository: member.repository}}}
		files = append(files, "controller/deploy/nke-sbom/"+member.name+"/release.yaml")
	}
	for _, name := range files {
		body, err := os.ReadFile(filepath.Join("testdata/nvke-pin-set", filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		tree.files[name] = body
	}
	return reg, tree
}

func replaceNVKEFixture(t *testing.T, tree *nvkeFixtureTree, file, old, replacement string) {
	t.Helper()
	raw := string(tree.files[file])
	if !strings.Contains(raw, old) {
		t.Fatalf("fixture %s lacks %q", file, old)
	}
	tree.files[file] = []byte(strings.Replace(raw, old, replacement, 1))
}

func TestNVKEPinSetReadsActualUpstreamDeployMetadata(t *testing.T) {
	reg, tree := nvkePinFixture(t)
	versions, err := deriveNVKEPinSet(t.Context(), tree, reg, "nvke")
	want := map[string]string{"spire": "0.30.1", "spire-crds": "0.6.1", "temporal": "0.74.0", "postgresql": "18.5.15"}
	if err != nil || !reflect.DeepEqual(versions, want) {
		t.Fatalf("derived versions = %v, %v; want %v", versions, err, want)
	}
	expectedReads := []string{nvkeFixtureDeploy, "controller/deploy/charts/controller/Chart.yaml"}
	for _, item := range []string{"spire", "spire-crds", "temporal", "postgresql", "controller"} {
		expectedReads = append(expectedReads, "controller/deploy/nke-sbom/"+item+"/release.yaml")
	}
	sort.Strings(expectedReads)
	sort.Strings(tree.reads)
	if !reflect.DeepEqual(tree.reads, expectedReads) {
		t.Fatalf("read non-metadata input or missed chart metadata: %v", tree.reads)
	}
	if _, included := versions["controller"]; included {
		t.Fatal("local controller placeholder version became an external pin")
	}
}

func nvkeBetaPinFixture(t *testing.T) (*registryDoc, *nvkeFixtureTree) {
	t.Helper()
	reg, _ := nvkePinFixture(t)
	set := reg.Sources["nvke"].PinSet
	set.File = nvkeBetaFixtureDeploy
	delete(set.Accounted, "nke-base-config")
	set.Accounted["nvke-base-config"] = PinSetItem{Type: "template"}
	set.Accounted["controller"] = PinSetItem{Type: "helm", Path: "../../controller/deploy/charts/controller"}
	set.Accounted["nktp"] = PinSetItem{Type: "helm", Path: "../../nktp/deploy/charts/nktp"}
	tree := &nvkeFixtureTree{files: map[string][]byte{}}
	files := []string{set.File, "controller/deploy/charts/controller/Chart.yaml", "nktp/deploy/charts/nktp/Chart.yaml"}
	for _, item := range []string{"controller", "nktp", "postgresql", "spire", "spire-crds", "temporal"} {
		files = append(files, "deploy/nico/"+item+"/release.yaml")
	}
	for _, name := range files {
		raw, err := os.ReadFile(filepath.Join("testdata/nvke-beta-pin-set", filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		tree.files[name] = raw
	}
	return reg, tree
}

func TestNVKEPinSetReadsBetaLocalChartMetadata(t *testing.T) {
	for _, explicitVersion := range []bool{false, true} {
		t.Run(map[bool]string{false: "source chart version", true: "matching declared version"}[explicitVersion], func(t *testing.T) {
			reg, tree := nvkeBetaPinFixture(t)
			if explicitVersion {
				for _, item := range []string{"controller", "nktp"} {
					replaceNVKEFixture(t, tree, "deploy/nico/"+item+"/release.yaml", "chart:\n", "chart:\n  version: \"0.0.0\"\n")
				}
			}
			versions, err := deriveNVKEPinSet(t.Context(), tree, reg, "nvke")
			want := map[string]string{"spire": "0.30.1", "spire-crds": "0.6.1", "temporal": "0.74.0", "postgresql": "18.5.15"}
			if err != nil || !reflect.DeepEqual(versions, want) {
				t.Fatalf("derived beta versions = %v, %v; want %v", versions, err, want)
			}
			if len(tree.reads) != len(tree.files) {
				t.Fatalf("read %d metadata files, want complete %d-file closure", len(tree.reads), len(tree.files))
			}
		})
	}
}

func TestNVKEPinSetRejectsInvalidBetaLocalChartMetadata(t *testing.T) {
	const controller = "deploy/nico/controller/release.yaml"
	const chart = "controller/deploy/charts/controller/Chart.yaml"
	for _, tc := range []struct{ name, file, old, replacement string }{
		{"missing external version", "deploy/nico/spire/release.yaml", "  version: \"0.30.1\"\n", ""},
		{"local null version", controller, "chart:\n", "chart:\n  version: null\n"},
		{"local empty version", controller, "chart:\n", "chart:\n  version: \"\"\n"},
		{"local numeric version", controller, "chart:\n", "chart:\n  version: 1\n"},
		{"local version range", controller, "chart:\n", "chart:\n  version: \"~0.0.0\"\n"},
		{"local mismatched version", controller, "chart:\n", "chart:\n  version: \"0.0.1\"\n"},
		{"local chart unsafe name", chart, "name: nke-controller", "name: ../controller"},
		{"local chart name absent", chart, "name: nke-controller\n", ""},
		{"local chart empty name", chart, "name: nke-controller", "name: \"\""},
		{"local chart numeric name", chart, "name: nke-controller", "name: 42"},
		{"local chart null name", chart, "name: nke-controller", "name: null"},
		{"local chart oversized name", chart, "name: nke-controller", "name: " + strings.Repeat("a", 254)},
		{"local display alias unsafe", controller, "name: nke-controller", "name: ../controller"},
		{"local chart version absent", chart, "version: \"0.0.0\"\n", ""},
		{"local chart version range", chart, "version: \"0.0.0\"", "version: \"~0.0.0\""},
		{"local chart version numeric", chart, "version: \"0.0.0\"", "version: 1"},
		{"local chart path", controller, "path: ../../controller/deploy/charts/controller", "path: ../../other"},
		{"unknown local metadata", controller, "chart:\n", "chart:\n  command: execute\n"},
		{"artifact mapping escape", controller, "artifact: controller-chart", "artifact: ../controller-chart"},
		{"artifact mapping empty", controller, "artifact: controller-chart", "artifact: \"\""},
		{"artifact mapping null", controller, "artifact: controller-chart", "artifact: null"},
		{"artifact mapping numeric", controller, "artifact: controller-chart", "artifact: 1"},
		{"image mapping whitespace", controller, "imageArtifact: cwe", "imageArtifact: \"cwe image\""},
		{"profiles mapping reference", controller, "profilesArtifact: controller-profiles", "profilesArtifact: registry.example/profiles"},
		{"external artifact mapping", "deploy/nico/spire/release.yaml", "chart:\n", "chart:\n  artifact: spire\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, tree := nvkeBetaPinFixture(t)
			replaceNVKEFixture(t, tree, tc.file, tc.old, tc.replacement)
			versions, err := deriveNVKEPinSet(t.Context(), tree, reg, "nvke")
			assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
			if versions != nil {
				t.Fatal("failed beta derivation returned partial versions")
			}
		})
	}
}

func TestNVKEPinSetAllowsReviewedSourceMember(t *testing.T) {
	reg, tree := nvkePinFixture(t)
	set := reg.Sources["nvke"].PinSet
	set.Members["spire"] = PinSetMember{Source: "spire-source", Chart: &HelmChart{Name: "spire", Repository: "https://spiffe.github.io/helm-charts-hardened/"}}
	reg.Sources["spire-source"] = Source{Repo: "https://github.com/spiffe/helm-charts-hardened", PinnedBy: "nvke", TagPrefix: "spire-"}
	versions, err := deriveNVKEPinSet(t.Context(), tree, reg, "nvke")
	if err != nil || versions["spire"] != "0.30.1" {
		t.Fatalf("source chart version %v, %v", versions, err)
	}
}

func TestNVKEPinSetRejectsUnaccountedMembershipBeforeFollowingReferences(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*testing.T, *registryDoc, *nvkeFixtureTree)
	}{
		{"new external member", func(_ *testing.T, _ *registryDoc, tree *nvkeFixtureTree) {
			tree.files[nvkeFixtureDeploy] = append(tree.files[nvkeFixtureDeploy], []byte("  - {name: unknown-chart, type: helm}\n")...)
		}},
		{"new nonchart member", func(_ *testing.T, _ *registryDoc, tree *nvkeFixtureTree) {
			tree.files[nvkeFixtureDeploy] = append(tree.files[nvkeFixtureDeploy], []byte("  - {name: unknown-config, type: template}\n")...)
		}},
		{"removed chart", func(t *testing.T, _ *registryDoc, tree *nvkeFixtureTree) {
			replaceNVKEFixture(t, tree, nvkeFixtureDeploy, "  - {name: spire, type: helm}\n", "")
		}},
		{"removed accounted item", func(t *testing.T, _ *registryDoc, tree *nvkeFixtureTree) {
			replaceNVKEFixture(t, tree, nvkeFixtureDeploy, "  - {name: capnico, type: manifest}\n", "")
		}},
		{"changed chart type", func(t *testing.T, _ *registryDoc, tree *nvkeFixtureTree) {
			replaceNVKEFixture(t, tree, nvkeFixtureDeploy, "name: spire, type: helm", "name: spire, type: manifest")
		}},
		{"changed accounted type", func(t *testing.T, _ *registryDoc, tree *nvkeFixtureTree) {
			replaceNVKEFixture(t, tree, nvkeFixtureDeploy, "name: capnico, type: manifest", "name: capnico, type: helm")
		}},
		{"unknown type", func(t *testing.T, _ *registryDoc, tree *nvkeFixtureTree) {
			replaceNVKEFixture(t, tree, nvkeFixtureDeploy, "name: capnico, type: manifest", "name: capnico, type: execute")
		}},
		{"duplicate item", func(_ *testing.T, _ *registryDoc, tree *nvkeFixtureTree) {
			tree.files[nvkeFixtureDeploy] = append(tree.files[nvkeFixtureDeploy], []byte("  - {name: spire, type: helm}\n")...)
		}},
		{"path traversal", func(t *testing.T, _ *registryDoc, tree *nvkeFixtureTree) {
			replaceNVKEFixture(t, tree, nvkeFixtureDeploy, "name: capnico", "name: ../capnico")
		}},
		{"unsupported item path", func(t *testing.T, _ *registryDoc, tree *nvkeFixtureTree) {
			replaceNVKEFixture(t, tree, nvkeFixtureDeploy, "name: capnico, type: manifest", "name: capnico, type: manifest, path: elsewhere")
		}},
		{"extra YAML document", func(_ *testing.T, _ *registryDoc, tree *nvkeFixtureTree) {
			tree.files[nvkeFixtureDeploy] = append(tree.files[nvkeFixtureDeploy], []byte("---\nitems: []\n")...)
		}},
		{"anchors", func(t *testing.T, _ *registryDoc, tree *nvkeFixtureTree) {
			replaceNVKEFixture(t, tree, nvkeFixtureDeploy, "items:", "items: &items")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, tree := nvkePinFixture(t)
			tc.edit(t, reg, tree)
			versions, err := deriveNVKEPinSet(t.Context(), tree, reg, "nvke")
			assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
			if versions != nil || !reflect.DeepEqual(tree.reads, []string{nvkeFixtureDeploy}) {
				t.Fatalf("failure exposed versions %v or followed item references %v", versions, tree.reads)
			}
		})
	}
}

func TestNVKEPinSetRejectsChartIdentityAndShapeChanges(t *testing.T) {
	const spire = "controller/deploy/nke-sbom/spire/release.yaml"
	const controller = "controller/deploy/nke-sbom/controller/release.yaml"
	for _, tc := range []struct {
		name string
		edit func(*testing.T, *registryDoc, *nvkeFixtureTree)
	}{
		{"missing release", func(_ *testing.T, _ *registryDoc, tree *nvkeFixtureTree) { delete(tree.files, spire) }},
		{"different chart name", func(t *testing.T, _ *registryDoc, tree *nvkeFixtureTree) {
			replaceNVKEFixture(t, tree, spire, "  name: spire\n", "  name: other\n")
		}},
		{"different repository", func(t *testing.T, _ *registryDoc, tree *nvkeFixtureTree) {
			replaceNVKEFixture(t, tree, spire, "repository: https://spiffe.github.io/helm-charts-hardened/", "repository: https://different.example/charts")
		}},
		{"version range", func(t *testing.T, _ *registryDoc, tree *nvkeFixtureTree) {
			replaceNVKEFixture(t, tree, spire, "version: \"0.30.1\"", "version: \"~0.30.1\"")
		}},
		{"external version omitted", func(t *testing.T, _ *registryDoc, tree *nvkeFixtureTree) {
			replaceNVKEFixture(t, tree, spire, "  version: \"0.30.1\"\n", "")
		}},
		{"both sources", func(t *testing.T, _ *registryDoc, tree *nvkeFixtureTree) {
			replaceNVKEFixture(t, tree, spire, "chart:\n", "chart:\n  path: ../charts/controller\n")
		}},
		{"external becomes local", func(t *testing.T, _ *registryDoc, tree *nvkeFixtureTree) {
			replaceNVKEFixture(t, tree, spire, "repository: https://spiffe.github.io/helm-charts-hardened/", "path: ../charts/controller")
		}},
		{"local becomes external", func(t *testing.T, _ *registryDoc, tree *nvkeFixtureTree) {
			replaceNVKEFixture(t, tree, controller, "path: ../charts/controller", "repository: https://different.example/charts")
		}},
		{"local chart path changes", func(t *testing.T, _ *registryDoc, tree *nvkeFixtureTree) {
			replaceNVKEFixture(t, tree, controller, "path: ../charts/controller", "path: ../charts/other")
		}},
		{"missing local chart", func(_ *testing.T, _ *registryDoc, tree *nvkeFixtureTree) {
			delete(tree.files, "controller/deploy/charts/controller/Chart.yaml")
		}},
		{"local chart invalid name", func(t *testing.T, _ *registryDoc, tree *nvkeFixtureTree) {
			replaceNVKEFixture(t, tree, "controller/deploy/charts/controller/Chart.yaml", "name: nke-controller", "name: wrong/controller")
		}},
		{"extra release field", func(t *testing.T, _ *registryDoc, tree *nvkeFixtureTree) {
			replaceNVKEFixture(t, tree, spire, "chart:\n", "chart:\n  command: run\n")
		}},
		{"duplicate chart version", func(t *testing.T, _ *registryDoc, tree *nvkeFixtureTree) {
			replaceNVKEFixture(t, tree, spire, "chart:\n", "chart:\n  version: \"0.30.1\"\n")
		}},
		{"missing producer chart identity", func(_ *testing.T, reg *registryDoc, _ *nvkeFixtureTree) {
			reg.Components["spire-chart"].Prebuilt.Chart = nil
		}},
		{"missing producer", func(_ *testing.T, reg *registryDoc, _ *nvkeFixtureTree) { delete(reg.Components, "spire-chart") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, tree := nvkePinFixture(t)
			tc.edit(t, reg, tree)
			versions, err := deriveNVKEPinSet(t.Context(), tree, reg, "nvke")
			assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
			if versions != nil {
				t.Fatal("failed derivation returned partial versions")
			}
		})
	}
}

func TestNVKEPinSetLocalChartOwnsOptionalReleaseVersion(t *testing.T) {
	const release = "controller/deploy/nke-sbom/controller/release.yaml"
	const chart = "controller/deploy/charts/controller/Chart.yaml"
	for _, tc := range []struct {
		name string
		edit func(*testing.T, *nvkeFixtureTree)
		want e.Code
	}{
		{"source-owned version", func(*testing.T, *nvkeFixtureTree) {}, ""},
		{"missing chart version", func(t *testing.T, tree *nvkeFixtureTree) {
			replaceNVKEFixture(t, tree, chart, "version: \"0.0.0\"\n", "")
		}, e.CodeInvalidCatalog},
		{"invalid chart version", func(t *testing.T, tree *nvkeFixtureTree) {
			replaceNVKEFixture(t, tree, chart, "version: \"0.0.0\"", "version: \"~0.0.0\"")
		}, e.CodeInvalidCatalog},
		{"unknown artifact field", func(t *testing.T, tree *nvkeFixtureTree) {
			replaceNVKEFixture(t, tree, release, "chart:\n", "chart:\n  arbitraryArtifact: other\n")
		}, e.CodeInvalidCatalog},
		{"invalid artifact name", func(t *testing.T, tree *nvkeFixtureTree) {
			replaceNVKEFixture(t, tree, release, "artifact: controller-chart", "artifact: ../other")
		}, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, tree := nvkePinFixture(t)
			replaceNVKEFixture(t, tree, release, "  version: \"0.0.0\"\n", "")
			replaceNVKEFixture(t, tree, release, "chart:\n", "chart:\n  artifact: controller-chart\n  imageArtifact: cwe\n  profilesArtifact: controller-profiles\n")
			tc.edit(t, tree)
			versions, err := deriveNVKEPinSet(t.Context(), tree, reg, "nvke")
			if tc.want != "" {
				assertCatalogContractCode(t, err, tc.want)
			} else if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" && (len(versions) != 4 || versions["spire"] != "0.30.1") {
				t.Fatal("local chart metadata changed external chart selections")
			}
		})
	}
}

func TestNVKEPinSetSchemaAndCancellationFailBeforeRead(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*PinSet)
	}{
		{"member selector", func(s *PinSet) { m := s.Members["spire"]; m.Path = "chart.version"; s.Members["spire"] = m }},
		{"both declarations", func(s *PinSet) { s.Accounted["spire"] = PinSetItem{Type: "helm", Path: "../charts/controller"} }},
		{"unknown accounted kind", func(s *PinSet) { s.Accounted["capnico"] = PinSetItem{Type: "unknown"} }},
		{"manifest chart path", func(s *PinSet) { s.Accounted["capnico"] = PinSetItem{Type: "manifest", Path: "elsewhere"} }},
		{"escaping local path", func(s *PinSet) { s.Accounted["controller"] = PinSetItem{Type: "helm", Path: "../../../../outside"} }},
		{"absolute local path", func(s *PinSet) { s.Accounted["controller"] = PinSetItem{Type: "helm", Path: "/tmp/chart"} }},
		{"source lacks chart identity", func(s *PinSet) { s.Members["spire"] = PinSetMember{Source: "spire-source"} }},
		{"artifact repeats chart identity", func(s *PinSet) {
			s.Members["spire"] = PinSetMember{Artifact: "spire-chart", Chart: &HelmChart{Name: "spire", Repository: "https://example.com"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, tree := nvkePinFixture(t)
			tc.edit(reg.Sources["nvke"].PinSet)
			_, err := deriveNVKEPinSet(t.Context(), tree, reg, "nvke")
			assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
			if len(tree.reads) != 0 {
				t.Fatal("read pinned tree before schema admission")
			}
		})
	}
	reg, tree := nvkePinFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := deriveNVKEPinSet(ctx, tree, reg, "nvke")
	assertCatalogContractCode(t, err, e.CodeTimeout)
	if len(tree.reads) != 0 {
		t.Fatal("read after cancellation")
	}
}

type failedPinTree struct{ err error }

func (tree failedPinTree) ReadFile(context.Context, string) ([]byte, error) { return nil, tree.err }
func (failedPinTree) Close() error                                          { return nil }

func TestPinSetDerivationPreservesOperationalErrors(t *testing.T) {
	for _, format := range []string{"values", "nvke-deploy-v1"} {
		for _, tc := range []struct {
			name        string
			input, want e.Code
		}{
			{"missing file", e.CodeInvalidCatalog, e.CodePinSetMismatch},
			{"internal timeout", e.CodeTimeout, e.CodeTimeout},
			{"fetch failure", e.CodeFetchFailed, e.CodeFetchFailed},
			{"verification failure", e.CodeVerifyFailed, e.CodeVerifyFailed},
		} {
			t.Run(format+"/"+tc.name, func(t *testing.T) {
				reg, _ := nvkePinFixture(t)
				reg.Sources["nvke"].PinSet.Format = format
				input := e.New(tc.input, "read failure")
				_, err := derivePinSetTree(t.Context(), failedPinTree{input}, reg, "nvke", Pin{})
				assertCatalogContractCode(t, err, tc.want)
				if tc.input != e.CodeInvalidCatalog && err != input {
					t.Fatal("operational error was replaced")
				}
			})
		}
	}
}

func TestNVKEPinSetLocalChartMetadata(t *testing.T) {
	const releaseFile = "controller/deploy/nke-sbom/controller/release.yaml"
	const chartFile = "controller/deploy/charts/controller/Chart.yaml"
	for _, tc := range []struct {
		name, version, chartName, reviewedName string
		valid                                  bool
	}{
		{"source version", "0.0.0", "nke-controller", "", true},
		{"reviewed rename", "0.0.0", "nvke-controller", "nvke-controller", true},
		{"unreviewed rename", "0.0.0", "nvke-controller", "", false},
		{"wrong reviewed name", "0.0.0", "other-controller", "nvke-controller", false},
		{"invalid version", "latest", "nke-controller", "", false},
		{"range version", "1.x", "nke-controller", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, tree := nvkePinFixture(t)
			replaceNVKEFixture(t, tree, releaseFile, "  version: \"0.0.0\"\n", "")
			replaceNVKEFixture(t, tree, releaseFile, "chart:\n", "chart:\n  artifact: controller-chart\n  imageArtifact: cwe\n  profilesArtifact: controller-profiles\n")
			tree.files[chartFile] = []byte("name: " + tc.chartName + "\nversion: \"" + tc.version + "\"\n")
			item := reg.Sources["nvke"].PinSet.Accounted["controller"]
			item.ChartName = tc.reviewedName
			reg.Sources["nvke"].PinSet.Accounted["controller"] = item
			versions, err := deriveNVKEPinSet(t.Context(), tree, reg, "nvke")
			if !tc.valid {
				assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
				return
			}
			if err != nil || versions["spire"] != "0.30.1" {
				t.Fatalf("versions=%v error=%v", versions, err)
			}
		})
	}
}
