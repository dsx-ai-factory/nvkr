// Package recipe resolves a criteria query against a catalog to a single recipe,
// then compiles it into the engine-neutral pipeline IR (SRD §4.1, §4.5, §5.1).
package recipe

import (
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/aicr"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// Resolved is the single matched recipe with its components in a stable order and
// its criteria fully concretized. AICRProfile is set (non-nil) when the winning
// recipe declares an AICR accelerated-runtime profile, so downstream stages can carry
// its identity+digest as provenance (NKD v1 §8.1).
type Resolved struct {
	RecipeID    string
	Criteria    criteria.Criteria
	AICRProfile *catalog.AICRProfile
	Components  []catalog.NamedComponent
}

// Resolve performs asymmetric matching, specificity tie-breaking, and fail-closed
// reserved-axis handling, returning the single winning recipe or a coded error
// (NKD-FR-RC-001..005, 012).
func Resolve(cat *catalog.Catalog, query criteria.Criteria, reserved map[string]string) (*Resolved, error) {
	// Non-AICR reserved axes (tier, compliance) always fail closed. AICR-governed axes
	// (accelerator/service/intent/platform) are deferred: they activate only for a
	// recipe that declares an AICR profile (NKD v1 goal 10 / §8.1).
	if err := criteria.CheckHardReserved(reserved); err != nil {
		return nil, err
	}
	if err := query.ValidateActive(); err != nil {
		return nil, err
	}
	if cat == nil {
		return nil, e.New(e.CodeInvalidCatalog, "cannot resolve a nil catalog")
	}
	// Resolve is also a public package seam and must not assume callers came through
	// catalog.Load. Re-validating here prevents hand-constructed catalogs from using
	// role/clusterType as hidden recipe selectors or carrying an invalid AICR profile.
	if err := cat.Validate(); err != nil {
		return nil, err
	}
	// Layered (base/overlays/mixins) catalogs resolve by composition, not flat match.
	if cat.Layers != nil {
		return resolveLayered(cat, query, reserved)
	}
	aicrRequested := criteria.AICRAxesSet(reserved)
	if len(aicrRequested) > 0 && !catalogHasAICR(cat) {
		return nil, e.New(e.CodeReservedAxis,
			"AICR-governed axes %v require a recipe with a declared aicrProfile; this catalog declares none", aicrRequested)
	}

	best := -1
	var winners []catalog.Recipe
	for _, r := range cat.Recipes {
		if err := criteria.CheckReserved(r.Reserved); err != nil {
			return nil, err
		}
		if !r.Criteria.Matches(query) {
			continue
		}
		if r.AICRProfile != nil {
			if !aicrProfileMatches(r.AICRProfile, query, reserved) {
				continue
			}
		} else if len(aicrRequested) > 0 {
			// Requested AICR axes cannot select a non-AICR recipe even if its ordinary
			// category criteria are more specific.
			continue
		}
		spec := r.Criteria.Specificity()
		switch {
		case spec > best:
			best = spec
			winners = []catalog.Recipe{r}
		case spec == best:
			winners = append(winners, r)
		}
	}

	if len(winners) == 0 {
		return nil, e.New(e.CodeNoMatch,
			"no recipe matches query {role:%q k8sVersion:%q os:%q arch:%q}",
			query.Role, query.K8sVersion, query.OS, query.Arch)
	}
	if len(winners) > 1 {
		ids := make([]string, len(winners))
		for i, w := range winners {
			ids[i] = w.ID
		}
		sort.Strings(ids)
		return nil, e.New(e.CodeAmbiguous,
			"query matches %d equally-specific recipes: %v — add a distinguishing constraint", len(ids), ids)
	}

	win := winners[0]
	resolved := criteria.Resolve(query, win.Criteria)
	names := dedupSorted(win.Components)
	comps := make([]catalog.NamedComponent, 0, len(names))
	for _, n := range names {
		c, ok := cat.Components[n]
		if !ok {
			return nil, e.New(e.CodeInvalidCatalog, "recipe %q references unknown component %q", win.ID, n)
		}
		// Per-arch pin gate for the no-compile lane: a prebuilt component must have a
		// pinned digest for the RESOLVED arch, else fail closed — never acquire an
		// unpinned artifact (NKD-FR-BLD-051). This mirrors the from-source lane's
		// requirement that every build resolve a digest-pinned toolchain.
		if c.Prebuilt != nil {
			if _, ok := c.Prebuilt.Digests[resolved.Arch]; !ok {
				return nil, e.New(e.CodeUnpinnedSource,
					"prebuilt component %q has no pinned digest for arch %q (run: nkdctl catalog pin-artifacts)", n, resolved.Arch)
			}
		} else if c.FromSource {
			if err := requireToolchainForArch(n, c, resolved.Arch); err != nil {
				return nil, err
			}
		}
		comps = append(comps, catalog.NamedComponent{Name: n, Component: c})
	}
	return &Resolved{
		RecipeID:    win.ID,
		Criteria:    resolved,
		AICRProfile: win.AICRProfile,
		Components:  comps,
	}, nil
}

// aicrProfileMatches applies exact matching to AICR's generator inputs after the core
// category axes match. Optional platform may be omitted on both sides, but is never a
// wildcard for a requested value. The ordinary os query participates too because the
// AICR profile OS must agree with the selected stack OS.
func aicrProfileMatches(p *catalog.AICRProfile, query criteria.Criteria, axes map[string]string) bool {
	if p == nil {
		return false
	}
	// AICR recipe-generation inputs are exact, not wildcards. In particular, an
	// optional empty profile platform matches only an empty query platform; accepting a
	// requested platform against it would silently discard an activated axis.
	return p.Service == axes["service"] &&
		p.Accelerator == axes["accelerator"] &&
		p.OS == query.OS &&
		p.Intent == axes["intent"] &&
		p.Platform == axes["platform"]
}

// requireToolchainForArch closes the partial-map hole at the resolution seam. Catalog
// validation proves every PRESENT toolchain is digest-pinned; resolution additionally
// proves the selected native architecture has either an exact pin or an explicit
// digest-pinned fallback before Compile can emit a build node.
func requireToolchainForArch(name string, c catalog.Component, arch string) error {
	if c.ToolchainImages[arch] != "" || c.ToolchainImage != "" {
		return nil
	}
	return e.New(e.CodeUnpinnedSource,
		"from-source component %q has no digest-pinned toolchain for arch %q", name, arch)
}

// resolveLayered resolves a composition-layered catalog: it composes the base/overlay/
// mixin graph for the query's resolved criteria into an effective component set, then
// maps it onto the registry. AICR-governed axes select an exact digest-pinned profile
// leaf; hard-reserved axes still fail closed. The per-arch prebuilt pin gate applies.
func resolveLayered(cat *catalog.Catalog, query criteria.Criteria, reserved map[string]string) (*Resolved, error) {
	if err := criteria.CheckHardReserved(reserved); err != nil {
		return nil, err
	}
	var selection *aicr.Selection
	if requested := criteria.AICRAxesSet(reserved); len(requested) > 0 {
		if !catalogHasAICR(cat) {
			return nil, e.New(e.CodeReservedAxis,
				"AICR-governed axes %v require a recipe with a declared aicrProfile; this catalog declares none", requested)
		}
		candidate := aicr.Selection{
			Service: reserved["service"], Accelerator: reserved["accelerator"],
			OS: query.OS, Intent: reserved["intent"], Platform: reserved["platform"],
		}
		if err := candidate.Validate(); err != nil {
			return nil, e.Wrap(e.CodeNoMatch, err,
				"no exact layered AICR profile matches the incomplete or unsupported selection")
		}
		selection = &candidate
	}
	resolved := criteria.Resolve(query, criteria.Criteria{}) // fill os/arch defaults from the query
	if criteria.IsWildcard(resolved.Category) {
		categories := cat.Layers.Categories()
		switch len(categories) {
		case 0:
			return nil, e.New(e.CodeNoMatch, "layered catalog declares no recipe categories")
		case 1:
			// Backward-compatible shorthand for a single-category catalog. A catalog
			// with multiple categories must make the selection explicit.
			resolved.Category = categories[0]
		default:
			return nil, e.New(e.CodeAmbiguous,
				"layered catalog declares multiple recipe categories %v; select one explicitly", categories)
		}
	}
	if !cat.Layers.DeclaresCategory(resolved.Category) {
		return nil, e.New(e.CodeNoMatch,
			"layered catalog declares no recipe category %q", resolved.Category)
	}
	composed, err := cat.Layers.ComposeFor(resolved, selection)
	if err != nil {
		return nil, err
	}
	comps, err := cat.ComposeComponents(composed.Components)
	if err != nil {
		return nil, err
	}
	for _, nc := range comps {
		if nc.Prebuilt != nil {
			if _, ok := nc.Prebuilt.Digests[resolved.Arch]; !ok {
				return nil, e.New(e.CodeUnpinnedSource,
					"prebuilt component %q has no pinned digest for arch %q (run: nkdctl catalog pin-artifacts)", nc.Name, resolved.Arch)
			}
		} else if nc.FromSource {
			if err := requireToolchainForArch(nc.Name, nc.Component, resolved.Arch); err != nil {
				return nil, err
			}
		}
	}
	return &Resolved{RecipeID: coordinate(resolved), Criteria: resolved,
		AICRProfile: composed.AICRProfile, Components: comps}, nil
}

// coordinate is a deterministic recipe id for a composed recipe: the concrete
// recipe-SELECTION axes joined (category × k8sVersion × os × arch, ADR-12 §1). Role and
// clusterType are COMPOSITION axes and are deliberately excluded — they do not select a
// recipe and are not part of its identity, so two SKUs of one category share the id.
func coordinate(c criteria.Criteria) string {
	var parts []string
	for _, v := range []string{c.Category, c.K8sVersion, c.OS, c.Arch} {
		if !criteria.IsWildcard(v) {
			parts = append(parts, v)
		}
	}
	if len(parts) == 0 {
		return "base"
	}
	return strings.Join(parts, "-")
}

// catalogHasAICR reports whether any recipe in the catalog declares an AICR profile.
func catalogHasAICR(cat *catalog.Catalog) bool {
	for _, r := range cat.Recipes {
		if r.AICRProfile != nil {
			return true
		}
	}
	if cat.Layers != nil {
		for _, overlay := range cat.Layers.Overlays {
			if overlay != nil && overlay.Spec.AICRProfile != nil {
				return true
			}
		}
	}
	return false
}

func dedupSorted(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
