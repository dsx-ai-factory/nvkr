package sbom

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	"github.com/nhensley/nkdctl/pkg/nkd/catalog"
	"github.com/nhensley/nkdctl/pkg/nkd/criteria"
	nkderr "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/ir"
	"github.com/nhensley/nkdctl/pkg/nkd/pipeline"
	"github.com/nhensley/nkdctl/pkg/nkd/profile"
	"github.com/nhensley/nkdctl/pkg/nkd/recipe"
)

func evidenceDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func externalSBOMEvidence(component, artifactDigest string) []byte {
	return []byte(`{"bomFormat":"CycloneDX","specVersion":"1.5","metadata":{"component":{"name":"` + component +
		`","hashes":[{"alg":"SHA-256","content":"` + strings.TrimPrefix(artifactDigest, "sha256:") + `"}]}}}`)
}

func externalProvenanceEvidence(component, artifactDigest string) []byte {
	return []byte(`{"_type":"https://in-toto.io/Statement/v1","subject":[{"name":"` + component +
		`","digest":{"sha256":"` + strings.TrimPrefix(artifactDigest, "sha256:") +
		`"}}],"predicateType":"https://slsa.dev/provenance/v1","predicate":{"buildDefinition":{"buildType":"https://example.test/prebuilt/v1","externalParameters":{}},"runDetails":{"builder":{"id":"https://example.test/upstream-builder"}}}}`)
}

func prebuiltResolved(t testing.TB, evidence *catalog.ExternalEvidence) (*recipe.Resolved, string, string) {
	t.Helper()
	artifactDigest := evidenceDigest([]byte("prebuilt-artifact"))
	resolved := &recipe.Resolved{
		RecipeID: "prebuilt-recipe", Criteria: criteria.Criteria{OS: "ubuntu", Arch: "amd64"},
		Components: []catalog.NamedComponent{{Name: "prebuilt", Component: catalog.Component{
			Tag: "v1.0.0", SourceDate: 1, OutputType: "host-package",
			Prebuilt: &catalog.Prebuilt{URL: "https://upstream.example/prebuilt", Digests: map[string]string{"amd64": artifactDigest}, Evidence: evidence},
		}}},
	}
	graph, err := recipe.Compile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	recipeDigest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return resolved, recipeDigest, artifactDigest
}

func sbomCode(err error) nkderr.Code {
	if coded, ok := err.(*nkderr.Error); ok {
		return coded.Code
	}
	return ""
}

func TestBuildVerifiedReadsAndHashesExternalEvidence(t *testing.T) {
	expectedArtifactDigest := evidenceDigest([]byte("prebuilt-artifact"))
	sbomBytes := externalSBOMEvidence("prebuilt", expectedArtifactDigest)
	provenanceBytes := externalProvenanceEvidence("prebuilt", expectedArtifactDigest)
	evidence := &catalog.ExternalEvidence{
		SBOMRef: "https://upstream.example/sbom", SBOMDigest: evidenceDigest(sbomBytes),
		ProvenanceRef: "https://upstream.example/provenance", ProvenanceDigest: evidenceDigest(provenanceBytes),
	}
	resolved, recipeDigest, artifactDigest := prebuiltResolved(t, evidence)
	if artifactDigest != expectedArtifactDigest {
		t.Fatalf("test fixture artifact digest drifted: got %s want %s", artifactDigest, expectedArtifactDigest)
	}
	reads := map[string]int{}
	reader := EvidenceReaderFunc(func(_ context.Context, ref string) ([]byte, error) {
		reads[ref]++
		switch ref {
		case evidence.SBOMRef:
			return sbomBytes, nil
		case evidence.ProvenanceRef:
			return provenanceBytes, nil
		default:
			return nil, errors.New("unknown ref")
		}
	})
	set, err := BuildVerified(context.Background(), resolved, recipeDigest,
		map[string]string{"prebuilt": artifactDigest}, reader)
	if err != nil {
		t.Fatal(err)
	}
	if reads[evidence.SBOMRef] != 1 || reads[evidence.ProvenanceRef] != 1 {
		t.Fatalf("external evidence was not acquired exactly once: %+v", reads)
	}
	if err := set.RequireCompleteExternalEvidence(); err != nil {
		t.Fatal(err)
	}
	receipts, err := set.ExternalReceipts()
	if err != nil || len(receipts) != 2 {
		t.Fatalf("want two verified receipts, got %+v, %v", receipts, err)
	}
	aggregate, err := set.Aggregate()
	if err != nil {
		t.Fatal(err)
	}
	properties := map[string]string{}
	for _, property := range aggregate.Components[0].Properties {
		properties[property.Name] = property.Value
	}
	if properties["nkd:externalEvidence"] != "verified" ||
		properties["nkd:externalSbomDigest"] != evidence.SBOMDigest ||
		properties["nkd:externalProvenanceDigest"] != evidence.ProvenanceDigest {
		t.Fatalf("verified bindings missing from SBOM: %+v", properties)
	}

	declared := fromRecipe(resolved, recipeDigest, map[string]string{"prebuilt": artifactDigest}, false)
	declaredProperties := map[string]string{}
	for _, property := range declared.Components[0].Properties {
		declaredProperties[property.Name] = property.Value
	}
	if declaredProperties["nkd:externalEvidence"] != "unverified-declaration" ||
		declaredProperties["nkd:externalSbomRef"] != "" {
		t.Fatalf("declaration projection copied an unverified reference: %+v", declaredProperties)
	}
}

