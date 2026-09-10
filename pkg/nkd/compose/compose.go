// Package compose is a faithful port of AICR's recipe overlay/mixin composition
// (github.com/NVIDIA/aicr, pkg/recipe) adapted to nkdctl's build-spec components. A
// recipe is not written flat; it is composed from a layered set of documents:
//
//   - base           — the root RecipeMetadata (metadata.name=base), the shared set.
//   - overlays        — RecipeMetadata keyed by a criteria SUBSET, forming a single-
//     parent inheritance tree via spec.base (base ← intermediate ← leaf). Each layer
//     ADDS components, DISABLES them, or deep-merge OVERRIDES their config.
//   - mixins          — RecipeMixin: orthogonal, single-axis fragments (constraints +
//     componentRefs) pulled into overlays by name, defined once and reused.
//
// Composition for a resolved query: select the maximal leaf overlays whose criteria
// match, resolve each one's spec.base chain (root→leaf), merge them into the base spec
// (child wins, deep-merge overrides), apply the accumulated mixins, and emit the
// effective component set + the applied-layer provenance. It is deterministic (sorted,
// no clock/random) and fail-closed (unknown base/mixin/component, inheritance cycle).
//
// This mirrors AICR: MetadataStore.BuildRecipeResult, resolveInheritanceChain,
// FindMatchingOverlays, filterToMaximalLeaves, mergeOverlayChains, mergeMixins,
// RecipeMetadataSpec.Merge, mergeComponentRef, and deepMergeMap.
package compose

import (
	"reflect"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/aicr"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// Schema identifiers, mirroring AICR's RecipeMetadataKind / RecipeMixinKind.
const (
	RecipeMetadataKind = "RecipeMetadata"
	RecipeMixinKind    = "RecipeMixin"
	APIVersion         = "nkd.dev/recipe/v1alpha1"
	// baseRecipeName is the implicit root every inheritance chain terminates at
	// (spec.base == "" is treated as this).
	baseRecipeName = "base"
)

// Constraint is a deployment precondition carried by a layer (advisory preflight
// input), mirroring AICR's Constraint. Constraints do not compose: a name collision
// across layers/mixins is a conflict.
type Constraint struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ComponentRef references a registry component by name and optionally disables it or
// deep-merge-overrides its config. This is the build-spec analog of AICR's ComponentRef:
// AICR carries Helm identity/values + an enabled/install gate in Overrides; nkdctl
// carries an explicit Enabled gate and an Overrides map deep-merged onto the resolved
// (typed) catalog component at integration time.
type ComponentRef struct {
	Name string `json:"name"`
	// Enabled defaults to true (nil). A layer sets it false to remove a component the
	// base/ancestor declared — the nkdctl analog of AICR's Overrides{enabled:false}
	// (kept in the composed inventory but suppressed from the effective set).
	Enabled *bool `json:"enabled,omitempty"`
	// Overrides deep-merges onto the referenced component's config, child-wins
	// (AICR mergeComponentRef → deepMergeMap).
	Overrides map[string]any `json:"overrides,omitempty"`
}

// isEnabled reports whether a ref contributes to the effective set (default true).
func (c ComponentRef) isEnabled() bool { return c.Enabled == nil || *c.Enabled }

// Spec is a layer's body, mirroring AICR's RecipeMetadataSpec.
type Spec struct {
	Base          string             `json:"base,omitempty"`     // parent name; "" ⇒ baseRecipeName
	Criteria      *criteria.Criteria `json:"criteria,omitempty"` // overlay only (base has none)
	AICRProfile   *aicr.Profile      `json:"aicrProfile,omitempty"`
	Mixins        []string           `json:"mixins,omitempty"`
	Constraints   []Constraint       `json:"constraints,omitempty"`
	ComponentRefs []ComponentRef     `json:"componentRefs,omitempty"`
}

// RecipeMetadata is a base or overlay document.
type RecipeMetadata struct {
	Kind       string `json:"kind"`
	APIVersion string `json:"apiVersion"`
	Metadata   struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec Spec `json:"spec"`
}

// RecipeMixin is a composable fragment carrying only constraints + componentRefs,
// mirroring AICR's RecipeMixin.
type RecipeMixin struct {
	Kind       string `json:"kind"`
	APIVersion string `json:"apiVersion"`
	Metadata   struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Constraints   []Constraint   `json:"constraints,omitempty"`
		ComponentRefs []ComponentRef `json:"componentRefs,omitempty"`
	} `json:"spec"`
}

