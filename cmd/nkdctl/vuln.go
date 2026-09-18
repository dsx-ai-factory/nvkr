package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/buildgraph"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/vuln"
	"github.com/urfave/cli/v2"
)

// runVulnScan is intentionally fail-closed in the stock CLI. Merely finding a
// scanner binary and parsing whatever it prints cannot prove which binary and DB
// bytes produced a result, nor that networking was denied. Product integrations
// may inject a release scanner through runVulnScanWithScanner; only QualifyScan can
// issue the report capability.
func runVulnScan(c *cli.Context, resolved *buildgraph.Resolved, buildDigest string,
	artifactHashes map[string]string,
) (string, error) {
	return "", e.New(e.CodeUnsupported,
		"qualified vulnerability scanning is disabled until a network-denied scanner adapter can report observed scanner and database identities")
}

func runVulnScanWithScanner(c *cli.Context, resolved *buildgraph.Resolved, buildDigest string,
	artifactHashes map[string]string, scanner vuln.ReleaseScanner,
) (string, error) {
	component, artifactDigest, err := selectVulnerabilitySubject(c.String("vuln-artifact"), artifactHashes)
	if err != nil {
		return "", err
	}
	verifiedSBOM, err := buildVerifiedSBOM(c.Context, resolved, buildDigest, artifactHashes)
	if err != nil {
		return "", err
	}
	boms, _, err := verifiedSBOM.PerArtifact()
	if err != nil {
		return "", err
	}
	bom := boms[component]
	if bom == nil {
		return "", e.New(e.CodeProvenanceMissing,
			"qualified vulnerability subject %q has no verified per-artifact SBOM", component)
	}
	sbomBytes, err := bom.Marshal()
	if err != nil {
		return "", err
	}
	sbomDigest, err := bom.Digest()
	if err != nil {
		return "", err
	}
	qualified, err := vuln.QualifyScan(c.Context, vuln.QualifiedScanRequest{
		Scanner: c.String("vuln-scanner"), ScannerDigest: c.String("vuln-scanner-digest"),
		DatabaseDigest: c.String("vuln-db"), BuildDigest: buildDigest,
		ArtifactDigest: artifactDigest, SBOMDigest: sbomDigest, CanonicalSBOM: sbomBytes,
	}, scanner)
	if err != nil {
		if scanner == nil {
			return "", e.Wrap(e.CodeUnsupported, err,
				"qualified vulnerability scanning is disabled until a network-denied scanner adapter can report observed scanner and database identities")
		}
		return "", err
	}
	raw, err := qualified.Marshal()
	if err != nil {
		return "", err
	}
	if err := writeAtomic(c.Context, c.String("vuln-out"), raw, 0o644); err != nil {
		return "", err
	}
	digest, err := qualified.Digest()
	if err != nil {
		return "", err
	}
	report, err := qualified.Report()
	if err != nil {
		return "", err
	}
	fmt.Printf("wrote qualified vuln report %s (%d finding(s), artifact %s, scanner %s, db %s) → %s\n",
		digest, len(report.Findings), component, report.ScannerDigest, report.DatabaseDigest, c.String("vuln-out"))
	return digest, nil
}

func selectVulnerabilitySubject(requested string, artifactHashes map[string]string) (string, string, error) {
	requested = strings.TrimSpace(requested)
	if requested != "" {
		digest := artifactHashes[requested]
		if digest == "" {
			return "", "", e.New(e.CodeUsage, "--vuln-artifact %q is not in the built artifact set", requested)
		}
		return requested, digest, nil
	}
	if len(artifactHashes) != 1 {
		names := make([]string, 0, len(artifactHashes))
		for name := range artifactHashes {
			names = append(names, name)
		}
		sort.Strings(names)
		return "", "", e.New(e.CodeUsage,
			"--vuln-artifact is required when the release has %d artifacts (%s)", len(names), strings.Join(names, ", "))
	}
	for name, digest := range artifactHashes {
		return name, digest, nil
	}
	return "", "", e.New(e.CodeProvenanceMissing, "qualified vulnerability scan has no artifact subject")
}

// vulnerabilityScannerFunc is the focused test/product adapter seam. The stock
// CLI intentionally does not install one until it can enforce offline execution.
type vulnerabilityScannerFunc func(context.Context, vuln.QualifiedScanRequest) (vuln.ScanObservation, error)

func (function vulnerabilityScannerFunc) Scan(ctx context.Context, request vuln.QualifiedScanRequest) (vuln.ScanObservation, error) {
	return function(ctx, request)
}

var _ vuln.ReleaseScanner = vulnerabilityScannerFunc(nil)