func TestBuildVerifiedRejectsTamperedAbsentAndUnpinnedMaterial(t *testing.T) {
	body := []byte(`{"bomFormat":"CycloneDX"}`)
	evidence := &catalog.ExternalEvidence{SBOMRef: "https://upstream.example/sbom", SBOMDigest: evidenceDigest(body)}
	resolved, recipeDigest, artifactDigest := prebuiltResolved(t, evidence)
	artifacts := map[string]string{"prebuilt": artifactDigest}

	tampered := EvidenceReaderFunc(func(context.Context, string) ([]byte, error) { return []byte("tampered"), nil })
	if _, err := BuildVerified(context.Background(), resolved, recipeDigest, artifacts, tampered); sbomCode(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("tampered external evidence must fail closed, got %v", err)
	}
	if _, err := BuildVerified(context.Background(), resolved, recipeDigest, artifacts, nil); sbomCode(err) != nkderr.CodeUnavailable {
		t.Fatalf("missing reader must fail closed, got %v", err)
	}
	if _, err := BuildVerified(context.Background(), resolved, recipeDigest, nil, tampered); sbomCode(err) != nkderr.CodeProvenanceMissing {
		t.Fatalf("missing artifact digest must fail closed, got %v", err)
	}
	if _, err := BuildVerified(context.Background(), resolved, recipeDigest,
		map[string]string{"prebuilt": "sha256:" + strings.Repeat("f", 64)}, tampered); sbomCode(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("caller subject differing from the recipe-pinned prebuilt artifact must fail closed, got %v", err)
	}
	unpinned := *evidence
	unpinned.SBOMDigest = "latest"
	resolved, recipeDigest, artifactDigest = prebuiltResolved(t, &unpinned)
	if _, err := BuildVerified(context.Background(), resolved, recipeDigest,
		map[string]string{"prebuilt": artifactDigest}, tampered); sbomCode(err) != nkderr.CodeUnpinnedInput {
		t.Fatalf("unpinned external evidence must fail closed, got %v", err)
	}
}

func TestEvidenceNoneIsWaiverSignalNeverReceipt(t *testing.T) {
	resolved, recipeDigest, artifactDigest := prebuiltResolved(t, &catalog.ExternalEvidence{None: true})
	set, err := BuildVerified(context.Background(), resolved, recipeDigest,
		map[string]string{"prebuilt": artifactDigest}, nil)
	if err != nil {
		t.Fatal(err)
	}
	receipts, _ := set.ExternalReceipts()
	if len(receipts) != 0 {
		t.Fatalf("evidence:none produced passing receipts: %+v", receipts)
	}
	gaps, _ := set.EvidenceGaps()
	if len(gaps) != 1 || gaps[0].Reason != "evidence:none" || strings.Join(gaps[0].MissingTypes, ",") != "provenance,sbom" {
		t.Fatalf("evidence:none was not preserved as an explicit waiver signal: %+v", gaps)
	}
	if err := set.RequireCompleteExternalEvidence(); sbomCode(err) != nkderr.CodeProvenanceMissing {
		t.Fatalf("evidence:none must never satisfy completeness, got %v", err)
	}
	aggregate, _ := set.Aggregate()
	foundNone := false
	for _, property := range aggregate.Components[0].Properties {
		foundNone = foundNone || (property.Name == "nkd:externalEvidence" && property.Value == "none")
	}
	if !foundNone {
		t.Fatal("SBOM lost the explicit evidence:none signal")
	}
}

