package catalog

import (
	"testing"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestValidateHelmValues(t *testing.T) {
	chart := func(mutate func(*Component)) Component {
		c := Component{Platforms: []string{"linux/amd64"}, FromSource: true, OutputType: "tarball", BuilderType: BuilderOCIHermetic,
			Helm: &HelmSpec{Values: []HelmValue{{Path: "image.repository", Value: "{{.Environment.Publication.RepositoryPrefix}}/x"}}}}
		if mutate != nil {
			mutate(&c)
		}
		return c
	}
	for _, tc := range []struct {
		name    string
		mutate  func(*Component)
		wantErr bool
	}{
		{name: "valid"},
		{name: "not from source", mutate: func(c *Component) { c.FromSource = false; c.ExclusionReason = "x" }, wantErr: true},
		{name: "not a tarball", mutate: func(c *Component) { c.OutputType = "oci-image" }, wantErr: true},
		{name: "empty values", mutate: func(c *Component) { c.Helm.Values = nil }, wantErr: true},
		{name: "bad path", mutate: func(c *Component) { c.Helm.Values[0].Path = "image..repository" }, wantErr: true},
		{name: "duplicate path", mutate: func(c *Component) { c.Helm.Values = append(c.Helm.Values, c.Helm.Values[0]) }, wantErr: true},
		{name: "bad template", mutate: func(c *Component) { c.Helm.Values[0].Value = "{{.Environment" }, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateHelmSpec("chart", chart(tc.mutate))
			if tc.wantErr {
				if catalogErrorCode(err) != nkderr.CodeInvalidCatalog {
					t.Fatalf("error = %v, want %s", err, nkderr.CodeInvalidCatalog)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDecodeRegistryLoadsHelmValues(t *testing.T) {
	const registry = `apiVersion: nkd.dev/catalog/v3
kind: SourceBuildRegistry
metadata:
  name: test
components:
  chart:
    fromSource: true
    source: src
    artifactPath: charts/chart.tgz
    writeDir: charts
    buildCommand: ['package']
    outputType: tarball
    platforms: [linux/amd64]
    helm:
      values:
      - path: image.repository
        value: '{{.Environment.Publication.RepositoryPrefix}}/chart-{{.Arch}}'
sources:
  src:
    repo: ssh://git@github.com/example/src.git
    constraint: v1.0.0
    toolchainImage: alpine@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
`
	reg, err := decodeRegistry([]byte(registry), "test")
	if err != nil {
		t.Fatal(err)
	}
	helm := reg.Components["chart"].Helm
	if helm == nil || len(helm.Values) != 1 || helm.Values[0].Path != "image.repository" {
		t.Fatalf("helm = %+v", helm)
	}
	clone := CloneHelmSpec(helm)
	clone.Values[0].Path = "changed"
	if helm.Values[0].Path != "image.repository" {
		t.Fatal("CloneHelmSpec shares the Values slice")
	}
	if CloneHelmSpec(nil) != nil {
		t.Fatal("CloneHelmSpec(nil) must be nil")
	}
}
