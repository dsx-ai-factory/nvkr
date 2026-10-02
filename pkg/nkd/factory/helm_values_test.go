package factory

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func helmValuesBuild(values ...catalog.HelmValue) (BuildPlan, []string, map[string]Artifact) {
	build := BuildPlan{Name: "chart-arm64", Platform: "linux/arm64", ResolvedComponents: []catalog.NamedComponent{
		{Name: "chart", Component: catalog.Component{Helm: &catalog.HelmSpec{Values: values}}}}}
	return build, []string{"artifact/chart"}, map[string]Artifact{"artifact/chart": {Kind: "tarball", Component: "chart"}}
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want fragment %q", err, want)
	}
}

func TestRenderHelmValues(t *testing.T) {
	published := authored.Environment{Metadata: authored.Metadata{Name: "lab"}, Publication: &authored.Publication{RepositoryPrefix: "registry.test/team", Anonymous: true}}
	for _, tc := range []struct {
		name    string
		env     authored.Environment
		values  []catalog.HelmValue
		want    []HelmValueOverride
		wantErr string
	}{
		{name: "sorted and rendered", env: published, values: []catalog.HelmValue{
			{Path: "z.tag", Value: "{{.Output}}"},
			{Path: "image.repository", Value: "{{.Environment.Publication.RepositoryPrefix}}/ctl-{{.Arch}}"},
		}, want: []HelmValueOverride{{Path: "image.repository", Value: "registry.test/team/ctl-arm64"}, {Path: "z.tag", Value: "chart-arm64"}}},
		{name: "no publication", env: authored.Environment{}, values: []catalog.HelmValue{{Path: "image.repository", Value: "{{.Environment.Publication.RepositoryPrefix}}/x"}}, wantErr: "not set"},
		{name: "environment name is not exposed", env: published, values: []catalog.HelmValue{{Path: "a", Value: "{{.Environment.Name}}"}}, wantErr: "not set"},
		{name: "unknown field", env: published, values: []catalog.HelmValue{{Path: "a", Value: "{{.Environment.Signing.Identity}}"}}, wantErr: "not set"},
		{name: "empty render", env: published, values: []catalog.HelmValue{{Path: "a", Value: "{{if .Arch}}{{end}}"}}, wantErr: "non-empty"},
		{name: "control character", env: published, values: []catalog.HelmValue{{Path: "a", Value: "x{{printf \"\\n\"}}y"}}, wantErr: "control characters"},
		{name: "oversized", env: published, values: []catalog.HelmValue{{Path: "a", Value: "{{printf \"%5000s\" \"x\"}}"}}, wantErr: "too large"},
		{name: "literal", env: authored.Environment{}, values: []catalog.HelmValue{{Path: "a", Value: "x"}}, want: []HelmValueOverride{{Path: "a", Value: "x"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			build, inputs, artifacts := helmValuesBuild(tc.values...)
			got, err := renderHelmValues(tc.env, build, inputs, artifacts)
			if tc.wantErr != "" {
				assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
				assertErrorContains(t, err, tc.wantErr)
				return
			}
			if err != nil || len(got) != len(tc.want) {
				t.Fatalf("got %v, %v", got, err)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestRenderHelmValuesRejectsUnsupportedPlatform(t *testing.T) {
	build, inputs, artifacts := helmValuesBuild(catalog.HelmValue{Path: "a", Value: "x"})
	build.Platform = "linux/arm64/v8"
	_, err := renderHelmValues(authored.Environment{}, build, inputs, artifacts)
	assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
	assertErrorContains(t, err, "unsupported platform")
}

func TestChartHelmSpecSelection(t *testing.T) {
	build, inputs, artifacts := helmValuesBuild(catalog.HelmValue{Path: "a", Value: "x"})
	spec, err := chartHelmSpec(build, inputs, artifacts)
	if err != nil || spec == nil || len(spec.Values) != 1 {
		t.Fatalf("spec = %v, %v", spec, err)
	}
	if spec, err := chartHelmSpec(build, nil, artifacts); spec != nil || err != nil {
		t.Fatalf("no inputs: spec = %v, %v", spec, err)
	}
	build.ResolvedComponents = append(build.ResolvedComponents, catalog.NamedComponent{Name: "other", Component: catalog.Component{Helm: &catalog.HelmSpec{}}})
	artifacts["artifact/other"] = Artifact{Kind: "tarball", Component: "other"}
	_, err = chartHelmSpec(build, []string{"artifact/chart", "artifact/other"}, artifacts)
	assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
	assertErrorContains(t, err, "more than one chart")
}

func TestRewriteHelmValues(t *testing.T) {
	const values = "image:\n  repository: old\nreplicas: 1\nenabled: true\nlist: [a]\nempty:\n"
	for _, tc := range []struct {
		name      string
		overrides []HelmValueOverride
		wantErr   string
		contains  string
	}{
		{name: "replaces scalar", overrides: []HelmValueOverride{{Path: "image.repository", Value: "new"}}, contains: "repository: new"},
		{name: "unknown path", overrides: []HelmValueOverride{{Path: "image.repo", Value: "new"}}, wantErr: "does not exist"},
		{name: "missing parent", overrides: []HelmValueOverride{{Path: "nope.repository", Value: "new"}}, wantErr: "not a mapping"},
		{name: "replaces mapping", overrides: []HelmValueOverride{{Path: "image", Value: "new"}}, wantErr: "not a string scalar"},
		{name: "scalar parent", overrides: []HelmValueOverride{{Path: "replicas.x", Value: "new"}}, wantErr: "not a mapping"},
		{name: "bool target", overrides: []HelmValueOverride{{Path: "enabled", Value: "false"}}, wantErr: "not a string scalar"},
		{name: "number target", overrides: []HelmValueOverride{{Path: "replicas", Value: "2"}}, wantErr: "not a string scalar"},
		{name: "list target", overrides: []HelmValueOverride{{Path: "list", Value: "x"}}, wantErr: "not a string scalar"},
		{name: "null target", overrides: []HelmValueOverride{{Path: "empty", Value: "x"}}, wantErr: "not a string scalar"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "values.yaml")
			if err := os.WriteFile(path, []byte(values), 0o644); err != nil {
				t.Fatal(err)
			}
			err := rewriteHelmValues(context.Background(), dir, tc.overrides)
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if tc.wantErr != "" {
				assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
				assertErrorContains(t, err, tc.wantErr)
				if string(raw) != values {
					t.Fatalf("values mutated on error: %s", raw)
				}
				return
			}
			if err != nil || !strings.Contains(string(raw), tc.contains) {
				t.Fatalf("%s, %v", raw, err)
			}
		})
	}
}

func TestRewriteHelmValuesRejectsUnsafeValuesFiles(t *testing.T) {
	override := []HelmValueOverride{{Path: "a", Value: "x"}}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string)
	}{
		{"missing", func(*testing.T, string) {}},
		{"symlink", func(t *testing.T, dir string) {
			target := filepath.Join(dir, "real.yaml")
			if err := os.WriteFile(target, []byte("a: old\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(dir, "values.yaml")); err != nil {
				t.Fatal(err)
			}
		}},
		{"oversized", func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "values.yaml"), []byte("a: "+strings.Repeat("x", maxHelmValuesBytes)+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(t, dir)
			err := rewriteHelmValues(context.Background(), dir, override)
			assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
			assertErrorContains(t, err, "regular values.yaml")
		})
	}
}

func TestRejectBoundImageOverride(t *testing.T) {
	for path, reject := range map[string]bool{"image.digest": true, "image.tag": true, "image.repository": true, "image": true, "imagePullPolicy": false, "other.image.tag": false} {
		err := rejectBoundImageOverride(path)
		if reject {
			assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
			assertErrorContains(t, err, "verified image binding")
		} else if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
}

func compileHelmValuesPlan(t *testing.T, format string, publication *authored.Publication) (*Plan, error) {
	t.Helper()
	t.Chdir(t.TempDir())
	stack, err := catalog.ApplyStack(&catalog.Catalog{Components: map[string]catalog.Component{
		"chart": {
			Platforms: []string{"linux/amd64"}, FromSource: true,
			BuilderType: catalog.BuilderOCIHermetic, OutputType: "tarball",
			Repo: "https://example.test/chart", Tag: "v1.0.0",
			Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40), SourceDate: 1,
			ToolchainImage: "example.test/chart@sha256:" + strings.Repeat("c", 64),
			BuildCommand:   []string{"sh", "package.sh"}, WriteDir: "out", ArtifactPath: "out/chart.tgz",
			Helm: &catalog.HelmSpec{Values: []catalog.HelmValue{
				{Path: "z.arch", Value: "{{.Arch}}"},
				{Path: "image.repository", Value: "{{.Environment.Publication.RepositoryPrefix}}/chart-{{.Arch}}"},
			}},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	definition := authored.Definition{
		APIVersion: authored.DefinitionAPIVersion, Kind: authored.DefinitionKind,
		Metadata: authored.Metadata{Name: "helm-values"}, Catalog: authored.Catalog{Base: "catalog"},
		Outputs: []authored.DefinitionOutput{{Name: "chart", Platform: "linux/amd64", Format: format, Components: []string{"chart"}}},
	}
	environment := authored.Environment{
		APIVersion: authored.EnvironmentAPIVersion, Kind: authored.EnvironmentKind,
		Metadata: authored.Metadata{Name: "test"}, RunsOn: "ubuntu-24.04", Publication: publication,
		UtilityContainer: fixtureUtilityContainer, UtilityPlatform: "linux/amd64",
	}
	return Compile(t.Context(), definition, environment, stack, "test")
}

func TestCompileRecordsHelmValuesOnHelmPackageOnly(t *testing.T) {
	published := &authored.Publication{RepositoryPrefix: "registry.test/team", Anonymous: true}
	plan, err := compileHelmValuesPlan(t, authored.OutputHelm, published)
	if err != nil {
		t.Fatal(err)
	}
	want := []HelmValueOverride{{Path: "image.repository", Value: "registry.test/team/chart-amd64"}, {Path: "z.arch", Value: "amd64"}}
	var packages int
	for _, node := range plan.Graph.Nodes {
		if node.Kind == NodePackage && node.PackageKind == PackageHelm {
			packages++
			if !reflect.DeepEqual(node.HelmValues, want) {
				t.Fatalf("HelmValues = %v, want %v", node.HelmValues, want)
			}
		} else if len(node.HelmValues) != 0 {
			t.Fatalf("node %s carries HelmValues", node.ID)
		}
	}
	if packages != 1 {
		t.Fatalf("Helm package nodes = %d", packages)
	}

	other, err := compileHelmValuesPlan(t, authored.OutputHelm, &authored.Publication{RepositoryPrefix: "registry.test/other", Anonymous: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Builds[0].BuildDigest != other.Builds[0].BuildDigest {
		t.Fatalf("Build Digest changed with the Environment: %s != %s", plan.Builds[0].BuildDigest, other.Builds[0].BuildDigest)
	}

	_, err = compileHelmValuesPlan(t, authored.OutputHelm, nil)
	assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
	assertErrorContains(t, err, "not set")
}

func TestCompileRejectsHelmValuesOnNonHelmPackage(t *testing.T) {
	_, err := compileHelmValuesPlan(t, authored.OutputExecutable, &authored.Publication{RepositoryPrefix: "registry.test/team", Anonymous: true})
	assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
	assertErrorContains(t, err, "only a Helm output applies")
}
