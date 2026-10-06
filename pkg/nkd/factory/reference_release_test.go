package factory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sign"
)

// This adapter tests authority handoffs only; it makes no cryptographic claim.
type referenceSigner struct {
	fakeOCISigner
	sbomErr error
	dry     bool
}

func (s *referenceSigner) Verify(ctx context.Context, subject sign.Subject, policy sign.VerifyPolicy) (sign.VerificationRecord, error) {
	s.verifiedPolicy = policy
	if s.verifyErr != nil {
		return sign.VerificationRecord{}, s.verifyErr
	}
	record := sign.VerificationRecord{SchemaVersion: sign.VerificationRecordSchemaVersion, Ref: subject.Ref, Digest: subject.Digest,
		Identity: policy.IdentityRegexp, Issuer: policy.Issuer, ObservedIdentity: "https://github.com/example/nkd/.github/workflows/nkd-factory.yml@refs/heads/main", ObservedIssuer: policy.Issuer,
		SignatureVerified: true, AttestationVerified: true, PredicateType: sign.ProvenancePredicateType, Ran: [][]string{{"fixture-verify"}}}
	if s.dry {
		return s.fakeOCISigner.Verify(ctx, subject, policy)
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

func referenceMetadataFixture(t *testing.T) (*Plan, map[string]Artifact, *referenceSigner) {
	t.Helper()
	plan := referenceTestPlan(t)
	t.Chdir(t.TempDir())
	for key, value := range map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_SERVER_URL": "https://github.com", "GITHUB_REPOSITORY": "example/nkd", "GITHUB_WORKFLOW_REF": "example/nkd/.github/workflows/nkd-factory.yml@refs/heads/main", "GITHUB_RUN_ID": "12345", "GITHUB_RUN_ATTEMPT": "1"} {
		t.Setenv(key, value)
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
	for _, output := range plan.LifecycleOutputs {
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
		source := artifacts["package/"+outputPublicationPackage(output.Name)].Path
		result, err := delivery.ExportMetadata(t.Context(), source, artifacts["metadata/"+route].Path)
		if err != nil {
			t.Fatal(err)
		}
		ref := plan.Targets[route].Ref + "@" + result.RootDigest
		writeReferenceFile(t, artifacts["publication/"+route].Path, ref)
		predicate, err := productionPredicateFor(plan, plan.Environment.Signing, time.Now().UTC(), time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		predicate = append(predicate, '\n')
		record := sign.SigningRecord{SchemaVersion: sign.SigningRecordSchemaVersion, Ref: ref, Digest: result.RootDigest, PredicateSHA256: pipeline.FileDigest(predicate), PredicateType: sign.ProvenancePredicateType, Identity: plan.Environment.Signing.Identity, Issuer: plan.Environment.Signing.Issuer, Ran: [][]string{{"fixture-sign"}}}
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
	return plan, artifacts, signer
}

func TestReferenceReleaseAuthenticatesEveryOutputBeforeAssembly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*testing.T, *Plan, map[string]Artifact, *referenceSigner)
		code   e.Code
	}{
		{name: "complete"},
		{name: "different attempt", change: func(t *testing.T, _ *Plan, _ map[string]Artifact, _ *referenceSigner) {
			t.Setenv("GITHUB_RUN_ATTEMPT", "2")
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
			plan, artifacts, signer := referenceMetadataFixture(t)
			if tc.change != nil {
				tc.change(t, plan, artifacts, signer)
			}
			node := planNode(plan, "package/release")
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
				if object.Evidence != nil && len(object.Evidence.Outputs) != 2 {
					t.Fatal("shared evidence lost an output")
				}
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