// Store holds the loaded layers, mirroring AICR's MetadataStore.
type Store struct {
	Base     *RecipeMetadata
	Overlays map[string]*RecipeMetadata
	Mixins   map[string]*RecipeMixin
}

// Composed is the flattened result of composition: the effective (enabled) component
// refs in deterministic order, the accumulated constraints, and the provenance list of
// every applied layer (base → …), mirroring AICR's RecipeResult + AppliedOverlays.
type Composed struct {
	Components    []ComponentRef
	Constraints   []Constraint
	AppliedLayers []string
	AICRProfile   *aicr.Profile
}

// Categories returns the concrete category recipe patterns declared by overlays.
// The abstract base and category-wildcard orthogonal overlays contribute to matched
// recipes but cannot create a recipe category by themselves.
func (s *Store) Categories() []string {
	if s == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, overlay := range s.Overlays {
		if overlay == nil || overlay.Spec.Criteria == nil ||
			criteria.IsWildcard(overlay.Spec.Criteria.Category) {
			continue
		}
		seen[overlay.Spec.Criteria.Category] = true
	}
	values := make([]string, 0, len(seen))
	for value := range seen {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

// DeclaresCategory reports whether at least one overlay establishes category as a
// recipe-selection coordinate. Base-only composition is not a resolved recipe.
func (s *Store) DeclaresCategory(category string) bool {
	values := s.Categories()
	i := sort.SearchStrings(values, category)
	return i < len(values) && values[i] == category
}

// ── deep merge (faithful port of AICR deepMergeMap) ──────────────────────────

// deepMergeMap recursively merges src into dst: a non-map src value replaces dst[k]
// (deep-copied); a map src value merges into a map dst[k], else replaces it. src (the
// more-specific layer) wins. A nil/absent src key leaves dst untouched.
func deepMergeMap(dst, src map[string]any) {
	for k, sv := range src {
		svMap, svIsMap := sv.(map[string]any)
		if !svIsMap {
			dst[k] = deepCopyAny(sv)
			continue
		}
		dvMap, dvIsMap := dst[k].(map[string]any)
		if !dvIsMap {
			dst[k] = deepCopyAny(svMap)
			continue
		}
		deepMergeMap(dvMap, svMap)
	}
}

// deepCopyAny clones a decoded YAML/JSON value tree so merged results never alias a
// source layer's maps/slices (AICR uses serializer.DeepCopyAny for the same reason).
func deepCopyAny(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, e := range t {
			m[k] = deepCopyAny(e)
		}
		return m
	case []any:
		s := make([]any, len(t))
		for i, e := range t {
			s[i] = deepCopyAny(e)
		}
		return s
	default:
		return v
	}
}

// ── spec merge (faithful port of RecipeMetadataSpec.Merge + mergeComponentRef) ──

// Merge folds other (the more-specific layer) into s. Component refs are keyed by name
// with other taking precedence (deep-merged via mergeComponentRef), while constraints
// deliberately do NOT compose: repeating a constraint name is ambiguous and fails
// closed instead of silently replacing an ancestor/sibling value.
func (s *Spec) Merge(other *Spec) error {
	if other == nil {
		return nil
	}
	mergedProfile := cloneAICRProfile(s.AICRProfile)
	if other.AICRProfile != nil {
		if mergedProfile != nil && *mergedProfile != *other.AICRProfile {
			return e.New(e.CodeAmbiguous,
				"composition layers declare conflicting AICR profiles %q and %q; select one exact profile leaf",
				mergedProfile.Identity, other.AICRProfile.Identity)
		}
		mergedProfile = cloneAICRProfile(other.AICRProfile)
	}

	constraintMap := make(map[string]Constraint, len(s.Constraints)+len(other.Constraints))
	for _, c := range s.Constraints {
		if strings.TrimSpace(c.Name) == "" || c.Name != strings.TrimSpace(c.Name) {
			return e.New(e.CodeInvalidCatalog, "composition constraint name %q is empty or has surrounding whitespace", c.Name)
		}
		if _, exists := constraintMap[c.Name]; exists {
			return e.New(e.CodeInvalidCatalog,
				"composition constraint %q is declared more than once (constraints do not compose)", c.Name)
		}
		constraintMap[c.Name] = c
	}
	for _, c := range other.Constraints {
		if strings.TrimSpace(c.Name) == "" || c.Name != strings.TrimSpace(c.Name) {
			return e.New(e.CodeInvalidCatalog, "composition constraint name %q is empty or has surrounding whitespace", c.Name)
		}
		if _, exists := constraintMap[c.Name]; exists {
			return e.New(e.CodeInvalidCatalog,
				"composition constraint %q is declared more than once (constraints do not compose)", c.Name)
		}
		constraintMap[c.Name] = c
	}
	componentMap := make(map[string]ComponentRef, len(s.ComponentRefs)+len(other.ComponentRefs))
	for _, c := range s.ComponentRefs {
		if strings.TrimSpace(c.Name) == "" || c.Name != strings.TrimSpace(c.Name) {
			return e.New(e.CodeInvalidCatalog,
				"composition component name %q is empty or has surrounding whitespace", c.Name)
		}
		if _, duplicate := componentMap[c.Name]; duplicate {
			return e.New(e.CodeInvalidCatalog,
				"composition layer references component %q more than once", c.Name)
		}
		componentMap[c.Name] = c
	}
	otherComponents := make(map[string]struct{}, len(other.ComponentRefs))
	for _, overlay := range other.ComponentRefs {
		if strings.TrimSpace(overlay.Name) == "" || overlay.Name != strings.TrimSpace(overlay.Name) {
			return e.New(e.CodeInvalidCatalog,
				"composition component name %q is empty or has surrounding whitespace", overlay.Name)
		}
		if _, duplicate := otherComponents[overlay.Name]; duplicate {
			return e.New(e.CodeInvalidCatalog,
				"composition layer references component %q more than once", overlay.Name)
		}
		otherComponents[overlay.Name] = struct{}{}
		if base, exists := componentMap[overlay.Name]; exists {
			componentMap[overlay.Name] = mergeComponentRef(base, overlay)
		} else {
			componentMap[overlay.Name] = overlay
		}
	}
	mergedMixins := append([]string(nil), s.Mixins...)
	seenMixins := make(map[string]bool, len(s.Mixins)+len(other.Mixins))
	for _, m := range s.Mixins {
		if strings.TrimSpace(m) == "" || m != strings.TrimSpace(m) {
			return e.New(e.CodeInvalidCatalog,
				"composition mixin name %q is empty or has surrounding whitespace", m)
		}
		if seenMixins[m] {
			return e.New(e.CodeInvalidCatalog, "composition layer references mixin %q more than once", m)
		}
		seenMixins[m] = true
	}
	otherMixins := make(map[string]bool, len(other.Mixins))
	for _, m := range other.Mixins {
		if strings.TrimSpace(m) == "" || m != strings.TrimSpace(m) {
			return e.New(e.CodeInvalidCatalog,
				"composition mixin name %q is empty or has surrounding whitespace", m)
		}
		if otherMixins[m] {
			return e.New(e.CodeInvalidCatalog, "composition layer references mixin %q more than once", m)
		}
		otherMixins[m] = true
		if !seenMixins[m] {
			mergedMixins = append(mergedMixins, m)
			seenMixins[m] = true
		}
	}

	// Commit only after every input has validated. A failed merge never leaves a
	// partially-mutated receiver whose constraints came from a rejected layer.
	s.Constraints = mapToSortedConstraints(constraintMap)
	s.ComponentRefs = mapToSortedComponentRefs(componentMap)
	s.Mixins = mergedMixins
	s.AICRProfile = mergedProfile
	return nil
}

func cloneAICRProfile(profile *aicr.Profile) *aicr.Profile {
	if profile == nil {
		return nil
	}
	copyProfile := *profile
	return &copyProfile
}

// mergeComponentRef merges overlay into base, overlay winning: a set Enabled overrides,
// and Overrides deep-merge (faithful to AICR's mergeComponentRef → deepMergeMap).
func mergeComponentRef(base, overlay ComponentRef) ComponentRef {
	result := base
	if overlay.Enabled != nil {
		result.Enabled = overlay.Enabled
	}
	if len(overlay.Overrides) > 0 {
		merged := make(map[string]any, len(result.Overrides)+len(overlay.Overrides))
		for k, v := range result.Overrides {
			merged[k] = deepCopyAny(v)
		}
		deepMergeMap(merged, overlay.Overrides)
		result.Overrides = merged
	}
	return result
}

func mapToSortedConstraints(m map[string]Constraint) []Constraint {
	out := make([]Constraint, 0, len(m))
	for _, c := range m {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func mapToSortedComponentRefs(m map[string]ComponentRef) []ComponentRef {
	out := make([]ComponentRef, 0, len(m))
	for _, c := range m {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ── resolution (faithful port of the MetadataStore build path) ───────────────

// getByName returns the base (for "" or "base") or an overlay by name.
func (s *Store) getByName(name string) (*RecipeMetadata, bool) {
	if name == "" || name == baseRecipeName {
		return s.Base, s.Base != nil
	}
	o, ok := s.Overlays[name]
	return o, ok
}

// resolveInheritanceChain returns a recipe's inheritance chain root→leaf (base first),
// detecting cycles and unknown parents. Port of AICR resolveInheritanceChain.
func (s *Store) resolveInheritanceChain(name string) ([]*RecipeMetadata, error) {
	visited := map[string]bool{}
	var chain []*RecipeMetadata
	current := name
	for current != "" && current != baseRecipeName {
		if visited[current] {
			return nil, e.New(e.CodeInvalidCatalog,
				"circular inheritance detected: overlay %q references itself in its base chain", current)
		}
		visited[current] = true
		r, ok := s.getByName(current)
		if !ok {
			return nil, e.New(e.CodeInvalidCatalog, "overlay %q not found (referenced as a base)", current)
		}
		chain = append(chain, r)
		current = r.Spec.Base
	}
	// reverse to root→target
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	if s.Base != nil {
		chain = append([]*RecipeMetadata{s.Base}, chain...)
	}
	return chain, nil
}

// findMatchingOverlays returns the maximal leaf overlays whose criteria match the query,
// sorted least-specific-first (then by name) so more-specific overlays apply later.
// Port of AICR FindMatchingOverlays + filterToMaximalLeaves.
func (s *Store) findMatchingOverlays(query criteria.Criteria, selection *aicr.Selection) []*RecipeMetadata {
	var matches []*RecipeMetadata
	for _, o := range s.Overlays {
		if o.Spec.Criteria == nil {
			continue
		}
		if o.Spec.Criteria.Matches(query) {
			if o.Spec.AICRProfile != nil &&
				(selection == nil || !o.Spec.AICRProfile.Matches(*selection)) {
				continue
			}
			matches = append(matches, o)
		}
	}
	matches = s.filterToMaximalLeaves(matches)
	sort.SliceStable(matches, func(i, j int) bool {
		si, sj := matches[i].Spec.Criteria.Specificity(), matches[j].Spec.Criteria.Specificity()
		if si != sj {
			return si < sj
		}
		return matches[i].Metadata.Name < matches[j].Metadata.Name
	})
	return matches
}

// checkSiblingAmbiguity enforces ADR-11 §1: two equally-specific, non-ancestor leaf
// overlays that write CONFLICTING values on the same (component, override path) — or
// disagree on a component's enabled flag — fail closed (CodeAmbiguous), extending the
// flat resolver's tie rule into the layered path. All selected leaves are mutually
// non-ancestor (filterToMaximalLeaves), so the predicate keys only on equal
// specificity + same component + same path + differing value; orthogonal overlays
// (e.g. category vs os) never collide because they touch different paths.
func (s *Store) checkSiblingAmbiguity(leaves []*RecipeMetadata) error {
	// Each leaf's own contribution = its chain merged WITHOUT the shared base, so
	// base-seeded values common to both siblings never register as a conflict.
	contrib := make(map[string]*Spec, len(leaves))
	for _, leaf := range leaves {
		chain, err := s.resolveInheritanceChain(leaf.Metadata.Name)
		if err != nil {
			return err
		}
		c := &Spec{}
		for i := 1; i < len(chain); i++ { // skip index 0 (base)
			if err := c.Merge(&chain[i].Spec); err != nil {
				return err
			}
		}
		contrib[leaf.Metadata.Name] = c
	}
	for i := 0; i < len(leaves); i++ {
		for j := i + 1; j < len(leaves); j++ {
			a, b := leaves[i], leaves[j]
			if a.Spec.Criteria.Specificity() != b.Spec.Criteria.Specificity() {
				continue // a clear specificity winner exists; not a tie
			}
			if err := siblingConflict(a.Metadata.Name, contrib[a.Metadata.Name],
				b.Metadata.Name, contrib[b.Metadata.Name]); err != nil {
				return err
			}
		}
	}
	return nil
}

// siblingConflict fails closed if two equal-specificity leaves disagree on a shared
// component's enabled flag or write differing values at the same override leaf path.
func siblingConflict(an string, a *Spec, bn string, b *Spec) error {
	if a.AICRProfile != nil && b.AICRProfile != nil && *a.AICRProfile != *b.AICRProfile {
		return e.New(e.CodeAmbiguous,
			"sibling overlays %q and %q declare conflicting AICR profiles %q and %q; add a distinguishing AICR selection",
			an, bn, a.AICRProfile.Identity, b.AICRProfile.Identity)
	}
	bref := make(map[string]ComponentRef, len(b.ComponentRefs))
	for _, c := range b.ComponentRefs {
		bref[c.Name] = c
	}
	for _, ca := range a.ComponentRefs {
		cb, ok := bref[ca.Name]
		if !ok {
			continue // component only touched by one sibling — orthogonal, no conflict
		}
		if ca.Enabled != nil && cb.Enabled != nil && *ca.Enabled != *cb.Enabled {
			return e.New(e.CodeAmbiguous,
				"sibling overlays %q and %q set conflicting 'enabled' on component %q; add a distinguishing constraint",
				an, bn, ca.Name)
		}
		fa, fb := map[string]any{}, map[string]any{}
		flattenOverrides("", ca.Overrides, fa)
		flattenOverrides("", cb.Overrides, fb)
		for path, va := range fa {
			if vb, ok := fb[path]; ok && !reflect.DeepEqual(va, vb) {
				return e.New(e.CodeAmbiguous,
					"sibling overlays %q and %q write conflicting values at component %q path %q (%v vs %v); add a distinguishing constraint",
					an, bn, ca.Name, path, va, vb)
			}
		}
	}
	return nil
}

// flattenOverrides flattens a nested override map to dotted leaf paths so two
// siblings' writes can be compared path-by-path.
func flattenOverrides(prefix string, m map[string]any, out map[string]any) {
	for k, v := range m {
		p := k
		if prefix != "" {
			p = prefix + "." + k
		}
		if sub, ok := v.(map[string]any); ok {
			flattenOverrides(p, sub, out)
		} else {
			out[p] = v
		}
	}
}

// filterToMaximalLeaves drops any match that is an ancestor (via spec.base) of another
// match, so only the most-specific leaves remain candidates (their chains are still
// resolved during merge). Port of AICR filterToMaximalLeaves.
func (s *Store) filterToMaximalLeaves(matches []*RecipeMetadata) []*RecipeMetadata {
	ancestors := map[string]bool{}
	for _, o := range matches {
		visited := map[string]bool{}
		base := o.Spec.Base
		for base != "" && base != baseRecipeName {
			if visited[base] {
				break
			}
			visited[base] = true
			ancestors[base] = true
			if r, ok := s.getByName(base); ok {
				base = r.Spec.Base
			} else {
				break
			}
		}
	}
	leaves := make([]*RecipeMetadata, 0, len(matches))
	for _, o := range matches {
		if !ancestors[o.Metadata.Name] {
			leaves = append(leaves, o)
		}
	}
	return leaves
}

// mergeMixins folds each referenced mixin's constraints + componentRefs into merged,
// failing closed on an unknown mixin or a constraint name-collision (constraints do not
// compose). Port of AICR mergeMixins (component conflict rules relaxed to nkdctl's
// merge-by-name since nkdctl has no chart-identity fields).
func (s *Store) mergeMixins(merged *Spec) error {
	if len(merged.Mixins) == 0 {
		return nil
	}
	for _, name := range merged.Mixins {
		mixin, ok := s.Mixins[name]
		if !ok {
			return e.New(e.CodeInvalidCatalog, "mixin %q not found", name)
		}
		if err := merged.Merge(&Spec{Constraints: mixin.Spec.Constraints, ComponentRefs: mixin.Spec.ComponentRefs}); err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err, "merging mixin %q", name)
		}
	}
	return nil
}

// Compose flattens the layered catalog for a resolved query into the effective
// component set + provenance. Port of AICR BuildRecipeResult: start from base, merge
// each matching leaf's inheritance chain (child wins, shared ancestors merged once),
// apply mixins, then drop disabled refs. Deterministic and fail-closed.
func (s *Store) Compose(query criteria.Criteria) (*Composed, error) {
	return s.ComposeFor(query, nil)
}

// ComposeFor is the AICR-aware composition entry point. Ordinary layers are selected
// by the four core recipe axes; an AICR profile leaf additionally requires exact
// equality with selection. This lets h100 and h200 leaves share the same category
// criteria without becoming ambiguous or leaking AICR axes into criteria.Criteria.
func (s *Store) ComposeFor(query criteria.Criteria, selection *aicr.Selection) (*Composed, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if selection != nil {
		if err := selection.Validate(); err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "AICR recipe selection")
		}
	}
	if s.Base == nil {
		return nil, e.New(e.CodeInvalidCatalog, "layered catalog has no base recipe (base.yaml)")
	}
	// init from base (copy so the store is never mutated)
	merged := Spec{
		Base:          s.Base.Spec.Base,
		AICRProfile:   cloneAICRProfile(s.Base.Spec.AICRProfile),
		Mixins:        append([]string(nil), s.Base.Spec.Mixins...),
		Constraints:   append([]Constraint(nil), s.Base.Spec.Constraints...),
		ComponentRefs: append([]ComponentRef(nil), s.Base.Spec.ComponentRefs...),
	}
	applied := []string{baseRecipeName}

	matchingProfileLayer := false
	if selection == nil {
		for _, overlay := range s.Overlays {
			if overlay != nil && overlay.Spec.Criteria != nil && overlay.Spec.AICRProfile != nil &&
				overlay.Spec.Criteria.Matches(query) {
				matchingProfileLayer = true
				break
			}
		}
	}
	overlays := s.findMatchingOverlays(query, selection)
	if selection == nil && matchingProfileLayer {
		return nil, e.New(e.CodeNoMatch,
			"layered accelerated-runtime recipe requires an exact AICR selection")
	}
	if err := s.checkSiblingAmbiguity(overlays); err != nil {
		return nil, err
	}
	processed := map[string]bool{}
	for _, overlay := range overlays {
		chain, err := s.resolveInheritanceChain(overlay.Metadata.Name)
		if err != nil {
			return nil, err
		}
		for i := 1; i < len(chain); i++ { // skip index 0 (base, already applied)
			r := chain[i]
			if processed[r.Metadata.Name] {
				continue
			}
			processed[r.Metadata.Name] = true
			if err := merged.Merge(&r.Spec); err != nil {
				return nil, e.Wrap(e.CodeInvalidCatalog, err, "merging overlay %q", r.Metadata.Name)
			}
			applied = append(applied, r.Metadata.Name)
		}
	}

	if err := s.mergeMixins(&merged); err != nil {
		return nil, err
	}

	// Materialize the effective set: enabled refs only, already sorted by name.
	effective := make([]ComponentRef, 0, len(merged.ComponentRefs))
	for _, c := range merged.ComponentRefs {
		if c.isEnabled() {
			effective = append(effective, c)
		}
	}
	if merged.AICRProfile != nil && selection == nil {
		return nil, e.New(e.CodeNoMatch,
			"layered AICR recipe %q requires an exact service, accelerator, intent, and optional platform selection",
			merged.AICRProfile.Identity)
	}
	if selection != nil && (merged.AICRProfile == nil || !merged.AICRProfile.Matches(*selection)) {
		return nil, e.New(e.CodeNoMatch,
			"no layered AICR profile matches service=%s accelerator=%s os=%s intent=%s platform=%s",
			selection.Service, selection.Accelerator, selection.OS, selection.Intent, selection.Platform)
	}
	return &Composed{Components: effective, Constraints: merged.Constraints,
		AppliedLayers: applied, AICRProfile: cloneAICRProfile(merged.AICRProfile)}, nil
}
