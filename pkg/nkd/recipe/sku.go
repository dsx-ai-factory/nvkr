package recipe

import (
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// NodeSKU is a role PROJECTION (a view) of a resolved node-level recipe: the subset of
// its components whose `roles` affinity includes Role, assembled for control-plane /
// worker / gpu-worker delivery (ADR-12 §3, ADR-14 §4). It is deliberately NOT a
// *Resolved: a Node SKU is a delivery view, not a recipe, and carries no independent
// identity. ParentRecipeID names the one category recipe that every SKU of a category
// shares; the SKU's role-invariance is enforced by the TYPE, not by convention — because
// NodeSKU is a distinct type, `Compile(sku)` does not type-check, so a role-filtered
// subset can never be recompiled into a role-variant recipeDigest. (Before this split
// the SKU was a *Resolved and Compile(FilterByRole(res)) silently minted such a digest.)
//
// The concept is intentionally node-scoped rather than a generic component grouping: the
// role filter is meaningful only for node-level categories (os, kubernetes), which
// produce per-node artifacts a machine image is carved from, and the node-category gate
// is a real safety property, not incidental. See docs decision note in this package's
// tests and CONTEXT.md ("Node SKU").
type NodeSKU struct {
	// ParentRecipeID and ParentCriteria are the resolved node-category recipe this SKU is
	// a view of; they are provenance, not a new identity.
	ParentRecipeID string
	ParentCriteria criteria.Criteria
	Role           string
	AICRProfile    *catalog.AICRProfile
	Components     []catalog.NamedComponent
}

// ComponentNames returns the SKU's selected component names in resolved order.
func (s *NodeSKU) ComponentNames() []string {
	if s == nil {
		return nil
	}
	names := make([]string, 0, len(s.Components))
	for _, nc := range s.Components {
		names = append(names, nc.Name)
	}
	return names
}

// FilterByRole projects a resolved node-level category recipe onto the Node SKU for
// role: the subset of components whose `roles` affinity includes role. A component with
// no declared roles is role-agnostic and appears in every SKU. An empty or "any" role
// returns the full component set as one SKU. It fails closed with a coded error for a
// nil recipe, a role outside the v1 domain, or a non-node category — never interpreting
// an unknown role as a valid empty SKU. This is COMPOSITION/delivery: it never rebuilds
// and never changes any component's identity; the parent recipe's recipeDigest is
// unaffected (that is exactly why the result is a NodeSKU and not a *Resolved).
//
// There is one function by design: the previous nil-returning legacy variant defeated
// its own fail-closed contract because a caller could dereference the nil result and
// panic with an uncoded error. Callers must propagate the coded error.
func FilterByRole(res *Resolved, role string) (*NodeSKU, error) {
	if res == nil {
		return nil, e.New(e.CodeInvalidCatalog, "cannot filter a nil resolved recipe")
	}
	if err := (criteria.Criteria{Role: role}).ValidateActive(); err != nil {
		return nil, err
	}
	sku := &NodeSKU{ParentRecipeID: res.RecipeID, ParentCriteria: res.Criteria, Role: role, AICRProfile: res.AICRProfile}
	if criteria.IsWildcard(role) {
		sku.Role = ""
		sku.Components = append(sku.Components, res.Components...)
		return sku, nil
	}
	if !criteria.IsNodeCategory(res.Criteria.Category) {
		return nil, e.New(e.CodeInvalidCatalog,
			"role %q can filter only node-level categories (os, kubernetes), not %q", role, res.Criteria.Category)
	}
	for _, nc := range res.Components {
		if componentHasRole(nc.Component, role) {
			sku.Components = append(sku.Components, nc)
		}
	}
	return sku, nil
}

func componentHasRole(c catalog.Component, role string) bool {
	if len(c.Roles) == 0 {
		return true // role-agnostic: in every SKU
	}
	for _, r := range c.Roles {
		if r == role {
			return true
		}
	}
	return false
}
