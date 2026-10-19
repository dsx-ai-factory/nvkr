// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// This adapter tests authority handoffs only; it makes no cryptographic claim.
type referenceSigner struct {
	fakeOCISigner
	sbomErr          error
	dry              bool
	observedIdentity string
}

func (s *referenceSigner) Verify(ctx context.Context, subject sign.Subject, policy sign.VerifyPolicy) (sign.VerificationRecord, error) {
	s.verifiedPolicy = policy
	if s.verifyErr != nil {
		return sign.VerificationRecord{}, s.verifyErr
	}
	record := sign.VerificationRecord{SchemaVersion: sign.VerificationRecordSchemaVersion, Ref: subject.Ref, Digest: subject.Digest,
		Identity: policy.IdentityRegexp, Issuer: policy.Issuer, ObservedIdentity: "https://github.com/example/nvkr/.github/workflows/nvkr-factory.yml@refs/heads/main", ObservedIssuer: policy.Issuer,
		SignatureVerified: true, AttestationVerified: true, PredicateType: sign.ProvenancePredicateType, Ran: [][]string{{"fixture-verify"}}}
	if s.dry {
		return s.fakeOCISigner.Verify(ctx, subject, policy)
	}
	if s.observedIdentity != "" {
		record.ObservedIdentity = s.observedIdentity
	}
	return record, nil
}
func (s *referenceSigner) AttestSBOM(_ context.Context, subject string, document []byte) ([]byte, error) {
	statement, err := json.Marshal(map[string]any{"_type": "https://in-toto.io/Statement/v1", "predicateType": sbom.CycloneDXPredicateType, "subject": []any{map[string]any{"digest": map[string]string{"sha256": strings.TrimPrefix(subject, "sha256:")}}}, "predicate": json.RawMessage(document)})
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"mediaType": sbom.BundleMediaType, "verificationMaterial": map[string]any{}, "dsseEnvelope": map[string]any{"payloadType": "application/vnd.in-toto+json", "payload": base64.StdEncoding.EncodeToString(statement), "signatures": []any{map[string]string{"sig": "Zml4dHVyZQ=="}}}})
}
func (s *referenceSigner) VerifySBOM(context.Context, string, []byte) error { return s.sbomErr }
func referenceSignerFactory(s *referenceSigner) signerFor {
	return func(context.Context, *Plan, Node, map[string]Artifact) (sign.OCISigner, error) { return s, nil }
}

func referenceMetadataFixture(t *testing.T, attempts ...string) (*Plan, map[string]Artifact, *referenceSigner) {
	t.Helper()
	plan := referenceTestPlan(t)
	return referenceMetadataFixtureForPlan(t, plan, attempts...)
}

