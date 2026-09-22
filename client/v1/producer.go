package client

import (
	"context"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/buildgraph"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

// ResolveRequest selects exact catalog producers for one output. OutputName optionally
// asks the injected AtomicOutput to commit the canonical full execution graph.
type ResolveRequest struct {
	Components   []string
	Platform     string
	OutputFormat string
	OutputName   string
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
	selection := buildgraph.Selection{Components: request.Components, Platform: request.Platform,
		OutputFormat: request.OutputFormat}
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

// RunRequest executes an already resolved graph. BuildDigest is required and must
// equal the graph's authoritative content digest.
type RunRequest struct {
	Graph       *ir.Graph
	BuildDigest string
	Options     pipeline.RunOptions
}

// Run delegates graph execution to the injected runner while binding its context and
// verifying that request and report identities refer to the same graph. On success,
// the caller owns the returned report and must call Report.Close after its final
// authority-bearing use.
func (c *Client) Run(ctx context.Context, request RunRequest) (*pipeline.Report, error) {
	if err := c.ready(ctx, "pipeline run"); err != nil {
		return nil, err
	}
	if c.runner == nil {
		return nil, e.New(e.CodeUsage, "pipeline run requires a runner")
	}
	if request.Graph == nil {
		return nil, e.New(e.CodeUsage, "pipeline run requires a graph")
	}
	if err := request.Graph.Validate(); err != nil {
		return nil, err
	}
	wantDigest, err := request.Graph.Digest()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.BuildDigest) == "" || request.BuildDigest != wantDigest {
		return nil, e.New(e.CodeVerifyFailed,
			"pipeline buildDigest %q does not match graph digest %s", request.BuildDigest, wantDigest)
	}
	if err := contextError(ctx, "pipeline run"); err != nil {
		return nil, err
	}
	options := request.Options
	options.Ctx = ctx
	if options.Runtime == "" {
		options.Runtime = c.runtime
	}
	report, err := c.runner.Run(request.Graph, request.BuildDigest, options)
	if err != nil {
		if report != nil {
			_ = report.Close()
		}
		return nil, normalizeError(ctx, err, "pipeline run")
	}
	if err := contextError(ctx, "pipeline run"); err != nil {
		if report != nil {
			_ = report.Close()
		}
		return nil, err
	}
	if report == nil {
		return nil, e.New(e.CodeInternal, "pipeline runner %q returned no report", c.runner.Name())
	}
	if report.BuildDigest != request.BuildDigest {
		_ = report.Close()
		return nil, e.New(e.CodeVerifyFailed,
			"pipeline runner %q returned build identity %q, want %q",
			c.runner.Name(), report.BuildDigest, request.BuildDigest)
	}
	if options.Qualification != nil {
		if err := report.RequireQualified(ctx); err != nil {
			_ = report.Close()
			return nil, err
		}
	}
	return report, nil
}
