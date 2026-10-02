package factory

import (
	"context"
	"os"
	"path/filepath"
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

func TestRenderHelmValues(t *testing.T) {
	published := authored.Environment{Metadata: authored.Metadata{Name: "lab"}, Publication: &authored.Publication{RepositoryPrefix: "registry.test/team"}}
	for _, tc := range []struct {
		name    string
		env     authored.Environment
		values  []catalog.HelmValue
		want    []HelmValueOverride
		wantErr bool
	}{
		{name: "sorted and rendered", env: published, values: []catalog.HelmValue{
			{Path: "z.tag", Value: "{{.Environment.Name}}"},
			{Path: "image.repository", Value: "{{.Environment.Publication.RepositoryPrefix}}/ctl-{{.Arch}}"},
		}, want: []HelmValueOverride{{Path: "image.repository", Value: "registry.test/team/ctl-arm64"}, {Path: "z.tag", Value: "lab"}}},
		{name: "no publication", env: authored.Environment{}, values: []catalog.HelmValue{{Path: "image.repository", Value: "{{.Environment.Publication.RepositoryPrefix}}/x"}}, wantErr: true},
		{name: "unknown field", env: published, values: []catalog.HelmValue{{Path: "a", Value: "{{.Environment.Signing.Identity}}"}}, wantErr: true},
		{name: "literal", env: authored.Environment{}, values: []catalog.HelmValue{{Path: "a", Value: "x"}}, want: []HelmValueOverride{{Path: "a", Value: "x"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			build, inputs, artifacts := helmValuesBuild(tc.values...)
			got, err := renderHelmValues(tc.env, build, inputs, artifacts)
			if tc.wantErr {
				assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
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

func TestRewriteHelmValues(t *testing.T) {
	const values = "image:\n  repository: old\nreplicas: 1\n"
	for _, tc := range []struct {
		name      string
		overrides []HelmValueOverride
		wantErr   bool
		contains  string
	}{
		{name: "replaces scalar", overrides: []HelmValueOverride{{Path: "image.repository", Value: "new"}}, contains: "repository: new"},
		{name: "unknown path", overrides: []HelmValueOverride{{Path: "image.repo", Value: "new"}}, wantErr: true},
		{name: "missing parent", overrides: []HelmValueOverride{{Path: "nope.repository", Value: "new"}}, wantErr: true},
		{name: "replaces mapping", overrides: []HelmValueOverride{{Path: "image", Value: "new"}}, wantErr: true},
		{name: "scalar parent", overrides: []HelmValueOverride{{Path: "replicas.x", Value: "new"}}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "values.yaml")
			if err := os.WriteFile(path, []byte(values), 0o644); err != nil {
				t.Fatal(err)
			}
			err := rewriteHelmValues(context.Background(), dir, tc.overrides)
			raw, _ := os.ReadFile(path)
			if tc.wantErr {
				assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
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
