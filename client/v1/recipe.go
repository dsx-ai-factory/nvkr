package client

import (
	"context"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/recipe"
)

// ResolveRequest is the complete input to recipe resolution. OutputName optionally
// asks the injected AtomicOutput to commit the canonical full execution graph.
type ResolveRequest struct {
	Criteria   criteria.Criteria
	Reserved   map[string]string
	OutputName string
}

// Resolution is the resolved declaration and its one compiled authoritative graph.
type Resolution struct {
	Resolved     *recipe.Resolved
	Graph        *ir.Graph
	RecipeDigest string
}

// ResolveRecipe loads, resolves, and compiles through the authoritative catalog and
// recipe modules. It computes the digest from the exact graph it returns.
func (c *Client) ResolveRecipe(ctx context.Context, request ResolveRequest) (*Resolution, error) {
	if err := c.ready(ctx, "recipe resolution"); err != nil {
		return nil, err
	}
	if err := c.requireAtomicOutput(request.OutputName); err != nil {
		return nil, err
	}
	if c.catalogIO == nil {
		return nil, e.New(e.CodeUsage, "recipe resolution requires catalog I/O")
	}
	loaded, err := catalog.LoadContext(ctx, contextProvider{ctx: ctx, catalogIO: c.catalogIO})
	if err != nil {
		return nil, normalizeError(ctx, err, "catalog load")
	}
	if err := contextError(ctx, "recipe resolution"); err != nil {
		return nil, err
	}
	resolved, err := recipe.Resolve(loaded, request.Criteria, request.Reserved)
	if err != nil {
		return nil, normalizeError(ctx, err, "recipe resolution")
	}
	if err := contextError(ctx, "recipe compilation"); err != nil {
		return nil, err
	}
	graph, err := recipe.Compile(resolved)
	if err != nil {
		return nil, normalizeError(ctx, err, "recipe compilation")
	}
	digest, err := graph.Digest()
	if err != nil {
		return nil, err
	}
	if err := contextError(ctx, "recipe compilation"); err != nil {
		return nil, err
	}
	if request.OutputName != "" {
		canonicalGraph, err := graph.CanonicalJSON()
		if err != nil {
			return nil, err
		}
		if err := c.commit(ctx, request.OutputName, canonicalGraph); err != nil {
			return nil, err
		}
	}
	return &Resolution{Resolved: resolved, Graph: graph, RecipeDigest: digest}, nil
}
