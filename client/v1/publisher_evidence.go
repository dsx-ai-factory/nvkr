package client

import (
	"context"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/attest"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/provenance"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

type localPublisherEvidenceAuthority struct {
	reader sbom.EvidenceReader
}

// NewLocalPublisherEvidenceAuthority composes the report-scoped SBOM and provenance
// modules behind the SDK's narrow publisher-evidence seam. A nil reader is valid
// only when the selected output does not require external evidence acquisition.
func NewLocalPublisherEvidenceAuthority(reader sbom.EvidenceReader) PublisherEvidenceAuthority {
	return localPublisherEvidenceAuthority{reader: reader}
}

func (authority localPublisherEvidenceAuthority) Qualify(ctx context.Context,
	request PublisherEvidenceRequest,
) (*attest.PublisherEvidence, error) {
	if request.Resolved == nil || request.Graph == nil || request.Report == nil {
		return nil, e.New(e.CodeUsage,
			"publisher-evidence authority requires resolved producers, graph, and qualified report")
	}
	sboms, err := sbom.BuildVerifiedFromReport(ctx, request.Resolved, request.Report, authority.reader)
	if err != nil {
		return nil, err
	}
	provenanceSet, err := provenance.BuildQualifiedFromReport(ctx, request.Report, request.Graph)
	if err != nil {
		return nil, err
	}
	return attest.QualifyPublisherEvidence(ctx, sboms, provenanceSet)
}