func TestBuildVerifiedHonorsContext(t *testing.T) {
	body := []byte(`{"bomFormat":"CycloneDX"}`)
	resolved, recipeDigest, artifactDigest := prebuiltResolved(t, &catalog.ExternalEvidence{
		SBOMRef: "https://upstream.example/sbom", SBOMDigest: evidenceDigest(body),
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader := EvidenceReaderFunc(func(context.Context, string) ([]byte, error) { return body, nil })
	if _, err := BuildVerified(ctx, resolved, recipeDigest,
		map[string]string{"prebuilt": artifactDigest}, reader); sbomCode(err) != nkderr.CodeTimeout {
		t.Fatalf("canceled acquisition must return timeout, got %v", err)
	}
}

func TestBuildVerifiedRequiresExactArtifactSetAndCanonicalReferences(t *testing.T) {
	body := []byte(`{"bomFormat":"CycloneDX"}`)
	evidence := &catalog.ExternalEvidence{
		SBOMRef: "https://upstream.example/sbom", SBOMDigest: evidenceDigest(body),
	}
	resolved, recipeDigest, artifactDigest := prebuiltResolved(t, evidence)
	readerCalls := 0
	reader := EvidenceReaderFunc(func(context.Context, string) ([]byte, error) {
		readerCalls++
		return body, nil
	})

	if _, err := BuildVerified(nil, resolved, recipeDigest, //nolint:staticcheck // verifies the nil-context fail-closed contract
		map[string]string{"prebuilt": artifactDigest}, reader); sbomCode(err) != nkderr.CodeUsage {
		t.Fatalf("nil context must fail as usage, got %v", err)
	}
	if _, err := BuildVerified(context.Background(), resolved, recipeDigest,
		map[string]string{"prebuilt": artifactDigest, "unselected": artifactDigest}, reader); sbomCode(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("an extra artifact identity must fail closed, got %v", err)
	}
	if readerCalls != 0 {
		t.Fatalf("invalid artifact set reached external I/O: %d calls", readerCalls)
	}

	badRef := *evidence
	badRef.SBOMRef += " "
	resolved, recipeDigest, artifactDigest = prebuiltResolved(t, &badRef)
	if _, err := BuildVerified(context.Background(), resolved, recipeDigest,
		map[string]string{"prebuilt": artifactDigest}, reader); sbomCode(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("non-canonical evidence reference must fail closed, got %v", err)
	}
	if readerCalls != 0 {
		t.Fatalf("non-canonical reference reached external I/O: %d calls", readerCalls)
	}
}

func TestBuildDiagnosticSelectedScopesCallerObservationWithoutMintingAuthority(t *testing.T) {
	resolved, report := qualifiedSBOMReport(t, []string{"beta"}, nil)
	defer func() { _ = report.Close() }()
	if len(report.Artifacts) != 1 || report.Artifacts[0].Component != "beta" {
		t.Fatalf("fixture did not select beta: %+v", report.Artifacts)
	}
	hashes := map[string]string{"beta": report.Artifacts[0].SHA256}

	set, err := BuildDiagnosticSelected(context.Background(), resolved, report.RecipeDigest, hashes, nil)
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err := set.Aggregate()
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregate.Components) != 1 || aggregate.Components[0].Name != "beta" {
		t.Fatalf("diagnostic SBOM leaked the unobserved recipe members: %+v", aggregate.Components)
	}
	if _, err := set.LocalReceipts(); sbomCode(err) != nkderr.CodePreflightFailed {
		t.Fatalf("diagnostic subset minted publisher authority: %v", err)
	}
	if _, err := BuildVerified(context.Background(), resolved, report.RecipeDigest, hashes, nil); sbomCode(err) != nkderr.CodeProvenanceMissing {
		t.Fatalf("exhaustive BuildVerified silently accepted a subset: %v", err)
	}
	if _, err := BuildDiagnosticSelected(context.Background(), resolved, report.RecipeDigest,
		map[string]string{"unknown": report.Artifacts[0].SHA256}, nil); sbomCode(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("unknown diagnostic artifact was accepted: %v", err)
	}
}

func TestBuildVerifiedSnapshotsCallerInputsBeforeExternalIO(t *testing.T) {
	expectedArtifactDigest := evidenceDigest([]byte("prebuilt-artifact"))
	body := externalSBOMEvidence("prebuilt", expectedArtifactDigest)
	evidence := &catalog.ExternalEvidence{
		SBOMRef: "https://upstream.example/sbom", SBOMDigest: evidenceDigest(body),
	}
	resolved, recipeDigest, artifactDigest := prebuiltResolved(t, evidence)
	hashes := map[string]string{"prebuilt": artifactDigest}
	reader := EvidenceReaderFunc(func(context.Context, string) ([]byte, error) {
		hashes["prebuilt"] = "sha256:" + strings.Repeat("f", 64)
		resolved.Components[0].Prebuilt.Evidence.SBOMDigest = "sha256:" + strings.Repeat("e", 64)
		resolved.Components[0].Name = "mutated"
		return body, nil
	})

	set, err := BuildVerified(context.Background(), resolved, recipeDigest, hashes, reader)
	if err != nil {
		t.Fatal(err)
	}
	boms, index, err := set.PerArtifact()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := boms["prebuilt"]; !ok || len(index.Entries) != 1 || index.Entries[0].ArtifactSHA != artifactDigest {
		t.Fatalf("external adapter changed the qualified subject identity: boms=%v index=%+v", boms, index)
	}
}

func TestBuildVerifiedRejectsUntypedOrWrongSubjectExternalBytes(t *testing.T) {
	for name, body := range map[string][]byte{
		"untyped":       []byte(`"hello"`),
		"wrong-subject": externalSBOMEvidence("other", evidenceDigest([]byte("prebuilt-artifact"))),
		"wrong-digest":  externalSBOMEvidence("prebuilt", evidenceDigest([]byte("other-artifact"))),
	} {
		t.Run(name, func(t *testing.T) {
			evidence := &catalog.ExternalEvidence{
				SBOMRef: "https://upstream.example/sbom", SBOMDigest: evidenceDigest(body),
			}
			resolved, recipeDigest, artifactDigest := prebuiltResolved(t, evidence)
			reader := EvidenceReaderFunc(func(context.Context, string) ([]byte, error) { return body, nil })
			if _, err := BuildVerified(context.Background(), resolved, recipeDigest,
				map[string]string{"prebuilt": artifactDigest}, reader); sbomCode(err) != nkderr.CodeUnverifiedRef {
				t.Fatalf("external bytes without a matching typed subject produced authority: %v", err)
			}
		})
	}
}

func TestExternalCycloneDXCannotReplayOneSubjectAcrossConflictingArtifactHashes(t *testing.T) {
	artifactA := "sha256:" + strings.Repeat("a", 64)
	artifactB := "sha256:" + strings.Repeat("b", 64)
	body := []byte(`{"bomFormat":"CycloneDX","specVersion":"1.5","metadata":{"component":{"name":"prebuilt","hashes":[{"alg":"SHA-256","content":"` +
		strings.TrimPrefix(artifactA, "sha256:") + `"},{"alg":"SHA-256","content":"` +
		strings.TrimPrefix(artifactB, "sha256:") + `"}]}}}`)
	for name, artifactDigest := range map[string]string{"artifact-a": artifactA, "artifact-b": artifactB} {
		t.Run(name, func(t *testing.T) {
			evidence := &catalog.ExternalEvidence{
				SBOMRef: "https://upstream.example/sbom", SBOMDigest: evidenceDigest(body),
			}
			resolved, _, _ := prebuiltResolved(t, evidence)
			resolved.Components[0].Prebuilt.Digests["amd64"] = artifactDigest
			graph, err := recipe.Compile(resolved)
			if err != nil {
				t.Fatal(err)
			}
			recipeDigest, err := graph.Digest()
			if err != nil {
				t.Fatal(err)
			}
			reader := EvidenceReaderFunc(func(context.Context, string) ([]byte, error) {
				return append([]byte(nil), body...), nil
			})
			if _, err := BuildVerified(context.Background(), resolved, recipeDigest,
				map[string]string{"prebuilt": artifactDigest}, reader); sbomCode(err) != nkderr.CodeUnverifiedRef {
				t.Fatalf("conflicting SHA-256 rows minted external evidence for %s: %v", artifactDigest, err)
			}
		})
	}
}

func TestBuildVerifiedRejectsDuplicateResolvedComponents(t *testing.T) {
	resolved, recipeDigest, artifactDigest := prebuiltResolved(t, &catalog.ExternalEvidence{None: true})
	resolved.Components = append(resolved.Components, resolved.Components[0])
	if _, err := BuildVerified(context.Background(), resolved, recipeDigest,
		map[string]string{"prebuilt": artifactDigest}, nil); sbomCode(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("duplicate resolved component must fail closed, got %v", err)
	}
}

func TestBuildVerifiedFromReportIssuesExactFullAndSubsetPublisherReceipts(t *testing.T) {
	for _, test := range []struct {
		name     string
		only     []string
		want     []string
		wantGaps int
	}{
		{name: "full", want: []string{"alpha", "beta"}, wantGaps: 2},
		{name: "exact subset", only: []string{"beta"}, want: []string{"beta"}, wantGaps: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolved, report := qualifiedSBOMReport(t, test.only, nil)
			set, err := BuildVerifiedFromReport(context.Background(), resolved, report, nil)
			if err != nil {
				t.Fatal(err)
			}
			receipts, err := set.LocalReceipts()
			if err != nil {
				t.Fatal(err)
			}
			if len(receipts) != len(test.want) {
				t.Fatalf("got %d publisher receipts, want %d", len(receipts), len(test.want))
			}
			for index, receipt := range receipts {
				binding, err := receipt.Binding()
				if err != nil {
					t.Fatal(err)
				}
				if binding.Component != test.want[index] || binding.RecipeDigest != report.RecipeDigest {
					t.Fatalf("receipt[%d] lost full-recipe/subset binding: %+v", index, binding)
				}
			}
			boms, index, err := set.PerArtifact()
			if err != nil {
				t.Fatal(err)
			}
			if len(boms) != len(test.want) || len(index.Entries) != len(test.want) ||
				index.RecipeDigest != report.RecipeDigest {
				t.Fatalf("SBOM index scope drifted: boms=%v index=%+v", boms, index)
			}
			aggregate, err := set.Aggregate()
			if err != nil {
				t.Fatal(err)
			}
			if len(aggregate.Components) != len(test.want) {
				t.Fatalf("aggregate leaked unselected components: %+v", aggregate.Components)
			}
			gaps, err := set.EvidenceGaps()
			if err != nil || len(gaps) != test.wantGaps {
				t.Fatalf("external-evidence gaps did not retain exact subset scope: %+v, %v", gaps, err)
			}
		})
	}
}

