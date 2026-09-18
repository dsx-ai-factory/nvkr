package buildgraph

import (
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/aicr"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func bundleCatalog() *catalog.Catalog {
	return &catalog.Catalog{AICRProfiles: map[string]catalog.AICRProfile{
		"runtime": {
			Identity: "runtime", Digest: "sha256:" + strings.Repeat("a", 64),
			GeneratorDigest: "sha256:" + strings.Repeat("b", 64),
			Service:         "eks", Accelerator: "h100", OS: "ubuntu", Intent: "training",
		},
	}}
}

func TestAICRBundleDefaultsAndOrderHaveOneBuildIdentity(t *testing.T) {
	for _, inputs := range [][]*aicr.BundleInputs{
		{nil, {}, {Deployer: "helm"}},
		{
			{WorkloadSelectors: []string{"b=2", "a=1"}},
			{Deployer: "helm", WorkloadSelectors: []string{"a=1", "b=2"}},
		},
	} {
		previous := ""
		for _, input := range inputs {
			resolved, err := Resolve(bundleCatalog(), Selection{Platform: "linux/amd64", AICRProfile: "runtime", Bundle: input})
			if err != nil {
				t.Fatal(err)
			}
			graph := mustCompile(t, resolved)
			if graph.AICR.Bundle.Deployer != "helm" {
				t.Fatal("effective renderer default omitted from graph")
			}
			digest, err := graph.Digest()
			if err != nil {
				t.Fatal(err)
			}
			if previous != "" && digest != previous {
				t.Fatal("equivalent rendering inputs received different identities")
			}
			previous = digest
		}
	}
}

func TestAICRBundleSettingsAffectBuildIdentity(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*aicr.BundleInputs)
	}{
		{"deployer", func(b *aicr.BundleInputs) { b.Deployer = "flux" }},
		{"storage class", func(b *aicr.BundleInputs) { b.StorageClass = "gp3" }},
		{"node selectors", func(b *aicr.BundleInputs) { b.AcceleratedNodeSelectors = []string{"gpu=true"} }},
		{"node tolerations", func(b *aicr.BundleInputs) { b.AcceleratedNodeTolerations = []string{"gpu=present:NoSchedule"} }},
		{"workload selectors", func(b *aicr.BundleInputs) { b.WorkloadSelectors = []string{"workload=training"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolved, err := Resolve(bundleCatalog(), Selection{Platform: "linux/amd64", AICRProfile: "runtime"})
			if err != nil {
				t.Fatal(err)
			}
			before, err := mustCompile(t, resolved).Digest()
			if err != nil {
				t.Fatal(err)
			}
			test.edit(resolved.Bundle)
			after, err := mustCompile(t, resolved).Digest()
			if err != nil || before == after {
				t.Fatalf("output-affecting rendering setting retained identity: %s, %v", after, err)
			}
		})
	}
}

func TestAICRSelectionAndGraphOwnBundleSettings(t *testing.T) {
	input := &aicr.BundleInputs{WorkloadSelectors: []string{"a=1"}}
	resolved, err := Resolve(bundleCatalog(), Selection{Platform: "linux/amd64", AICRProfile: "runtime", Bundle: input})
	if err != nil {
		t.Fatal(err)
	}
	input.WorkloadSelectors[0] = "changed=1"
	if resolved.Bundle.WorkloadSelectors[0] != "a=1" {
		t.Fatal("authored slice mutation changed resolved settings")
	}
	graph := mustCompile(t, resolved)
	resolved.Bundle.WorkloadSelectors[0] = "changed=2"
	if graph.AICR.Bundle.WorkloadSelectors[0] != "a=1" {
		t.Fatal("resolved slice mutation changed compiled graph")
	}
}

func TestAICRBundleAdmissionRejectsInvalidInputsAndMutations(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*Selection)
	}{
		{"ordinary output", func(s *Selection) { s.Components = []string{"tool"}; s.AICRProfile = "" }},
		{"invalid deployer", func(s *Selection) { s.Bundle.Deployer = "unknown" }},
		{"duplicate selector", func(s *Selection) { s.Bundle.WorkloadSelectors = []string{"a=1", "a=1"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			selection := Selection{Platform: "linux/amd64", AICRProfile: "runtime", Bundle: &aicr.BundleInputs{}}
			test.edit(&selection)
			resolved, err := Resolve(nil, selection)
			if resolved != nil || code(err) != e.CodeInvalidCatalog {
				t.Fatalf("invalid settings reached catalog admission: %+v, %v", resolved, err)
			}
		})
	}
	for _, test := range []struct {
		name string
		edit func(*Resolved)
	}{
		{"missing settings", func(r *Resolved) { r.Bundle = nil }},
		{"missing effective deployer", func(r *Resolved) { r.Bundle.Deployer = "" }},
		{"unsorted mutated settings", func(r *Resolved) { r.Bundle.WorkloadSelectors = []string{"z=1", "a=1"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolved, err := Resolve(bundleCatalog(), Selection{Platform: "linux/amd64", AICRProfile: "runtime"})
			if err != nil {
				t.Fatal(err)
			}
			test.edit(resolved)
			graph, err := Compile(resolved)
			if graph != nil || code(err) != e.CodeInvalidCatalog {
				t.Fatalf("mutated effective settings compiled: %+v, %v", graph, err)
			}
		})
	}
}
