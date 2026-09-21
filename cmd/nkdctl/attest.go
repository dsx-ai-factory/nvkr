package main

import (
	"context"
	"fmt"
	"os"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/attest"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/buildgraph"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/provenance"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/trust"
	"github.com/urfave/cli/v2"
)

func cmdAttest() *cli.Command {
	return &cli.Command{
		Name:  "attest",
		Usage: "build, then emit the expected-digests manifest (the publisher's zero-binary artifact)",
		Flags: append(append(append(selectionFlags(), buildFlags()...),
			&cli.StringFlag{Name: "out", Aliases: []string{"o"}, Value: "expected-digests.json",
				Usage: "manifest output path"},
			&cli.StringFlag{Name: "conformance", Usage: "reserved legacy field; qualified publisher vectors reject caller-authored conformance claims"},
			&cli.StringFlag{Name: "validation-vector", Usage: "reserved legacy field; qualified publisher vectors reject caller-authored validation references"},
			&cli.StringFlag{Name: "evidence-out", Usage: "also write a deterministic evidence bundle (verdict + preflight + per-component causes)"},
			&cli.BoolFlag{Name: "aicr-bundle", Usage: "reserved; publisher-attestation AICR bundling is unavailable in this release"},
			&cli.StringFlag{Name: "aicr-out", Value: ".nkd/work/aicr-bundle", Usage: "reserved output path for --aicr-bundle"},
			&cli.BoolFlag{Name: "vuln", Usage: "scan the deterministic SBOM with a shelled scanner and fold the report digest into the vector (ADR-13 §3)"},
			&cli.StringFlag{Name: "vuln-artifact", Usage: "with --vuln: exact component whose per-artifact SBOM is scanned (required for a multi-artifact build)"},
			&cli.StringFlag{Name: "vuln-scanner", Value: "grype", Usage: "with --vuln: qualified scanner kind (grype|trivy)"},
			&cli.StringFlag{Name: "vuln-scanner-digest", Usage: "with --vuln: exact observed scanner sha256 identity"},
			&cli.StringFlag{Name: "vuln-db", Usage: "with --vuln: exact observed scanner database sha256 identity"},
			&cli.StringFlag{Name: "vuln-out", Value: "vuln-report.json", Usage: "with --vuln: the deterministic vuln-report output path"},
			&cli.StringFlag{Name: "release-member", Usage: "also emit a release.Member for this build (aggregate with `release from-vectors` into a ReleaseSet; ADR-16)"},
			&cli.BoolFlag{Name: "sign", Usage: "sign the manifest detached+keyless via the pinned cosign capability"},
			&cli.BoolFlag{Name: "offline", Usage: "verification-only compatibility flag; qualified keyless signing rejects offline mode"},
			&cli.StringFlag{Name: "bundle", Usage: "with --sign: write a mirrorable bundle (sig+cert+Rekor inclusion proof) for airgap verify"},
		), qualifiedDocumentSignerFlags()...),
		Action: func(c *cli.Context) error {
			if c.String("conformance") != "" || c.String("validation-vector") != "" {
				return e.New(e.CodeUnsupported,
					"attest --conformance/--validation-vector cannot add caller-authored later-gate claims to a publisher vector; use a separately authenticated conformance receipt")
			}
			if c.Bool("aicr-bundle") {
				return e.New(e.CodeUnsupported,
					"attest --aicr-bundle cannot bind a rendered bundle with qualified publisher authority in this release; standalone AICR rendering is a locally verified diagnostic only")
			}
			if err := rejectQualifiedPackaging(c); err != nil {
				return err
			}
			if (c.Bool("sign") || c.String("bundle") != "") && c.Bool("offline") {
				return e.New(e.CodeUnsupported,
					"qualified keyless signing requires normal transparency-log access; --offline is verification-only")
			}
			if c.Bool("sign") || c.String("bundle") != "" {
				if err := admitQualifiedDocumentSigner(c); err != nil {
					return err
				}
			}
			if c.Bool("vuln") {
				return e.New(e.CodeUnsupported,
					"attest --vuln requires a network-denied observed-identity scanner adapter; the stock CLI has none")
			}
			if c.String("evidence-out") != "" {
				return e.New(e.CodeUnsupported,
					"attest --evidence-out cannot issue a reproduction verdict for the publisher's own build; operator observations belong to qualified verify")
			}
			if c.String("release-member") != "" {
				return e.New(e.CodeUnsupported,
					"attest --release-member requires typed reproduction and resolved-input receipts; a local vector cannot fabricate them")
			}
			qualified, err := runQualifiedLocalBuild(c, c.String("source-root"))
			if err != nil {
				if qualified != nil {
					printReport(qualified.Report)
				}
				return err
			}
			defer func() { _ = qualified.Report.Close() }()
			rep := qualified.Report
			res := qualified.Resolved
			m, err := attest.FromQualifiedReport(c.Context, rep)
			if err != nil {
				return err
			}
			g, err := buildgraph.Compile(res)
			if err != nil {
				return err
			}
			// Record the pinned AICR accelerated-runtime profile as provenance. Rendered
			// bundles remain standalone locally verified diagnostics in this release; the
			// publisher vector cannot acquire their authority through a caller-selected path.
			if res.AICRProfile != nil {
				p, err := profileFromActivation(res.AICRProfile, g.AICR)
				if err != nil {
					return err
				}
				ref := &attest.AICRRef{
					Identity: p.Identity, Digest: p.Digest, GeneratorDigest: p.GeneratorDigest, Repo: p.Repo,
				}
				m.AICR = ref
			}
			// Publisher evidence is assembled only from two opaque, locally verified
			// capabilities over this same qualified report: an exact-subject SBOM set and
			// report/graph-derived provenance. Caller-supplied evidence strings cannot
			// enter MarshalQualified.
			verifiedSBOM, err := buildVerifiedSBOMFromReport(c.Context, res, rep)
			if err != nil {
				return err
			}
			if gaps, err := verifiedSBOM.EvidenceGaps(); err != nil {
				return err
			} else if len(gaps) != 0 {
				fmt.Fprintf(os.Stderr, "SBOM contains %d explicit external-evidence gap(s); release authorization requires exact signed waivers\n", len(gaps))
			}
			qualifiedProvenance, err := provenance.BuildQualifiedFromReport(c.Context, rep, g)
			if err != nil {
				return err
			}
			publisherEvidence, err := attest.QualifyPublisherEvidence(c.Context, verifiedSBOM, qualifiedProvenance)
			if err != nil {
				return err
			}
			if err := m.AttachPublisherEvidence(publisherEvidence); err != nil {
				return err
			}
			b, err := m.MarshalQualified(c.Context)
			if err != nil {
				return err
			}
			out := c.String("out")
			bundle := c.String("bundle")
			if c.Bool("sign") || bundle != "" {
				if err := writeQualifiedSignedDocument(c, b, out, bundle); err != nil {
					return err
				}
			} else if err := writeAtomic(c.Context, out, b, 0o644); err != nil {
				return err
			}
			fmt.Printf("wrote %s — %s\n", out, m.Summary())
			if m.AICR != nil {
				fmt.Printf("AICR accelerated-runtime profile: %s @ %s (profile reference only; standalone `nkdctl aicr bundle` output remains unqualified across process boundaries)\n",
					m.AICR.Identity, m.AICR.Digest)
			}
			if c.Bool("sign") || bundle != "" {
				if bundle != "" {
					fmt.Printf("signed + bundled (mirrorable, airgap-verifiable): %s.sig + %s.pem + %s\n", out, out, bundle)
				} else {
					fmt.Printf("signed (detached, keyless): %s.sig + %s.pem\n", out, out)
				}
			} else {
				fmt.Println("(sign detached with --sign, add --bundle <path> for an airgap-verifiable bundle, or: cosign sign-blob --yes " + out + ")")
			}
			return nil
		},
	}
}

// verifyEvidenceDigests reconstructs the complete publisher-evidence aggregate
// from the terminal-qualified report and compares it to the authenticated vector
// receipt. Neither decoded manifests nor caller-provided digest strings can enter
// this authority boundary.
func verifyEvidenceDigests(ctx context.Context, vectorDocument trust.DocumentReceipt,
	res *buildgraph.Resolved, report *pipeline.Report,
) error {
	published, err := attest.VerifyPublishedVectorReceipt(ctx, vectorDocument)
	if err != nil {
		return err
	}
	g, err := buildgraph.Compile(res)
	if err != nil {
		return err
	}
	verified, err := buildVerifiedSBOMFromReport(ctx, res, report)
	if err != nil {
		return err
	}
	qualifiedProvenance, err := provenance.BuildQualifiedFromReport(ctx, report, g)
	if err != nil {
		return err
	}
	localEvidence, err := attest.QualifyPublisherEvidence(ctx, verified, qualifiedProvenance)
	if err != nil {
		return err
	}
	if err := localEvidence.VerifyAgainstPublishedVector(ctx, published); err != nil {
		return err
	}
	fmt.Println("qualified publisher evidence (SBOM, provenance, and exact artifact scope) matches the authenticated vector")
	return nil
}
