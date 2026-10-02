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