func TestBuildVerifiedFromReportSBOMIndexIsMirrorInvariant(t *testing.T) {
	toolchainDigest := "sha256:" + strings.Repeat("c", 64)
	onlineResolved, onlineReport := qualifiedSourceSBOMReport(t,
		"https://source.online.example/agent.git",
		"registry.online.example/toolchain@"+toolchainDigest)
	mirrorResolved, mirrorReport := qualifiedSourceSBOMReport(t,
		"https://source.airgap.example/mirror/agent.git",
		"registry.airgap.example/mirror/toolchain@"+toolchainDigest)
	if onlineReport.RecipeDigest != mirrorReport.RecipeDigest {
		t.Fatalf("transport relocation changed recipe identity: %s != %s",
			onlineReport.RecipeDigest, mirrorReport.RecipeDigest)
	}

	online, err := BuildVerifiedFromReport(context.Background(), onlineResolved, onlineReport, nil)
	if err != nil {
		t.Fatal(err)
	}
	mirror, err := BuildVerifiedFromReport(context.Background(), mirrorResolved, mirrorReport, nil)
	if err != nil {
		t.Fatal(err)
	}
	onlineBOMs, onlineIndex, err := online.PerArtifact()
	if err != nil {
		t.Fatal(err)
	}
	mirrorBOMs, mirrorIndex, err := mirror.PerArtifact()
	if err != nil {
		t.Fatal(err)
	}
	onlineIndexDigest, err := onlineIndex.Digest()
	if err != nil {
		t.Fatal(err)
	}
	mirrorIndexDigest, err := mirrorIndex.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if onlineIndexDigest != mirrorIndexDigest {
		t.Fatalf("source/toolchain mirror relocation changed SBOM index: %s != %s",
			onlineIndexDigest, mirrorIndexDigest)
	}
	onlineBOMDigest, err := onlineBOMs["agent"].Digest()
	if err != nil {
		t.Fatal(err)
	}
	mirrorBOMDigest, err := mirrorBOMs["agent"].Digest()
	if err != nil {
		t.Fatal(err)
	}
	if onlineBOMDigest != mirrorBOMDigest {
		t.Fatalf("source/toolchain mirror relocation changed artifact SBOM: %s != %s",
			onlineBOMDigest, mirrorBOMDigest)
	}
	component := onlineBOMs["agent"].Metadata.Component
	if component.PURL != "" {
		t.Fatalf("publisher SBOM retained transport-derived purl %q", component.PURL)
	}
	properties := make(map[string]string, len(component.Properties))
	for _, property := range component.Properties {
		properties[property.Name] = property.Value
	}
	if properties["nkd:repo"] != "" || properties["nkd:source"] != "nkd:source/agent" ||
		properties["nkd:toolchain"] != toolchainDigest {
		t.Fatalf("publisher SBOM lost semantic source/toolchain projection: %+v", properties)
	}
}

