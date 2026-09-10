package cluster

import (
	"bytes"
	"errors"
	"testing"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

func TestCompositionInvalidBindingsHaveNoCanonicalIdentity(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ClusterComposition)
		code e.Code
	}{
		{"schema", func(c *ClusterComposition) { c.SchemaVersion = "unknown" }, e.CodeInvalidCatalog},
		{"cluster type", func(c *ClusterComposition) { c.ClusterType = "unknown" }, e.CodeUnsupported},
		{"CNI whitespace", func(c *ClusterComposition) { c.CNI = " bad " }, e.CodeInvalidCatalog},
		{"missing roles", func(c *ClusterComposition) { c.RoleBindings = nil }, e.CodeInvalidCatalog},
		{"unknown role", func(c *ClusterComposition) { c.RoleBindings[0].Role = "unknown" }, e.CodeUnsupported},
		{"duplicate role", func(c *ClusterComposition) { c.RoleBindings = append(c.RoleBindings, c.RoleBindings[0]) }, e.CodeInvalidCatalog},
		{"empty node categories", func(c *ClusterComposition) { c.RoleBindings[0].NodeLevelCategories = nil }, e.CodeInvalidCatalog},
		{"duplicate subset", func(c *ClusterComposition) { c.RoleBindings[0].Subset = []string{"kubelet", "kubelet"} }, e.CodeInvalidCatalog},
		{"noncanonical validation requirement", func(c *ClusterComposition) { c.ValidationRequirements = []string{" test "} }, e.CodeInvalidCatalog},
		{"recipe order", func(c *ClusterComposition) { c.Recipes[0], c.Recipes[1] = c.Recipes[1], c.Recipes[0] }, e.CodeInvalidCatalog},
		{"unselected recipe", func(c *ClusterComposition) { c.ClusterCategories = nil }, e.CodeInvalidCatalog},
		{"missing recipe", func(c *ClusterComposition) { c.Recipes = c.Recipes[:1] }, e.CodeNoMatch},
		{"recipe frame mismatch", func(c *ClusterComposition) { c.Recipes[1].Criteria.Arch = "arm64" }, e.CodeInvalidCatalog},
		{"missing activation", func(c *ClusterComposition) { c.Recipes[0].AICR = nil }, e.CodeInvalidCatalog},
		{"unnamed activation", func(c *ClusterComposition) { c.Recipes[0].AICRProfileIdentity = "" }, e.CodeInvalidCatalog},
		{"bundle on node recipe", func(c *ClusterComposition) { c.Recipes[1].AICRBundle = c.Recipes[0].AICRBundle }, e.CodeInvalidCatalog},
		{"missing components", func(c *ClusterComposition) { c.Recipes[1].Components = nil }, e.CodeInvalidCatalog},
		{"missing bundle", func(c *ClusterComposition) { c.Recipes[0].AICRBundle = nil }, e.CodeInvalidCatalog},
		{"unsupported deployer", func(c *ClusterComposition) { c.Recipes[0].AICRBundle.Deployer = "unknown" }, e.CodeUnsupported},
		{"storage whitespace", func(c *ClusterComposition) { c.Recipes[0].AICRBundle.StorageClass = " gp3 " }, e.CodeInvalidCatalog},
		{"malformed selector", func(c *ClusterComposition) { c.Recipes[0].AICRBundle.WorkloadSelectors = []string{"training"} }, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := tenantAcceleratedComposition()
			if err := c.Validate(); err != nil {
				t.Fatal(err)
			}
			tc.edit(c)
			for _, check := range []func() error{
				c.Validate,
				func() error {
					raw, err := c.Marshal()
					if len(raw) != 0 {
						t.Fatal("invalid composition produced signing bytes")
					}
					return err
				},
				func() error {
					digest, err := c.Digest()
					if digest != "" {
						t.Fatal("invalid composition produced identity")
					}
					return err
				},
			} {
				var coded *e.Error
				if err := check(); !errors.As(err, &coded) || coded.Code != tc.code {
					t.Fatalf("error = %v; want %s", err, tc.code)
				}
			}
		})
	}
}

func TestCompositionNormalizationCanonicalizesAllSelections(t *testing.T) {
	c := tenantAcceleratedComposition()
	c.RoleBindings = append(c.RoleBindings, RoleBinding{Role: "control-plane", NodeLevelCategories: []string{"kubernetes"}, Subset: []string{"kubelet"}})
	c.ValidationRequirements = []string{"zeta", "alpha"}
	bundle := c.Recipes[0].AICRBundle
	bundle.AcceleratedNodeSelectors = []string{"z=2", "a=1"}
	bundle.AcceleratedNodeTolerations = []string{"z=2", "a=1"}
	bundle.WorkloadSelectors = []string{"z=2", "a=1"}
	c.Recipes[1].Components = []string{"z-tool", "kubelet"}
	c.Recipes[0], c.Recipes[1] = c.Recipes[1], c.Recipes[0]
	c.Normalize()
	raw, err := c.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	first, err := c.Digest()
	if err != nil {
		t.Fatal(err)
	}
	c.Normalize()
	again, err := c.Marshal()
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatalf("normalization is not idempotent: %v", err)
	}
	second, err := c.Digest()
	if err != nil || first != second {
		t.Fatalf("normalization changed identity: %v", err)
	}
	if c.RoleBindings[0].Role != "control-plane" || c.Recipes[0].Category != "accelerated-runtime" || bundle.WorkloadSelectors[0] != "a=1" || c.Recipes[1].Components[0] != "kubelet" {
		t.Fatal("normalization omitted a selection")
	}
}
