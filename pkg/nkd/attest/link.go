package attest

import (
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/provenance"
)

// ProvenanceIndex builds a declaration-only deterministic SLSA provenance index
// for diagnostics. It resolves dependencies from caller data but carries no opaque
// producer authority and cannot issue publisher receipts. Publisher-qualified
// flows use provenance.BuildQualifiedFromReport. profileDigest is the declared
// reproducible builder id.
func (m *Manifest) ProvenanceIndex(g *ir.Graph, profileDigest string) *provenance.Index {
	builds := map[string]ir.Node{}
	byID := map[string]ir.Node{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
		if n.Kind == ir.KindBuild || n.Kind == ir.KindPull {
			builds[n.Component] = n
		}
	}
	arts := make([]provenance.Artifact, 0, len(m.Components))
	for _, c := range m.Components {
		bn := builds[c.Name]
		var deps []provenance.Dependency
		if bn.Toolchain != "" {
			deps = append(deps, provenance.Dependency{URI: bn.Toolchain})
		}
		if c.InputDigest != "" {
			deps = append(deps, provenance.Dependency{
				URI:    "nkd:resolved-input/" + c.Name,
				Digest: map[string]string{"sha256": strings.TrimPrefix(c.InputDigest, "sha256:")},
			})
		}
		for _, need := range bn.Needs {
			if f := byID[need]; f.Kind == ir.KindFetch {
				deps = append(deps, provenance.Dependency{
					URI: f.Repo, Digest: map[string]string{"gitCommit": f.Commit},
				})
			}
		}
		if bn.Kind == ir.KindPull && bn.Artifact != "" {
			deps = append(deps, provenance.Dependency{
				URI: bn.Artifact, Digest: map[string]string{"sha256": strings.TrimPrefix(bn.Digest, "sha256:")},
			})
		}
		arts = append(arts, provenance.Artifact{
			Component: c.Name, SHA256: c.SHA256, InputDigest: c.InputDigest,
			BuilderType: c.BuilderType, Deps: deps,
		})
	}
	return provenance.BuildIndex(m.RecipeDigest, profileDigest, arts)
}

// LinkEvidence computes and attaches the aggregate evidence digests to the vector
// (ADR-05 §1/§4): the SBOM digest (caller-supplied, deterministic), the provenance
// index digest, and the evidence-bundle aggregate digest. e2e/cluster conformance
// digests are handed off separately (ADR-16) and left untouched. Fail-closed on a
// serialization error.
func (m *Manifest) LinkEvidence(g *ir.Graph, profileDigest, sbomDigest string, bundle *EvidenceBundle) error {
	prov := m.ProvenanceIndex(g, profileDigest)
	provDigest, err := prov.Digest()
	if err != nil {
		return err
	}
	ev := &EvidenceRef{SBOMDigest: sbomDigest, ProvenanceDigest: provDigest}
	if m.Evidence != nil {
		ev.E2ETestDigest = m.Evidence.E2ETestDigest
		ev.ClusterValidationDigest = m.Evidence.ClusterValidationDigest
		ev.VulnReportDigest = m.Evidence.VulnReportDigest
	}
	var bundleDigest string
	if bundle != nil {
		if bundleDigest, err = bundle.Digest(); err != nil {
			return err
		}
	}
	m.AttachEvidence(ev, bundleDigest)
	return nil
}
