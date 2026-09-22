package catalog

import (
	"context"
	"sort"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

const (
	lockAPIVersion = "nkd.dev/lock/v1"
	lockKind       = "PinLock"
)

// readSources returns the declared sources without requiring the lock.
func readSources(p DataProvider) (map[string]Source, error) {
	reg, err := readRegistry(p)
	if err != nil {
		return nil, err
	}
	return reg.Sources, nil
}

// ReadSourcesContext is ReadSources through the bounded context-aware provider
// contract.
func ReadSourcesContext(ctx context.Context, p DataProvider) (map[string]Source, error) {
	bound, err := BindContext(ctx, p)
	if err != nil {
		return nil, err
	}
	sources, err := readSources(bound)
	return sources, normalizeCatalogContextError(ctx, err, "catalog source read")
}

// PrebuiltTarget is a prebuilt component's acquisition, resolved against its version
// pin for `catalog pin-artifacts`: URL/Image are version-rendered (a binary URL still
// carries the {{.Arch}} token; an image ref does not), and Tag records the version the
// resolved digests will correspond to.
type PrebuiltTarget struct {
	Platforms []string
	Name      string
	Tag       string
	URL       string
	Image     string
}

// ReadPrebuiltTargets returns the prebuilt components of a registry with their
// acquisition refs version-rendered from the lock's source pins — WITHOUT requiring
// artifact digests to exist yet, so `catalog pin-artifacts` can resolve them on a first
// run (full Load would fail closed on the not-yet-pinned digests). Targets are returned
// in stable (sorted-by-name) order.
func readPrebuiltTargets(p DataProvider) ([]PrebuiltTarget, error) {
	reg, err := readRegistry(p)
	if err != nil {
		return nil, err
	}
	lock, err := readLock(p)
	if err != nil {
		return nil, err
	}
	if err := validateLockAgainstRegistry(reg, lock, false); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(reg.Components))
	for n := range reg.Components {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []PrebuiltTarget
	for _, name := range names {
		raw := reg.Components[name]
		if raw.Prebuilt == nil {
			continue
		}
		pin, ok := lock.Pins[raw.Source]
		if !ok {
			return nil, e.New(e.CodeUnpinnedSource,
				"prebuilt component %q: source %q has no pin in the lock (run: nkdctl catalog pin %s)", name, raw.Source, raw.Source)
		}
		url, err := renderRef("prebuilt.url", raw.Prebuilt.URL, pin)
		if err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "prebuilt component %q", name)
		}
		img, err := renderRef("prebuilt.image", raw.Prebuilt.Image, pin)
		if err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "prebuilt component %q", name)
		}
		out = append(out, PrebuiltTarget{Name: name, Tag: pin.Tag, URL: url, Image: img, Platforms: append([]string(nil), raw.Platforms...)})
	}
	return out, nil
}

// ReadPrebuiltTargetsContext is ReadPrebuiltTargets through the bounded
// context-aware provider contract.
func ReadPrebuiltTargetsContext(ctx context.Context, p DataProvider) ([]PrebuiltTarget, error) {
	bound, err := BindContext(ctx, p)
	if err != nil {
		return nil, err
	}
	targets, err := readPrebuiltTargets(bound)
	return targets, normalizeCatalogContextError(ctx, err, "catalog prebuilt-target read")
}