func TestBuildVerifiedSeparatesExternalVerificationFromPublisherAuthority(t *testing.T) {
	expectedArtifactDigest := evidenceDigest([]byte("prebuilt-artifact"))
	sbomBytes := externalSBOMEvidence("prebuilt", expectedArtifactDigest)
	provenanceBytes := externalProvenanceEvidence("prebuilt", expectedArtifactDigest)
	evidence := &catalog.ExternalEvidence{
		SBOMRef: "https://upstream.example/sbom", SBOMDigest: evidenceDigest(sbomBytes),
		ProvenanceRef: "https://upstream.example/provenance", ProvenanceDigest: evidenceDigest(provenanceBytes),
	}
	resolved, recipeDigest, artifactDigest := prebuiltResolved(t, evidence)
	reader := EvidenceReaderFunc(func(_ context.Context, ref string) ([]byte, error) {
		switch ref {
		case evidence.SBOMRef:
			return sbomBytes, nil
		case evidence.ProvenanceRef:
			return provenanceBytes, nil
		default:
			return nil, errors.New("unknown reference")
		}
	})
	set, err := BuildVerified(context.Background(), resolved, recipeDigest,
		map[string]string{"prebuilt": artifactDigest}, reader)
	if err != nil {
		t.Fatal(err)
	}
	external, err := set.ExternalReceipts()
	if err != nil || len(external) != 2 {
		t.Fatalf("caller-hash path lost independently verified external receipts: %+v, %v", external, err)
	}
	if _, err := set.LocalReceipts(); sbomCode(err) != nkderr.CodePreflightFailed {
		t.Fatalf("caller artifact hashes minted publisher SBOM authority: %v", err)
	}
}

