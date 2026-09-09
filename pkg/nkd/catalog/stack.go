package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/internal/heldfile"
	"sigs.k8s.io/yaml"
)

const (
	OverlayAPIVersion = "nkd.dev/catalog-overlay/v1"
	OverlayKind       = "CatalogOverlay"
	OverlayMaxBytes   = int64(4 << 20)
)

// Overlay is one independently versioned, ordered mutation of a resolved catalog.
// RequiresCatalogDigest pins the exact input state, so reordering, omission, or
// replay against another base fails closed. Component replacements are deliberately
// whole-producer substitutions: no source-only fields can leak into a supplier lane.
type Overlay struct {
	APIVersion            string                          `json:"apiVersion"`
	Kind                  string                          `json:"kind"`
	Name                  string                          `json:"name"`
	RequiresCatalogDigest string                          `json:"requiresCatalogDigest"`
	Components            map[string]ComponentReplacement `json:"components"`
}

// ComponentReplacement replaces one producer while retaining its logical component
// name. Prebuilt is the common trusted-supplier form. Component is the escape hatch
// for a complete, already-resolved source/prebuilt/exclusion definition; exactly one
// form is required.
type ComponentReplacement struct {
	Version         string     `json:"version,omitempty"`
	SourceDateEpoch int64      `json:"sourceDateEpoch,omitempty"`
	OutputType      string     `json:"outputType,omitempty"`
	Roles           []string   `json:"roles,omitempty"`
	Prebuilt        *Prebuilt  `json:"prebuilt,omitempty"`
	Component       *Component `json:"component,omitempty"`
}

// AppliedOverlay is the immutable provenance row for one applied stack member.
type AppliedOverlay struct {
	Name          string `json:"name"`
	OverlayDigest string `json:"overlayDigest"`
	InputDigest   string `json:"inputCatalogDigest"`
	OutputDigest  string `json:"outputCatalogDigest"`
}

// Stack is a fully resolved catalog plus its ordered overlay provenance. It remains
// definition data, not signature authority; callers authenticate overlay files at
// their own boundary when policy requires it.
type Stack struct {
	Catalog *Catalog         `json:"-"`
	Digest  string           `json:"catalogDigest"`
	Applied []AppliedOverlay `json:"appliedOverlays,omitempty"`
}

type catalogSnapshot struct {
	SchemaVersion string               `json:"schemaVersion"`
	APIVersion    string               `json:"apiVersion"`
	Kind          string               `json:"kind"`
	Metadata      Metadata             `json:"metadata"`
	Sources       map[string]Source    `json:"sources"`
	Recipes       []Recipe             `json:"recipes,omitempty"`
	Components    map[string]Component `json:"components"`
	Layers        any                  `json:"layers,omitempty"`
}

// SnapshotDigest identifies the complete resolved catalog state consumed by recipe
// resolution. Transport locations are intentionally included: this is an operator
// catalog-state pin, not recipeDigest's mirror-independent content identity.
func (cat *Catalog) SnapshotDigest() (string, error) {
	if err := cat.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindCatalogSnapshot, catalogSnapshot{
		SchemaVersion: "nkd.dev/catalog-snapshot/v1", APIVersion: cat.APIVersion,
		Kind: cat.Kind, Metadata: cat.Metadata, Sources: cat.Sources,
		Recipes: cat.Recipes, Components: cat.Components, Layers: cat.Layers,
	})
}

func (overlay Overlay) Validate() error {
	if overlay.APIVersion != OverlayAPIVersion || overlay.Kind != OverlayKind {
		return e.New(e.CodeInvalidCatalog, "catalog overlay has unsupported apiVersion/kind %q/%q", overlay.APIVersion, overlay.Kind)
	}
	if strings.TrimSpace(overlay.Name) == "" || overlay.Name != strings.TrimSpace(overlay.Name) {
		return e.New(e.CodeInvalidCatalog, "catalog overlay name %q is empty or non-canonical", overlay.Name)
	}
	if !canonical.IsDigest(overlay.RequiresCatalogDigest) {
		return e.New(e.CodeUnpinnedSource, "catalog overlay %q requires invalid catalog digest %q", overlay.Name, overlay.RequiresCatalogDigest)
	}
	if len(overlay.Components) == 0 {
		return e.New(e.CodeInvalidCatalog, "catalog overlay %q replaces no components", overlay.Name)
	}
	for _, name := range sortedReplacementNames(overlay.Components) {
		if strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) {
			return e.New(e.CodeInvalidCatalog, "catalog overlay %q has invalid component name %q", overlay.Name, name)
		}
		replacement := overlay.Components[name]
		forms := 0
		if replacement.Prebuilt != nil {
			forms++
		}
		if replacement.Component != nil {
			forms++
		}
		if forms != 1 {
			return e.New(e.CodeInvalidCatalog, "catalog overlay %q component %q requires exactly one of prebuilt or component", overlay.Name, name)
		}
	}
	return nil
}

func (overlay Overlay) Digest() (string, error) {
	if err := overlay.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindCatalogOverlay, overlay)
}

