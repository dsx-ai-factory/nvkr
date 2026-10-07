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

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

const nkeFixtureDeploy = "controller/deploy/nke-sbom/nke-deploy.yaml"

type nkeFixtureTree struct {
	files map[string][]byte
	reads []string
}

func (tree *nkeFixtureTree) ReadFile(ctx context.Context, name string) ([]byte, error) {
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

func (*nkeFixtureTree) Close() error { return nil }

func nkePinFixture(t *testing.T) (*registryDoc, *nkeFixtureTree) {
	t.Helper()
	set := &PinSet{File: nkeFixtureDeploy, Format: "nke-deploy-v1", Members: map[string]PinSetMember{}, Accounted: map[string]PinSetItem{
		"nke-base-config":              {Type: "template"},
		"spire-tpm-pubhashes":          {Type: "manifest"},
		"spire-server-nodeport":        {Type: "manifest"},
		"spire-oidc-provider-nodeport": {Type: "manifest"},
		"capnico":                      {Type: "manifest"},
		"controller":                   {Type: "helm", Path: "../charts/controller"},
	}}
	reg := &registryDoc{Sources: map[string]Source{"nke": {PinSet: set}}, Components: map[string]rawComponent{}}
	tree := &nkeFixtureTree{files: map[string][]byte{}}
	files := []string{nkeFixtureDeploy, "controller/deploy/nke-sbom/controller/release.yaml", "controller/deploy/charts/controller/Chart.yaml"}
	for _, member := range []struct{ name, repository string }{
		{"spire", "https://spiffe.github.io/helm-charts-hardened/"},
		{"spire-crds", "https://spiffe.github.io/helm-charts-hardened/"},
		{"temporal", "https://go.temporal.io/helm-charts"},
		{"postgresql", "oci://registry-1.docker.io/bitnamicharts"},
	} {
		artifact := member.name + "-chart"
		set.Members[member.name] = PinSetMember{Artifact: artifact}
		reg.Components[artifact] = rawComponent{PinnedBy: "nke", Prebuilt: &Prebuilt{Chart: &HelmChart{Name: member.name, Repository: member.repository}}}
		files = append(files, "controller/deploy/nke-sbom/"+member.name+"/release.yaml")
	}
	for _, name := range files {
		body, err := os.ReadFile(filepath.Join("testdata/nke-pin-set", filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		tree.files[name] = body
	}
	return reg, tree
}

func replaceNKEFixture(t *testing.T, tree *nkeFixtureTree, file, old, replacement string) {
	t.Helper()
	raw := string(tree.files[file])
	if !strings.Contains(raw, old) {
		t.Fatalf("fixture %s lacks %q", file, old)
	}
	tree.files[file] = []byte(strings.Replace(raw, old, replacement, 1))
}

func TestNKEPinSetReadsActualUpstreamDeployMetadata(t *testing.T) {
	reg, tree := nkePinFixture(t)
	versions, err := deriveNKEPinSet(t.Context(), tree, reg, "nke")
	want := map[string]string{"spire": "0.30.1", "spire-crds": "0.6.1", "temporal": "0.74.0", "postgresql": "18.5.15"}
	if err != nil || !reflect.DeepEqual(versions, want) {
		t.Fatalf("derived versions = %v, %v; want %v", versions, err, want)
	}
	expectedReads := []string{nkeFixtureDeploy, "controller/deploy/charts/controller/Chart.yaml"}
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

func TestNKEPinSetAllowsReviewedSourceMember(t *testing.T) {
	reg, tree := nkePinFixture(t)
	set := reg.Sources["nke"].PinSet
	set.Members["spire"] = PinSetMember{Source: "spire-source", Chart: &HelmChart{Name: "spire", Repository: "https://spiffe.github.io/helm-charts-hardened/"}}
	reg.Sources["spire-source"] = Source{Repo: "https://github.com/spiffe/helm-charts-hardened", PinnedBy: "nke", TagPrefix: "spire-"}
	versions, err := deriveNKEPinSet(t.Context(), tree, reg, "nke")
	if err != nil || versions["spire"] != "0.30.1" {
		t.Fatalf("source chart version %v, %v", versions, err)
	}
}

func TestNKEPinSetRejectsUnaccountedMembershipBeforeFollowingReferences(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*testing.T, *registryDoc, *nkeFixtureTree)
	}{
		{"new external member", func(_ *testing.T, _ *registryDoc, tree *nkeFixtureTree) {
			tree.files[nkeFixtureDeploy] = append(tree.files[nkeFixtureDeploy], []byte("  - {name: unknown-chart, type: helm}\n")...)
		}},
		{"new nonchart member", func(_ *testing.T, _ *registryDoc, tree *nkeFixtureTree) {
			tree.files[nkeFixtureDeploy] = append(tree.files[nkeFixtureDeploy], []byte("  - {name: unknown-config, type: template}\n")...)
		}},
		{"removed chart", func(t *testing.T, _ *registryDoc, tree *nkeFixtureTree) {
			replaceNKEFixture(t, tree, nkeFixtureDeploy, "  - {name: spire, type: helm}\n", "")
		}},
		{"removed accounted item", func(t *testing.T, _ *registryDoc, tree *nkeFixtureTree) {
			replaceNKEFixture(t, tree, nkeFixtureDeploy, "  - {name: capnico, type: manifest}\n", "")
		}},
		{"changed chart type", func(t *testing.T, _ *registryDoc, tree *nkeFixtureTree) {
			replaceNKEFixture(t, tree, nkeFixtureDeploy, "name: spire, type: helm", "name: spire, type: manifest")
		}},
		{"changed accounted type", func(t *testing.T, _ *registryDoc, tree *nkeFixtureTree) {
			replaceNKEFixture(t, tree, nkeFixtureDeploy, "name: capnico, type: manifest", "name: capnico, type: helm")
		}},
		{"unknown type", func(t *testing.T, _ *registryDoc, tree *nkeFixtureTree) {
			replaceNKEFixture(t, tree, nkeFixtureDeploy, "name: capnico, type: manifest", "name: capnico, type: execute")
		}},
		{"duplicate item", func(_ *testing.T, _ *registryDoc, tree *nkeFixtureTree) {
			tree.files[nkeFixtureDeploy] = append(tree.files[nkeFixtureDeploy], []byte("  - {name: spire, type: helm}\n")...)
		}},
		{"path traversal", func(t *testing.T, _ *registryDoc, tree *nkeFixtureTree) {
			replaceNKEFixture(t, tree, nkeFixtureDeploy, "name: capnico", "name: ../capnico")
		}},
		{"unsupported item path", func(t *testing.T, _ *registryDoc, tree *nkeFixtureTree) {
			replaceNKEFixture(t, tree, nkeFixtureDeploy, "name: capnico, type: manifest", "name: capnico, type: manifest, path: elsewhere")
		}},
		{"extra YAML document", func(_ *testing.T, _ *registryDoc, tree *nkeFixtureTree) {
			tree.files[nkeFixtureDeploy] = append(tree.files[nkeFixtureDeploy], []byte("---\nitems: []\n")...)
		}},
		{"anchors", func(t *testing.T, _ *registryDoc, tree *nkeFixtureTree) {
			replaceNKEFixture(t, tree, nkeFixtureDeploy, "items:", "items: &items")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, tree := nkePinFixture(t)
			tc.edit(t, reg, tree)
			versions, err := deriveNKEPinSet(t.Context(), tree, reg, "nke")
			assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
			if versions != nil || !reflect.DeepEqual(tree.reads, []string{nkeFixtureDeploy}) {
				t.Fatalf("failure exposed versions %v or followed item references %v", versions, tree.reads)
			}
		})
	}
}

func TestNKEPinSetRejectsChartIdentityAndShapeChanges(t *testing.T) {
	const spire = "controller/deploy/nke-sbom/spire/release.yaml"
	const controller = "controller/deploy/nke-sbom/controller/release.yaml"
	for _, tc := range []struct {
		name string
		edit func(*testing.T, *registryDoc, *nkeFixtureTree)
	}{
		{"missing release", func(_ *testing.T, _ *registryDoc, tree *nkeFixtureTree) { delete(tree.files, spire) }},
		{"different chart name", func(t *testing.T, _ *registryDoc, tree *nkeFixtureTree) {
			replaceNKEFixture(t, tree, spire, "  name: spire\n", "  name: other\n")
		}},
		{"different repository", func(t *testing.T, _ *registryDoc, tree *nkeFixtureTree) {
			replaceNKEFixture(t, tree, spire, "repository: https://spiffe.github.io/helm-charts-hardened/", "repository: https://different.example/charts")
		}},
		{"version range", func(t *testing.T, _ *registryDoc, tree *nkeFixtureTree) {
			replaceNKEFixture(t, tree, spire, "version: \"0.30.1\"", "version: \"~0.30.1\"")
		}},
		{"both sources", func(t *testing.T, _ *registryDoc, tree *nkeFixtureTree) {
			replaceNKEFixture(t, tree, spire, "chart:\n", "chart:\n  path: ../charts/controller\n")
		}},
		{"external becomes local", func(t *testing.T, _ *registryDoc, tree *nkeFixtureTree) {
			replaceNKEFixture(t, tree, spire, "repository: https://spiffe.github.io/helm-charts-hardened/", "path: ../charts/controller")
		}},
		{"local becomes external", func(t *testing.T, _ *registryDoc, tree *nkeFixtureTree) {
			replaceNKEFixture(t, tree, controller, "path: ../charts/controller", "repository: https://different.example/charts")
		}},
		{"local chart path changes", func(t *testing.T, _ *registryDoc, tree *nkeFixtureTree) {
			replaceNKEFixture(t, tree, controller, "path: ../charts/controller", "path: ../charts/other")
		}},
		{"missing local chart", func(_ *testing.T, _ *registryDoc, tree *nkeFixtureTree) {
			delete(tree.files, "controller/deploy/charts/controller/Chart.yaml")
		}},
		{"local chart mismatches release", func(t *testing.T, _ *registryDoc, tree *nkeFixtureTree) {
			replaceNKEFixture(t, tree, "controller/deploy/charts/controller/Chart.yaml", "name: nke-controller", "name: wrong-controller")
		}},
		{"extra release field", func(t *testing.T, _ *registryDoc, tree *nkeFixtureTree) {
			replaceNKEFixture(t, tree, spire, "chart:\n", "chart:\n  command: run\n")
		}},
		{"duplicate chart version", func(t *testing.T, _ *registryDoc, tree *nkeFixtureTree) {
			replaceNKEFixture(t, tree, spire, "chart:\n", "chart:\n  version: \"0.30.1\"\n")
		}},
		{"missing producer chart identity", func(_ *testing.T, reg *registryDoc, _ *nkeFixtureTree) {
			reg.Components["spire-chart"].Prebuilt.Chart = nil
		}},
		{"missing producer", func(_ *testing.T, reg *registryDoc, _ *nkeFixtureTree) { delete(reg.Components, "spire-chart") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, tree := nkePinFixture(t)
			tc.edit(t, reg, tree)
			versions, err := deriveNKEPinSet(t.Context(), tree, reg, "nke")
			assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
			if versions != nil {
				t.Fatal("failed derivation returned partial versions")
			}
		})
	}
}

func TestNKEPinSetSchemaAndCancellationFailBeforeRead(t *testing.T) {
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
			reg, tree := nkePinFixture(t)
			tc.edit(reg.Sources["nke"].PinSet)
			_, err := deriveNKEPinSet(t.Context(), tree, reg, "nke")
			assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
			if len(tree.reads) != 0 {
				t.Fatal("read pinned tree before schema admission")
			}
		})
	}
	reg, tree := nkePinFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := deriveNKEPinSet(ctx, tree, reg, "nke")
	assertCatalogContractCode(t, err, e.CodeTimeout)
	if len(tree.reads) != 0 {
		t.Fatal("read after cancellation")
	}
}

type failedPinTree struct{ err error }

func (tree failedPinTree) ReadFile(context.Context, string) ([]byte, error) { return nil, tree.err }
func (failedPinTree) Close() error                                          { return nil }

func TestPinSetDerivationPreservesOperationalErrors(t *testing.T) {
	for _, format := range []string{"values", "nke-deploy-v1"} {
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
				reg, _ := nkePinFixture(t)
				reg.Sources["nke"].PinSet.Format = format
				input := e.New(tc.input, "read failure")
				_, err := derivePinSetTree(t.Context(), failedPinTree{input}, reg, "nke", Pin{})
				assertCatalogContractCode(t, err, tc.want)
				if tc.input != e.CodeInvalidCatalog && err != input {
					t.Fatal("operational error was replaced")
				}
			})
		}
	}
}