func TestBuildVerifiedFromReportRejectsFabricationAndCallbackMutation(t *testing.T) {
	resolved, report := qualifiedSBOMReport(t, []string{"alpha"}, nil)
	assembled := &pipeline.Report{
		RecipeID: report.RecipeID, RecipeDigest: report.RecipeDigest,
		Artifacts: append([]pipeline.ArtifactResult(nil), report.Artifacts...),
	}
	if _, err := BuildVerifiedFromReport(context.Background(), resolved, assembled, nil); sbomCode(err) != nkderr.CodePreflightFailed {
		t.Fatalf("caller-assembled report minted publisher SBOM authority: %v", err)
	}

	missing := *report
	missing.Artifacts = nil
	if _, err := BuildVerifiedFromReport(context.Background(), resolved, &missing, nil); sbomCode(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("report artifact omission did not fail its opaque receipt: %v", err)
	}

	wrongResolved, _ := snapshotResolved(resolved)
	wrongResolved.Components[0].Prebuilt.Digests["amd64"] = "sha256:" + strings.Repeat("9", 64)
	if _, err := BuildVerifiedFromReport(context.Background(), wrongResolved, report, nil); sbomCode(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("caller recipe projection substitution did not fail closed: %v", err)
	}

	artifactDigest := report.Artifacts[0].SHA256
	body := externalSBOMEvidence("alpha", artifactDigest)
	external := &catalog.ExternalEvidence{
		SBOMRef: "https://upstream.example/alpha.sbom", SBOMDigest: evidenceDigest(body),
	}
	resolved, report = qualifiedSBOMReport(t, []string{"alpha"}, map[string]*catalog.ExternalEvidence{"alpha": external})
	reader := EvidenceReaderFunc(func(context.Context, string) ([]byte, error) {
		report.Artifacts[0].SHA256 = "sha256:" + strings.Repeat("f", 64)
		return body, nil
	})
	if _, err := BuildVerifiedFromReport(context.Background(), resolved, report, reader); sbomCode(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("external callback mutated qualified report without invalidating authority: %v", err)
	}
}