func referenceMetadataFixtureForPlan(t *testing.T, plan *Plan, attempts ...string) (*Plan, map[string]Artifact, *referenceSigner) {
	t.Helper()
	t.Chdir(t.TempDir())
	for key, value := range map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_SERVER_URL": "https://github.com", "GITHUB_REPOSITORY": "example/nvkr", "GITHUB_WORKFLOW_REF": "example/nvkr/.github/workflows/nvkr-factory.yml@refs/heads/main", "GITHUB_RUN_ID": "12345", "GITHUB_RUN_ATTEMPT": "1"} {
		t.Setenv(key, value)
	}
	if plan.Environment.GitHubDevelopment {
		setDevelopmentRun(t)
	}
	nodes, artifacts := planIndex(plan)
	for _, a := range artifacts {
		if a.Kind != "producer-result" {
			continue
		}
		for _, payload := range artifacts {
			if strings.HasPrefix(payload.ID, "artifact/") && payload.Component == a.Component && payload.BuildTarget == a.BuildTarget {
				if err := writeFactoryFile(t.Context(), payload.Path, []byte("built "+a.Component), 0755); err != nil {
					t.Fatal(err)
				}
				writeProducerRecordFixture(t, plan, payload, a.Path)
			}
		}
	}
	signer := &referenceSigner{}
	if plan.Environment.GitHubDevelopment {
		signer.observedIdentity = "https://github.com/example/nvkr/.github/workflows/nvkr-dev.yml@refs/heads/feature/pipeline"
	}
	for i, output := range plan.LifecycleOutputs {
		if i < len(attempts) {
			t.Setenv("GITHUB_RUN_ATTEMPT", attempts[i])
		}
		if err := executeSBOM(t.Context(), plan, nodes[output.Name+"/sbom"], artifacts, io.Discard); err != nil {
			t.Fatal(err)
		}
		if err := executePackage(t.Context(), plan, nodes["package/"+output.Name], artifacts, io.Discard); err != nil {
			t.Fatal(err)
		}
		route := outputPublicationRoute(output.Name)
		if err := executePackage(t.Context(), plan, nodes["package/"+outputPublicationPackage(output.Name)], artifacts, io.Discard); err != nil {
			t.Fatal(err)
		}
		if err := executeAttestEvidence(t.Context(), plan, nodes[NodeAttestEvidence+"/"+route], artifacts, referenceSignerFactory(signer), io.Discard); err != nil {
			t.Fatal(err)
		}
		source, err := completePublicationInput(t.Context(), plan, nodes[NodePublish+"/"+route], artifacts, referenceSignerFactory(signer), "")
		if err != nil {
			t.Fatal(err)
		}
		result, err := delivery.ExportMetadata(t.Context(), source, artifacts["metadata/"+route].Path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := delivery.ExportMetadataWithExpectedRoot(t.Context(), source, artifacts["signing-metadata/"+route].Path, result.RootDigest); err != nil {
			t.Fatal(err)
		}
		writeReferenceSigningFixture(t, plan, artifacts, route, result.RootDigest)
	}
	return plan, artifacts, signer
}

