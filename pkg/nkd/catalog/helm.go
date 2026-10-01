package catalog

import (
	"maps"
	"regexp"
	"slices"

	"github.com/NVIDIA/nke-recipes/internal/documentpath"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// HelmPackaging declares chart-specific image slots on a source producer.
// Definitions bind these slots to outputs; no output names or registry routes live here.
type HelmPackaging struct {
	Chart  string               `json:"chart"`
	Images map[string]HelmImage `json:"images"`
}
type HelmImage struct {
	Values HelmImageValues `json:"values"`
}
type HelmImageValues struct {
	Digest string `json:"digest"`
}

var helmNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$`)

// Validate admits explicit, non-overlapping chart mappings before filesystem work.
func (h HelmPackaging) Validate() error {
	if (h.Chart != "." && !helmNamePattern.MatchString(h.Chart)) || len(h.Images) == 0 || len(h.Images) > 64 {
		return e.New(e.CodeInvalidCatalog, "Helm binding requires a chart and 1..64 image slots")
	}
	values := map[string]bool{}
	for _, slot := range slices.Sorted(maps.Keys(h.Images)) {
		b := h.Images[slot]
		if !helmNamePattern.MatchString(slot) {
			return e.New(e.CodeInvalidCatalog, "invalid Helm image slot %q", slot)
		}
		p := b.Values.Digest
		if err := documentpath.Validate(p); err != nil {
			return err
		}
		if values[p] {
			return e.New(e.CodeInvalidCatalog, "overlapping Helm value binding %q", p)
		}
		values[p] = true
	}
	return nil
}

func validateHelmProducer(h *HelmPackaging, fromSource bool, prebuilt *Prebuilt, outputType string) error {
	if h == nil {
		return nil
	}
	if !fromSource || prebuilt != nil || outputType != "tarball" {
		return e.New(e.CodeInvalidCatalog, "Helm mappings require a from-source tarball producer")
	}
	return h.Validate()
}