type fixtureOCITransport struct{}

type fixtureOCILayout struct {
	digest string
	files  map[string][]byte
}

func newFixtureOCILayout(component string) fixtureOCILayout {
	config := []byte(`{"component":"` + component + `"}`)
	configDigest := evidenceDigest(config)
	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"` +
		configDigest + `","size":` + strconv.Itoa(len(config)) + `},"layers":[]}`)
	manifestDigest := evidenceDigest(manifest)
	index := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"` +
		manifestDigest + `","size":` + strconv.Itoa(len(manifest)) + `}]}`)
	return fixtureOCILayout{digest: manifestDigest, files: map[string][]byte{
		"oci-layout": []byte(`{"imageLayoutVersion":"1.0.0"}`),
		"index.json": index,
		filepath.Join("blobs", "sha256", strings.TrimPrefix(configDigest, "sha256:")):   config,
		filepath.Join("blobs", "sha256", strings.TrimPrefix(manifestDigest, "sha256:")): manifest,
	}}
}

type sourceSBOMExecutionObserver struct {
	observation profile.ExecutionObservation
}

func (observer sourceSBOMExecutionObserver) ObserveExecution(context.Context,
	pipeline.ExecutionObservationRequest,
) (profile.ExecutionObservation, error) {
	return observer.observation, nil
}

func (fixtureOCITransport) Acquire(ctx context.Context, digestRef, destination string) (string, int64, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	digest := digestRef[strings.LastIndex(digestRef, "@")+1:]
	reference := digestRef[:strings.LastIndex(digestRef, "@")]
	component := reference[strings.LastIndex(reference, "/")+1:]
	layout := newFixtureOCILayout(component)
	if digest != layout.digest {
		return "", 0, errors.New("fixture OCI digest does not match requested reference")
	}
	if err := os.MkdirAll(filepath.Join(destination, "blobs", "sha256"), 0o755); err != nil {
		return "", 0, err
	}
	var size int64
	for relative, raw := range layout.files {
		if err := os.WriteFile(filepath.Join(destination, relative), raw, 0o600); err != nil {
			return "", 0, err
		}
		size += int64(len(raw))
	}
	return digest, size, nil
}

func (fixtureOCITransport) Verify(context.Context, string, string) error { return nil }

