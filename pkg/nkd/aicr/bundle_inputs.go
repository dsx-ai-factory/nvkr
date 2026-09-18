package aicr

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// BundleInputs contains the output-affecting settings passed to the upstream
// bundle renderer. They belong to one AICR output and participate in its build
// identity; they do not describe or authorize a deployment.
type BundleInputs struct {
	Deployer                   string   `json:"deployer"`
	StorageClass               string   `json:"storageClass,omitempty"`
	AcceleratedNodeSelectors   []string `json:"acceleratedNodeSelectors,omitempty"`
	AcceleratedNodeTolerations []string `json:"acceleratedNodeTolerations,omitempty"`
	WorkloadSelectors          []string `json:"workloadSelectors,omitempty"`
}

// Clone returns an owned copy, preserving exact values for subsequent validation.
func (b BundleInputs) Clone() BundleInputs {
	b.AcceleratedNodeSelectors = slices.Clone(b.AcceleratedNodeSelectors)
	b.AcceleratedNodeTolerations = slices.Clone(b.AcceleratedNodeTolerations)
	b.WorkloadSelectors = slices.Clone(b.WorkloadSelectors)
	return b
}

// NormalizeBundleInputs admits authored settings and returns their canonical
// effective form. Omitted settings mean Helm with no overrides. Normalization
// sorts owned lists but never drops duplicates or repairs invalid strings.
func NormalizeBundleInputs(input *BundleInputs) (BundleInputs, error) {
	b := BundleInputs{Deployer: "helm"}
	if input != nil {
		b = input.Clone()
		if b.Deployer == "" {
			b.Deployer = "helm"
		}
	}
	slices.Sort(b.AcceleratedNodeSelectors)
	slices.Sort(b.AcceleratedNodeTolerations)
	slices.Sort(b.WorkloadSelectors)
	if err := b.Validate(); err != nil {
		return BundleInputs{}, err
	}
	return b, nil
}

// Validate admits the complete effective settings carried by executable graphs.
// Unlike authored input normalization, it rejects omitted deployers and unsorted
// lists so a decoded or caller-mutated graph cannot silently change meaning.
func (b BundleInputs) Validate() error {
	switch b.Deployer {
	case "helm", "argocd", "argocd-helm", "flux", "helmfile":
	default:
		return e.New(e.CodeInvalidCatalog, "AICR bundle deployer %q is unsupported or missing", b.Deployer)
	}
	if b.StorageClass != "" && !canonicalBundleText(b.StorageClass) {
		return e.New(e.CodeInvalidCatalog, "AICR storageClass %q is not canonical", b.StorageClass)
	}
	for _, field := range []struct {
		name   string
		values []string
	}{
		{"acceleratedNodeSelectors", b.AcceleratedNodeSelectors},
		{"acceleratedNodeTolerations", b.AcceleratedNodeTolerations},
		{"workloadSelectors", b.WorkloadSelectors},
	} {
		previous := ""
		for _, value := range field.values {
			key, _, hasValue := strings.Cut(value, "=")
			if !canonicalBundleText(value) || !hasValue || key == "" || value <= previous {
				return e.New(e.CodeInvalidCatalog,
					"AICR %s must contain sorted unique canonical key=value entries", field.name)
			}
			previous = value
		}
	}
	return nil
}

func canonicalBundleText(value string) bool {
	return value != "" && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}