func TestReferenceReleaseAuthenticatesEveryOutputBeforeAssembly(t *testing.T) {
	plan, original, _ := referenceMetadataFixture(t)
	for _, tc := range []struct {
		name   string
		change func(*testing.T, *Plan, map[string]Artifact, *referenceSigner)
		code   e.Code
	}{
		{name: "complete"},
		{name: "earlier attempt", change: func(t *testing.T, _ *Plan, _ map[string]Artifact, _ *referenceSigner) {
			t.Setenv("GITHUB_RUN_ATTEMPT", "2")
		}},
		{name: "different run", change: func(t *testing.T, _ *Plan, _ map[string]Artifact, _ *referenceSigner) {
			t.Setenv("GITHUB_RUN_ID", "12346")
		}, code: e.CodeVerifyFailed},
		{name: "different workflow", change: func(t *testing.T, _ *Plan, _ map[string]Artifact, _ *referenceSigner) {
			t.Setenv("GITHUB_WORKFLOW_REF", "example/nvkr/.github/workflows/other.yml@refs/heads/main")
		}, code: e.CodeVerifyFailed},
		{name: "changed plan predicate", change: func(t *testing.T, plan *Plan, artifacts map[string]Artifact, _ *referenceSigner) {
			route := outputPublicationRoute(plan.LifecycleOutputs[0].Name)
			digest, err := plan.IdentityDigest()
			if err != nil {
				t.Fatal(err)
			}
			predicate := artifacts["predicate/"+route]
			raw, err := os.ReadFile(predicate.Path)
			if err != nil {
				t.Fatal(err)
			}
			raw = []byte(strings.ReplaceAll(string(raw), strings.TrimPrefix(digest, "sha256:"), strings.Repeat("f", 64)))
			predicate.Path = filepath.Join(t.TempDir(), "predicate.json")
			artifacts[predicate.ID] = predicate
			if err := os.WriteFile(predicate.Path, raw, 0644); err != nil {
				t.Fatal(err)
			}
			signature := artifacts["signature/"+route]
			record, err := readSigningRecord(t.Context(), signature.Path)
			if err != nil {
				t.Fatal(err)
			}
			record.PredicateSHA256 = pipeline.FileDigest(raw)
			encoded, err := record.Canonical()
			if err != nil {
				t.Fatal(err)
			}
			signature.Path = filepath.Join(t.TempDir(), "signature.json")
			artifacts[signature.ID] = signature
			if err := os.WriteFile(signature.Path, encoded, 0644); err != nil {
				t.Fatal(err)
			}
		}, code: e.CodeVerifyFailed},
		{name: "root signature rejected", change: func(_ *testing.T, _ *Plan, _ map[string]Artifact, s *referenceSigner) {
			s.verifyErr = e.New(e.CodeSignatureInvalid, "rejected")
		}, code: e.CodeSignatureInvalid},
		{name: "SBOM signature rejected", change: func(_ *testing.T, _ *Plan, _ map[string]Artifact, s *referenceSigner) {
			s.sbomErr = e.New(e.CodeSignatureInvalid, "rejected")
		}, code: e.CodeSignatureInvalid},
		{name: "dry run", change: func(_ *testing.T, _ *Plan, _ map[string]Artifact, s *referenceSigner) { s.dry = true }, code: e.CodeSignatureInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Authentication reads the same immutable output roots. Isolate the
			// signer, handoff descriptions and assembly destination for each row.
			artifacts := maps.Clone(original)
			signer := &referenceSigner{}
			node := planNode(plan, "package/release")
			output := artifacts[node.Outputs[0]]
			output.Path = filepath.Join(t.TempDir(), "release")
			artifacts[output.ID] = output
			if tc.change != nil {
				tc.change(t, plan, artifacts, signer)
			}
			err := executeReferenceRelease(t.Context(), plan, node, artifacts, referenceSignerFactory(signer))
			if tc.code != "" {
				assertFactoryErrorCode(t, err, tc.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(signer.verifiedPolicy.ExpectedPredicate) == 0 {
				t.Fatal("no exact provenance binding")
			}
			result, err := delivery.InspectMetadata(t.Context(), artifacts[node.Outputs[0]].Path)
			if err != nil {
				t.Fatal(err)
			}
			outputs, err := delivery.MetadataOutputs(result.Root)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateLifecycleOutputs(plan.LifecycleOutputs, outputs); err != nil {
				t.Fatal(err)
			}
			for _, object := range result.Root.Objects {
				if object.Output != nil {
					origin := object.Output.Publication
					if origin == nil || origin.PreparedInvocationID != "https://github.com/example/nvkr/actions/runs/12345/attempts/1" || origin.SignedInvocationID != origin.PreparedInvocationID {
						t.Fatal("output lost original invocation")
					}
				}
				if object.Evidence != nil && len(object.Evidence.Outputs) != 2 {
					t.Fatal("shared evidence lost an output")
				}
			}
			if result.Root.Release.InvocationID != "https://github.com/example/nvkr/actions/runs/12345/attempts/"+os.Getenv("GITHUB_RUN_ATTEMPT") {
				t.Fatal("assembly retained an earlier invocation")
			}
			if _, err := delivery.Inspect(t.Context(), artifacts[node.Outputs[0]].Path); err == nil {
				t.Fatal("assembly copied all payloads")
			}
			if err := assembleReferenceRelease(t.Context(), plan, filepath.Join(t.TempDir(), "empty"), nil); err == nil {
				t.Fatal("empty closure accepted")
			}
		})
	}
}

func writeReferenceSigningFixture(t *testing.T, plan *Plan, artifacts map[string]Artifact, route, digest string) {
	t.Helper()
	target, err := runtimeTarget(plan, route)
	if err != nil {
		t.Fatal(err)
	}
	ref := target.Ref + "@" + digest
	writeReferenceFile(t, artifacts["publication/"+route].Path, ref)
	predicate, err := productionPredicateFor(plan, plan.Environment.Signing, time.Now().UTC(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	predicate = append(predicate, '\n')
	record := sign.SigningRecord{SchemaVersion: sign.SigningRecordSchemaVersion, Ref: ref, Digest: digest, PredicateSHA256: pipeline.FileDigest(predicate), PredicateType: sign.ProvenancePredicateType, Identity: plan.Environment.Signing.Identity, Issuer: plan.Environment.Signing.Issuer, Ran: [][]string{{"fixture-sign"}}}
	raw, err := record.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFactoryFile(t.Context(), artifacts["signature/"+route].Path, raw, 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeFactoryFile(t.Context(), artifacts["predicate/"+route].Path, predicate, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestReferenceReleaseMixedAttemptsAndResigning(t *testing.T) {
	const invocation = "https://github.com/example/nvkr/actions/runs/12345/attempts/"
	plan, artifacts, signer := referenceMetadataFixture(t, "1", "2")
	first := outputPublicationRoute(plan.LifecycleOutputs[0].Name)
	before, err := delivery.InspectMetadata(t.Context(), artifacts["metadata/"+first].Path)
	if err != nil {
		t.Fatal(err)
	}
	// Re-signing an existing package is also a legitimate partial retry.
	t.Setenv("GITHUB_RUN_ATTEMPT", "3")
	writeReferenceSigningFixture(t, plan, artifacts, first, before.RootDigest)
	if err := executeReferenceRelease(t.Context(), plan, planNode(plan, "package/release"), artifacts, referenceSignerFactory(signer)); err != nil {
		t.Fatal(err)
	}
	source := artifacts["package/release"].Path
	result, err := delivery.InspectMetadata(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	if result.Root.Release.InvocationID != invocation+"3" {
		t.Fatal("aggregate does not identify its assembly attempt")
	}
	for i, output := range plan.LifecycleOutputs {
		found := false
		for _, object := range result.Root.Objects {
			if object.Name != output.Name || object.Output == nil {
				continue
			}
			found = true
			prepared, signed := "1", "3"
			if i == 1 {
				prepared, signed = "2", "2"
			}
			origin := object.Output.Publication
			subject, err := readPublicationSubject(t.Context(), artifacts["publication/"+outputPublicationRoute(output.Name)].Path)
			if err != nil {
				t.Fatal(err)
			}
			if origin == nil || origin.PreparedInvocationID != invocation+prepared || origin.SignedInvocationID != invocation+signed || origin.Ref != subject.Ref {
				t.Fatalf("lost original output binding: %+v", origin)
			}
		}
		if !found {
			t.Fatal("missing output")
		}
	}
	// A finalizer retry authenticates the unchanged aggregate from attempt 3.
	sources := map[string]string{}
	for _, object := range result.Root.Objects {
		sources[object.Name] = source
	}
	if _, err := delivery.BuildMetadataRoot(t.Context(), artifacts["metadata/release"].Path, result.Root, sources); err != nil {
		t.Fatal(err)
	}
	writeReferenceSigningFixture(t, plan, artifacts, "release", result.RootDigest)
	t.Setenv("GITHUB_RUN_ATTEMPT", "4")
	if _, err := authenticateReferenceMetadata(t.Context(), plan, Route{Name: "release", Target: "release"}, artifacts, referenceSignerFactory(signer)); err != nil {
		t.Fatal(err)
	}
	// Earlier consumers cannot accept future evidence, even when the Plan matches.
	t.Setenv("GITHUB_RUN_ATTEMPT", "2")
	_, err = authenticateReferenceMetadata(t.Context(), plan, Route{Name: "release", Target: "release"}, artifacts, referenceSignerFactory(signer))
	assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
	// Signing cannot precede preparation, even within the same run.
	t.Setenv("GITHUB_RUN_ATTEMPT", "1")
	second := outputPublicationRoute(plan.LifecycleOutputs[1].Name)
	prior, err := delivery.InspectMetadata(t.Context(), artifacts["metadata/"+second].Path)
	if err != nil {
		t.Fatal(err)
	}
	writeReferenceSigningFixture(t, plan, artifacts, second, prior.RootDigest)
	t.Setenv("GITHUB_RUN_ATTEMPT", "4")
	_, err = authenticateReferenceMetadata(t.Context(), plan, Route{Name: second, Target: second}, artifacts, referenceSignerFactory(signer))
	assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
}