func qualifiedSBOMReport(t *testing.T, only []string,
	evidence map[string]*catalog.ExternalEvidence,
) (*recipe.Resolved, *pipeline.Report) {
	t.Helper()
	resolved := &recipe.Resolved{
		RecipeID: "publisher-sbom-fixture",
		Criteria: criteria.Criteria{OS: "ubuntu", Arch: "amd64"},
	}
	for _, name := range []string{"alpha", "beta"} {
		declaredEvidence := &catalog.ExternalEvidence{None: true}
		if replacement := evidence[name]; replacement != nil {
			declaredEvidence = replacement
		}
		resolved.Components = append(resolved.Components, catalog.NamedComponent{
			Name: name,
			Component: catalog.Component{
				Tag: "v1.0.0", OutputType: "oci-image",
				Prebuilt: &catalog.Prebuilt{
					Image:    "registry.example.test/" + name,
					Digests:  map[string]string{"amd64": newFixtureOCILayout(name).digest},
					Evidence: declaredEvidence,
				},
			},
		})
	}
	graph, err := recipe.Compile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	recipeDigest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(t.TempDir(), "work")
	report, err := (pipeline.Local{}).Run(graph, recipeDigest, pipeline.RunOptions{
		Ctx: context.Background(), SourceRoot: filepath.Join(work, "src"),
		Only: only, OCI: fixtureOCITransport{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return resolved, report
}

func qualifiedSourceSBOMReport(t *testing.T, repo, toolchain string) (*recipe.Resolved, *pipeline.Report) {
	t.Helper()
	resolved := &recipe.Resolved{
		RecipeID: "publisher-source-sbom-fixture",
		Criteria: criteria.Criteria{OS: "ubuntu", Arch: "amd64"},
		Components: []catalog.NamedComponent{{
			Name: "agent",
			Component: catalog.Component{
				FromSource: true, Repo: repo, Tag: "v1.0.0",
				Commit: strings.Repeat("d", 40), TreeHash: strings.Repeat("e", 40),
				ToolchainImage: toolchain, OutputType: "raw",
				BuildCommand: []string{"true"}, ArtifactPath: "out/agent", WriteDir: "out",
				SourceDate: 1,
			},
		}},
	}
	graph, err := recipe.Compile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	recipeDigest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	var build ir.Node
	for _, node := range graph.Nodes {
		if node.Kind == ir.KindBuild {
			build = node
		}
	}
	if build.ID == "" {
		t.Fatal("source fixture compiled no build node")
	}

	runnerDigest := "sha256:" + strings.Repeat("a", 64)
	runtimeDigest := "sha256:" + strings.Repeat("b", 64)
	toolchainDigest := toolchain[strings.LastIndex(toolchain, "@")+1:]
	builderProfile := profile.BuilderProfile{
		SchemaVersion: profile.SchemaVersion, Identity: "publisher-source-fixture", Version: "1",
		HostControlClass: profile.OperatorControlled, Provides: []string{ir.BuilderOCIHermetic},
		Fields: []profile.Field{
			{Name: profile.FieldTargetArch, Value: "amd64", HardFail: true, MeasurementStage: profile.MeasurementHost},
			{Name: profile.FieldRunnerDigest, Value: runnerDigest, HardFail: true, MeasurementStage: profile.MeasurementExecution},
			{Name: profile.FieldRuntimeIdentity, Value: runtimeDigest, HardFail: true, MeasurementStage: profile.MeasurementExecution},
			{Name: profile.FieldOCISandboxMode, Value: "fixture-sandbox", HardFail: true, MeasurementStage: profile.MeasurementExecution},
		},
	}
	host, divergences, err := builderProfile.VerifyHost(context.Background(),
		map[string]string{profile.FieldTargetArch: "amd64"})
	if err != nil || len(divergences) != 0 {
		t.Fatalf("source fixture host preflight: %v %+v", err, divergences)
	}
	observation := profile.ExecutionObservation{
		RunnerDigest: runnerDigest, RunnerOrigin: profile.RunnerPreexisting,
		RuntimeIdentity: runtimeDigest,
		Nodes: []profile.NodeExecutionObservation{{
			NodeID: build.ID, EngineClass: ir.EngineOCIHermetic,
			ToolchainDigest: toolchainDigest, SandboxMode: "fixture-sandbox",
		}},
	}
	execution, err := profile.VerifyExecution(context.Background(), host, builderProfile,
		[]ir.Node{build}, observation)
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(t.TempDir(), "work")
	options := pipeline.RunOptions{
		Ctx: context.Background(), SourceRoot: filepath.Join(work, "src"), Runtime: os.Args[0],
		Qualification: &pipeline.QualifiedExecution{
			Profile: builderProfile, HostReceipt: host,
			Observer: sourceSBOMExecutionObserver{observation: observation},
		},
	}
	artifactPath := pipeline.ArtifactPath(options, build)
	payload := []byte("mirror-invariant-built-artifact")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifactPath, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	result := pipeline.ArtifactResult{
		Component: build.Component, OutputType: build.OutputType, Toolchain: build.Toolchain,
		Qualification: pipeline.ArtifactQualifiedExecution,
		InputDigest:   "sha256:" + strings.Repeat("f", 64),
		BuilderType:   build.BuilderType, EngineClass: ir.EngineOCIHermetic,
		ProfileDigest: execution.ProfileDigest(), ExecutionBinding: execution.ExecutionBinding(),
		Path: build.ArtifactPath, SHA256: evidenceDigest(payload), Size: int64(len(payload)),
	}
	resultBytes, err := canonical.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	resultsDir := pipeline.ResultDirectory(options.SourceRoot)
	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resultsDir, build.ID+".json"), resultBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := (pipeline.Local{}).VerifyResults(graph, recipeDigest, resultsDir, options)
	if err != nil {
		t.Fatal(err)
	}
	return resolved, report
}

var _ pipeline.OCITransport = fixtureOCITransport{}
