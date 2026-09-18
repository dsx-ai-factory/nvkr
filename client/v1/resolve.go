package client

import (
	"context"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/buildgraph"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// ResolveRequest selects exact catalog producers for one output. OutputName optionally
// asks the injected AtomicOutput to commit the canonical full execution graph.
type ResolveRequest struct {
	Components  []string
	Platform    string
	AICRProfile string
	OutputName  string
}

// Resolution is the resolved declaration and its one compiled authoritative graph.
type Resolution struct {
	Resolved    *buildgraph.Resolved
	Graph       *ir.Graph
	BuildDigest string
}

// ResolveBuild loads, resolves, and compiles through the authoritative catalog and
// build-graph modules. It computes the digest from the exact graph it returns.
func (c *Client) ResolveBuild(ctx context.Context, request ResolveRequest) (*Resolution, error) {
	if err := c.ready(ctx, "build resolution"); err != nil {
		return nil, err
	}
	if err := c.requireAtomicOutput(request.OutputName); err != nil {
		return nil, err
	}
	if c.catalogIO == nil {
		return nil, e.New(e.CodeUsage, "build resolution requires catalog I/O")
	}
	selection := buildgraph.Selection{Components: request.Components, Platform: request.Platform, AICRProfile: request.AICRProfile}
	if err := selection.Validate(); err != nil {
		return nil, err
	}
	loaded, err := catalog.LoadContext(ctx, contextProvider{ctx: ctx, catalogIO: c.catalogIO})
	if err != nil {
		return nil, normalizeError(ctx, err, "catalog load")
	}
	if err := contextError(ctx, "build resolution"); err != nil {
		return nil, err
	}
	resolved, err := buildgraph.Resolve(loaded, selection)
	if err != nil {
		return nil, normalizeError(ctx, err, "build resolution")
	}
	if err := contextError(ctx, "build compilation"); err != nil {
		return nil, err
	}
	graph, err := buildgraph.Compile(resolved)
	if err != nil {
		return nil, normalizeError(ctx, err, "build compilation")
	}
	digest, err := graph.Digest()
	if err != nil {
		return nil, err
	}
	if err := contextError(ctx, "build compilation"); err != nil {
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
	return &Resolution{Resolved: resolved, Graph: graph, BuildDigest: digest}, nil
}