// LoadOverlayContext snapshots and strictly decodes one overlay without following a
// caller-controlled symlink or blocking on a FIFO.
func LoadOverlayContext(ctx context.Context, path string) (Overlay, error) {
	if ctx == nil {
		return Overlay{}, e.New(e.CodeUsage, "catalog overlay load requires a context")
	}
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "catalog-overlay", MaxBytes: OverlayMaxBytes})
	if err != nil {
		if ctx.Err() != nil {
			return Overlay{}, e.Wrap(e.CodeTimeout, ctx.Err(), "loading catalog overlay")
		}
		return Overlay{}, e.Wrap(e.CodeInvalidCatalog, err, "loading catalog overlay %s", path)
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, OverlayMaxBytes)
	if err != nil {
		if ctx.Err() != nil {
			return Overlay{}, e.Wrap(e.CodeTimeout, ctx.Err(), "reading catalog overlay")
		}
		return Overlay{}, e.Wrap(e.CodeInvalidCatalog, err, "reading catalog overlay %s", path)
	}
	var overlay Overlay
	if err := yaml.UnmarshalStrict(raw, &overlay); err != nil {
		return Overlay{}, e.Wrap(e.CodeInvalidCatalog, err, "parsing catalog overlay %s", path)
	}
	if err := overlay.Validate(); err != nil {
		return Overlay{}, err
	}
	return overlay, nil
}

// ApplyStack applies ordered overlays to a defensive catalog copy. Every overlay
// must pin the exact digest produced by its predecessor.
func ApplyStack(base *Catalog, overlays ...Overlay) (*Stack, error) {
	if base == nil {
		return nil, e.New(e.CodeInvalidCatalog, "catalog stack requires a base catalog")
	}
	current, err := cloneCatalog(base)
	if err != nil {
		return nil, err
	}
	digest, err := current.SnapshotDigest()
	if err != nil {
		return nil, err
	}
	result := &Stack{Catalog: current, Digest: digest}
	for _, overlay := range overlays {
		if err := overlay.Validate(); err != nil {
			return nil, err
		}
		if overlay.RequiresCatalogDigest != digest {
			return nil, e.New(e.CodeUnverifiedRef,
				"catalog overlay %q requires %s but current stack is %s", overlay.Name, overlay.RequiresCatalogDigest, digest)
		}
		overlayDigest, err := overlay.Digest()
		if err != nil {
			return nil, err
		}
		for _, name := range sortedReplacementNames(overlay.Components) {
			original, ok := current.Components[name]
			if !ok {
				return nil, e.New(e.CodeInvalidCatalog, "catalog overlay %q replaces unknown component %q", overlay.Name, name)
			}
			replacement, err := materializeReplacement(name, original, overlay.Components[name])
			if err != nil {
				return nil, e.Wrap(e.CodeInvalidCatalog, err, "catalog overlay %q", overlay.Name)
			}
			current.Components[name] = replacement
		}
		if err := current.Validate(); err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "catalog overlay %q result", overlay.Name)
		}
		outputDigest, err := current.SnapshotDigest()
		if err != nil {
			return nil, err
		}
		result.Applied = append(result.Applied, AppliedOverlay{Name: overlay.Name,
			OverlayDigest: overlayDigest, InputDigest: digest, OutputDigest: outputDigest})
		digest = outputDigest
	}
	result.Digest = digest
	return result, nil
}

func materializeReplacement(name string, original Component, replacement ComponentReplacement) (Component, error) {
	var out Component
	if replacement.Component != nil {
		out = *replacement.Component
	} else {
		if strings.TrimSpace(replacement.Version) == "" {
			return Component{}, fmt.Errorf("component %q supplier replacement has no version", name)
		}
		if replacement.SourceDateEpoch <= 0 {
			return Component{}, fmt.Errorf("component %q supplier replacement has no positive sourceDateEpoch", name)
		}
		outputType := replacement.OutputType
		if outputType == "" {
			outputType = original.OutputType
		}
		roles := append([]string(nil), replacement.Roles...)
		if len(roles) == 0 {
			roles = append(roles, original.Roles...)
		}
		prebuilt := *replacement.Prebuilt
		prebuilt.Digests = cloneStringMap(prebuilt.Digests)
		out = Component{Roles: roles, OutputType: outputType, Tag: replacement.Version,
			SourceDate: replacement.SourceDateEpoch, Prebuilt: &prebuilt}
	}
	if out.OutputType != original.OutputType {
		return Component{}, fmt.Errorf("component %q replacement changes outputType from %q to %q; use a new logical component name for a different contract", name, original.OutputType, out.OutputType)
	}
	if err := validateComponent(name, out); err != nil {
		return Component{}, err
	}
	return out, nil
}

func cloneCatalog(in *Catalog) (*Catalog, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "cloning catalog stack base")
	}
	var out Catalog
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "cloning catalog stack base")
	}
	return &out, nil
}

func sortedReplacementNames(replacements map[string]ComponentReplacement) []string {
	names := make([]string, 0, len(replacements))
	for name := range replacements {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
