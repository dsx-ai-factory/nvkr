package client

import (
	"context"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/attest"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/buildgraph"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

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

// ValidationVectorRequest derives a vector from an authoritative pipeline report.
// Resolved and Graph are required when the Client's PublisherEvidenceAuthority is
// asked to qualify evidence. OutputName optionally atomically commits only a fully
// publishable vector's canonical bytes.
type ValidationVectorRequest struct {
	Report            *pipeline.Report
	Resolved          *buildgraph.Resolved
	Graph             *ir.Graph
	PublisherEvidence *attest.PublisherEvidence
	OutputName        string
}

// CreateValidationVector delegates to attest's receipt-bearing conversion and uses
// AtomicOutput for any requested write. A plain report projection is diagnostic only
// and can never enter this authority path.
func (c *Client) CreateValidationVector(ctx context.Context, request ValidationVectorRequest) (*attest.Manifest, error) {
	if err := c.ready(ctx, "validation-vector creation"); err != nil {
		return nil, err
	}
	if err := c.requireAtomicOutput(request.OutputName); err != nil {
		return nil, err
	}
	if request.Report == nil {
		return nil, e.New(e.CodeUsage, "validation-vector creation requires a build report")
	}
	if err := request.Report.RequireQualified(ctx); err != nil {
		return nil, err
	}
	manifest, err := attest.FromQualifiedReport(ctx, request.Report)
	if err != nil {
		return nil, err
	}
	publisherEvidence := request.PublisherEvidence
	if publisherEvidence == nil && c.publisherEvidence != nil {
		if request.Resolved == nil || request.Graph == nil {
			return nil, e.New(e.CodeUsage,
				"publisher-evidence authority requires the resolved producers and graph")
		}
		publisherEvidence, err = c.publisherEvidence.Qualify(ctx, PublisherEvidenceRequest{
			Resolved: request.Resolved, Graph: request.Graph, Report: request.Report,
		})
		if err != nil {
			return nil, normalizeError(ctx, err, "publisher-evidence qualification")
		}
	}
	if publisherEvidence != nil {
		if err := manifest.AttachPublisherEvidence(publisherEvidence); err != nil {
			return nil, err
		}
	}
	if err := contextError(ctx, "validation-vector creation"); err != nil {
		return nil, err
	}
	if request.OutputName != "" {
		canonicalManifest, err := manifest.MarshalQualified(ctx)
		if err != nil {
			return nil, err
		}
		if err := c.commit(ctx, request.OutputName, canonicalManifest); err != nil {
			return nil, err
		}
	}
	return manifest, nil
}
