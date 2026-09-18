package aicr

import (
	"reflect"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestBundleInputDefaultsAreCanonical(t *testing.T) {
	for _, input := range []*BundleInputs{nil, {}, {Deployer: "helm"}} {
		got, err := NormalizeBundleInputs(input)
		if err != nil || !reflect.DeepEqual(got, BundleInputs{Deployer: "helm"}) {
			t.Fatalf("effective bundle = %+v, %v", got, err)
		}
	}
}

func TestBundleInputNormalizationOwnsAndSortsEveryList(t *testing.T) {
	input := &BundleInputs{
		Deployer: "flux", StorageClass: "gp3",
		AcceleratedNodeSelectors:   []string{"z=1", "a=1"},
		AcceleratedNodeTolerations: []string{"z=1:NoSchedule", "a=1:NoSchedule"},
		WorkloadSelectors:          []string{"z=2", "a=2"},
	}
	before := input.Clone()
	normalized, err := NormalizeBundleInputs(input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*input, before) {
		t.Fatal("normalization changed authored input")
	}
	input.AcceleratedNodeSelectors[0] = "changed=1"
	input.AcceleratedNodeTolerations[0] = "changed=1"
	input.WorkloadSelectors[0] = "changed=1"
	want := BundleInputs{
		Deployer: "flux", StorageClass: "gp3",
		AcceleratedNodeSelectors:   []string{"a=1", "z=1"},
		AcceleratedNodeTolerations: []string{"a=1:NoSchedule", "z=1:NoSchedule"},
		WorkloadSelectors:          []string{"a=2", "z=2"},
	}
	if !reflect.DeepEqual(normalized, want) {
		t.Fatalf("effective settings did not preserve their own inputs: %+v", normalized)
	}
}

func TestBundleInputsRejectMalformedValuesWithoutPartialResult(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*BundleInputs)
	}{
		{"unknown deployer", func(b *BundleInputs) { b.Deployer = "future" }},
		{"padded deployer", func(b *BundleInputs) { b.Deployer = " helm" }},
		{"padded storage class", func(b *BundleInputs) { b.StorageClass = " gp3" }},
		{"control in storage class", func(b *BundleInputs) { b.StorageClass = "gp3\n" }},
		{"invalid UTF8", func(b *BundleInputs) { b.WorkloadSelectors = []string{"a=" + string([]byte{0xff})} }},
		{"empty selector", func(b *BundleInputs) { b.AcceleratedNodeSelectors = []string{""} }},
		{"no key", func(b *BundleInputs) { b.WorkloadSelectors = []string{"=value"} }},
		{"no equals", func(b *BundleInputs) { b.WorkloadSelectors = []string{"value"} }},
		{"duplicate selector", func(b *BundleInputs) { b.AcceleratedNodeSelectors = []string{"a=1", "a=1"} }},
		{"duplicate toleration", func(b *BundleInputs) { b.AcceleratedNodeTolerations = []string{"a=1:NoSchedule", "a=1:NoSchedule"} }},
		{"padded workload selector", func(b *BundleInputs) { b.WorkloadSelectors = []string{"a=1 "} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := BundleInputs{Deployer: "helm"}
			test.edit(&input)
			got, err := NormalizeBundleInputs(&input)
			if coded, ok := err.(*e.Error); !ok || coded.Code != e.CodeInvalidCatalog {
				t.Fatalf("malformed settings = %+v, %v; want coded failure", got, err)
			}
			if !reflect.DeepEqual(got, BundleInputs{}) {
				t.Fatalf("malformed settings returned partial result: %+v", got)
			}
		})
	}
}

func TestEffectiveBundleInputsRejectMissingDefaultsAndUnsortedLists(t *testing.T) {
	for _, input := range []BundleInputs{
		{},
		{Deployer: "helm", AcceleratedNodeSelectors: []string{"z=1", "a=1"}},
		{Deployer: "helm", AcceleratedNodeTolerations: []string{"z=1:NoSchedule", "a=1:NoSchedule"}},
		{Deployer: "helm", WorkloadSelectors: []string{"z=1", "a=1"}},
	} {
		if coded, ok := input.Validate().(*e.Error); !ok || coded.Code != e.CodeInvalidCatalog {
			t.Fatalf("effective input must be complete and canonical: %+v", input)
		}
	}
}
