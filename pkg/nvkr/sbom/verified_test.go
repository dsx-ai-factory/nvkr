// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
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

func prebuiltResolved(t testing.TB, evidence *catalog.ExternalEvidence) (*buildgraph.Resolved, string, string) {
	t.Helper()
	artifactDigest := evidenceDigest([]byte("prebuilt-artifact"))
	resolved := &buildgraph.Resolved{
		Platform: "linux/amd64",
		Components: []catalog.NamedComponent{{Name: "prebuilt", Component: catalog.Component{Platforms: []string{"linux/amd64"},
			Tag: "v1.0.0", SourceDate: 1, OutputType: "host-package",
			Prebuilt: &catalog.Prebuilt{URL: "https://upstream.example/prebuilt", Digests: map[string]string{"amd64": artifactDigest}, Evidence: evidence},
		}}},
	}
	graph, err := buildgraph.Compile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	buildDigest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return resolved, buildDigest, artifactDigest
}

func sbomCode(err error) nvkrerr.Code {
	if coded, ok := err.(*nvkrerr.Error); ok {
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
	resolved, buildDigest, artifactDigest := prebuiltResolved(t, evidence)
	if artifactDigest != expectedArtifactDigest {
		t.Fatalf("test fixture artifact digest drifted: got %s want %s", artifactDigest, expectedArtifactDigest)
	}
	reads := map[string]int{}
	reader := EvidenceReaderFunc(func(_ context.Context, ref string, _ int64) ([]byte, error) {
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
	set, err := BuildVerified(context.Background(), resolved, buildDigest,
		map[string]string{"prebuilt": artifactDigest}, reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reads[evidence.SBOMRef] != 1 || reads[evidence.ProvenanceRef] != 1 {
		t.Fatalf("external evidence was not acquired exactly once: %+v", reads)
	}
	if gaps, err := set.EvidenceGaps(); err != nil || len(gaps) != 0 {
		t.Fatalf("complete verified evidence retained gaps: %+v, %v", gaps, err)
	}
	aggregate, err := set.Aggregate()
	if err != nil {
		t.Fatal(err)
	}
	properties := map[string]string{}
	for _, property := range aggregate.Components[0].Properties {
		properties[property.Name] = property.Value
	}
	if properties["nvkr:externalEvidence"] != "verified" ||
		properties["nvkr:externalSbomDigest"] != evidence.SBOMDigest ||
		properties["nvkr:externalProvenanceDigest"] != evidence.ProvenanceDigest {
		t.Fatalf("verified bindings missing from SBOM: %+v", properties)
	}

	declared := fromBuild(resolved, buildDigest, map[string]string{"prebuilt": artifactDigest}, false)
	declaredProperties := map[string]string{}
	for _, property := range declared.Components[0].Properties {
		declaredProperties[property.Name] = property.Value
	}
	if declaredProperties["nvkr:externalEvidence"] != "unverified-declaration" ||
		declaredProperties["nvkr:externalSbomRef"] != "" {
		t.Fatalf("declaration projection copied an unverified reference: %+v", declaredProperties)
	}
}

func TestBuildVerifiedRejectsTamperedAbsentAndUnpinnedMaterial(t *testing.T) {
	body := []byte(`{"bomFormat":"CycloneDX"}`)
	evidence := &catalog.ExternalEvidence{SBOMRef: "https://upstream.example/sbom", SBOMDigest: evidenceDigest(body)}
	resolved, buildDigest, artifactDigest := prebuiltResolved(t, evidence)
	artifacts := map[string]string{"prebuilt": artifactDigest}

	tampered := EvidenceReaderFunc(func(context.Context, string, int64) ([]byte, error) { return []byte("tampered"), nil })
	if _, err := BuildVerified(context.Background(), resolved, buildDigest, artifacts, tampered, nil); sbomCode(err) != nvkrerr.CodeUnverifiedRef {
		t.Fatalf("tampered external evidence must fail closed, got %v", err)
	}
	if _, err := BuildVerified(context.Background(), resolved, buildDigest, artifacts, nil, nil); sbomCode(err) != nvkrerr.CodeUnavailable {
		t.Fatalf("missing reader must fail closed, got %v", err)
	}
	if _, err := BuildVerified(context.Background(), resolved, buildDigest, nil, tampered, nil); sbomCode(err) != nvkrerr.CodeProvenanceMissing {
		t.Fatalf("missing artifact digest must fail closed, got %v", err)
	}
	if _, err := BuildVerified(context.Background(), resolved, buildDigest,
		map[string]string{"prebuilt": "sha256:" + strings.Repeat("f", 64)}, tampered, nil); sbomCode(err) != nvkrerr.CodeUnverifiedRef {
		t.Fatalf("caller subject differing from the build-pinned prebuilt artifact must fail closed, got %v", err)
	}
	unpinned := *evidence
	unpinned.SBOMDigest = "latest"
	resolved.Components[0].Prebuilt.Evidence = &unpinned
	if _, err := BuildVerified(context.Background(), resolved, buildDigest,
		map[string]string{"prebuilt": artifactDigest}, tampered, nil); sbomCode(err) != nvkrerr.CodeUnpinnedSource {
		t.Fatalf("unpinned external evidence must fail closed, got %v", err)
	}
}

func TestEvidenceNoneIsWaiverSignalNeverReceipt(t *testing.T) {
	resolved, buildDigest, artifactDigest := prebuiltResolved(t, &catalog.ExternalEvidence{None: true})
	set, err := BuildVerified(context.Background(), resolved, buildDigest,
		map[string]string{"prebuilt": artifactDigest}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	gaps, _ := set.EvidenceGaps()
	if len(gaps) != 1 || gaps[0].Reason != "evidence:none" || strings.Join(gaps[0].MissingTypes, ",") != "provenance,sbom" {
		t.Fatalf("evidence:none was not preserved as an explicit waiver signal: %+v", gaps)
	}
	aggregate, _ := set.Aggregate()
	foundNone := false
	for _, property := range aggregate.Components[0].Properties {
		foundNone = foundNone || (property.Name == "nvkr:externalEvidence" && property.Value == "none")
	}
	if !foundNone {
		t.Fatal("SBOM lost the explicit evidence:none signal")
	}
}

func TestBuildVerifiedHonorsContext(t *testing.T) {
	body := []byte(`{"bomFormat":"CycloneDX"}`)
	resolved, buildDigest, artifactDigest := prebuiltResolved(t, &catalog.ExternalEvidence{
		SBOMRef: "https://upstream.example/sbom", SBOMDigest: evidenceDigest(body),
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader := EvidenceReaderFunc(func(context.Context, string, int64) ([]byte, error) { return body, nil })
	if _, err := BuildVerified(ctx, resolved, buildDigest,
		map[string]string{"prebuilt": artifactDigest}, reader, nil); sbomCode(err) != nvkrerr.CodeTimeout {
		t.Fatalf("canceled acquisition must return timeout, got %v", err)
	}
}

func TestBuildVerifiedRequiresExactArtifactSetAndCanonicalReferences(t *testing.T) {
	body := []byte(`{"bomFormat":"CycloneDX"}`)
	evidence := &catalog.ExternalEvidence{
		SBOMRef: "https://upstream.example/sbom", SBOMDigest: evidenceDigest(body),
	}
	resolved, buildDigest, artifactDigest := prebuiltResolved(t, evidence)
	readerCalls := 0
	reader := EvidenceReaderFunc(func(context.Context, string, int64) ([]byte, error) {
		readerCalls++
		return body, nil
	})

	if _, err := BuildVerified(nil, resolved, buildDigest, //nolint:staticcheck // verifies the nil-context fail-closed contract
		map[string]string{"prebuilt": artifactDigest}, reader, nil); sbomCode(err) != nvkrerr.CodeUsage {
		t.Fatalf("nil context must fail as usage, got %v", err)
	}
	if _, err := BuildVerified(context.Background(), resolved, buildDigest,
		map[string]string{"prebuilt": artifactDigest, "unselected": artifactDigest}, reader, nil); sbomCode(err) != nvkrerr.CodeUnverifiedRef {
		t.Fatalf("an extra artifact identity must fail closed, got %v", err)
	}
	if readerCalls != 0 {
		t.Fatalf("invalid artifact set reached external I/O: %d calls", readerCalls)
	}

	badRef := *evidence
	badRef.SBOMRef += " "
	resolved, buildDigest, artifactDigest = prebuiltResolved(t, &badRef)
	if _, err := BuildVerified(context.Background(), resolved, buildDigest,
		map[string]string{"prebuilt": artifactDigest}, reader, nil); sbomCode(err) != nvkrerr.CodeUnverifiedRef {
		t.Fatalf("non-canonical evidence reference must fail closed, got %v", err)
	}
	if readerCalls != 0 {
		t.Fatalf("non-canonical reference reached external I/O: %d calls", readerCalls)
	}
}

func TestBuildDiagnosticSelectedScopesCallerObservationWithoutMintingAuthority(t *testing.T) {
	resolved, _, artifactDigest := prebuiltResolved(t, &catalog.ExternalEvidence{None: true})
	resolved.Components[0].Name = "alpha"
	beta := resolved.Components[0]
	beta.Name = "beta"
	resolved.Components = append(resolved.Components, beta)
	graph, err := buildgraph.Compile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	buildDigest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	hashes := map[string]string{"beta": artifactDigest}

	set, err := BuildDiagnosticSelected(context.Background(), resolved, buildDigest, hashes, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err := set.Aggregate()
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregate.Components) != 1 || aggregate.Components[0].Name != "beta" {
		t.Fatalf("diagnostic SBOM leaked the unobserved build members: %+v", aggregate.Components)
	}
	if _, err := set.LocalReceipts(); sbomCode(err) != nvkrerr.CodePreflightFailed {
		t.Fatalf("diagnostic subset minted publisher authority: %v", err)
	}
	if _, err := BuildVerified(context.Background(), resolved, buildDigest, hashes, nil, nil); sbomCode(err) != nvkrerr.CodeProvenanceMissing {
		t.Fatalf("exhaustive BuildVerified silently accepted a subset: %v", err)
	}
	if _, err := BuildDiagnosticSelected(context.Background(), resolved, buildDigest,
		map[string]string{"unknown": artifactDigest}, nil, nil); sbomCode(err) != nvkrerr.CodeUnverifiedRef {
		t.Fatalf("unknown diagnostic artifact was accepted: %v", err)
	}
}

func TestBuildVerifiedSnapshotsCallerInputsBeforeExternalIO(t *testing.T) {
	expectedArtifactDigest := evidenceDigest([]byte("prebuilt-artifact"))
	body := externalSBOMEvidence("prebuilt", expectedArtifactDigest)
	evidence := &catalog.ExternalEvidence{
		SBOMRef: "https://upstream.example/sbom", SBOMDigest: evidenceDigest(body),
	}
	resolved, buildDigest, artifactDigest := prebuiltResolved(t, evidence)
	hashes := map[string]string{"prebuilt": artifactDigest}
	reader := EvidenceReaderFunc(func(context.Context, string, int64) ([]byte, error) {
		hashes["prebuilt"] = "sha256:" + strings.Repeat("f", 64)
		resolved.Components[0].Prebuilt.Evidence.SBOMDigest = "sha256:" + strings.Repeat("e", 64)
		resolved.Components[0].Name = "mutated"
		return body, nil
	})

	set, err := BuildVerified(context.Background(), resolved, buildDigest, hashes, reader, nil)
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
			resolved, buildDigest, artifactDigest := prebuiltResolved(t, evidence)
			reader := EvidenceReaderFunc(func(context.Context, string, int64) ([]byte, error) { return body, nil })
			if _, err := BuildVerified(context.Background(), resolved, buildDigest,
				map[string]string{"prebuilt": artifactDigest}, reader, nil); sbomCode(err) != nvkrerr.CodeUnverifiedRef {
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
			graph, err := buildgraph.Compile(resolved)
			if err != nil {
				t.Fatal(err)
			}
			buildDigest, err := graph.Digest()
			if err != nil {
				t.Fatal(err)
			}
			reader := EvidenceReaderFunc(func(context.Context, string, int64) ([]byte, error) {
				return append([]byte(nil), body...), nil
			})
			if _, err := BuildVerified(context.Background(), resolved, buildDigest,
				map[string]string{"prebuilt": artifactDigest}, reader, nil); sbomCode(err) != nvkrerr.CodeUnverifiedRef {
				t.Fatalf("conflicting SHA-256 rows minted external evidence for %s: %v", artifactDigest, err)
			}
		})
	}
}

func TestBuildVerifiedRejectsDuplicateResolvedComponents(t *testing.T) {
	resolved, buildDigest, artifactDigest := prebuiltResolved(t, &catalog.ExternalEvidence{None: true})
	resolved.Components = append(resolved.Components, resolved.Components[0])
	if _, err := BuildVerified(context.Background(), resolved, buildDigest,
		map[string]string{"prebuilt": artifactDigest}, nil, nil); sbomCode(err) != nvkrerr.CodeInvalidCatalog {
		t.Fatalf("duplicate resolved component must fail closed, got %v", err)
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
	resolved, buildDigest, artifactDigest := prebuiltResolved(t, evidence)
	reader := EvidenceReaderFunc(func(_ context.Context, ref string, _ int64) ([]byte, error) {
		switch ref {
		case evidence.SBOMRef:
			return sbomBytes, nil
		case evidence.ProvenanceRef:
			return provenanceBytes, nil
		default:
			return nil, errors.New("unknown reference")
		}
	})
	set, err := BuildVerified(context.Background(), resolved, buildDigest,
		map[string]string{"prebuilt": artifactDigest}, reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gaps, err := set.EvidenceGaps(); err != nil || len(gaps) != 0 {
		t.Fatalf("caller-hash path lost independently verified external evidence: %+v, %v", gaps, err)
	}
	if _, err := set.LocalReceipts(); sbomCode(err) != nvkrerr.CodePreflightFailed {
		t.Fatalf("caller artifact hashes minted publisher SBOM authority: %v", err)
	}
}
