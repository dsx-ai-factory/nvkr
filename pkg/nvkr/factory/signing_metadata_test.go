// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

func TestOutputSignerConsumesDistinctPublisherMetadata(t *testing.T) {
	plan := referenceTestPlan(t)
	nodes, artifacts := planIndex(plan)
	for _, output := range plan.LifecycleOutputs {
		route := outputPublicationRoute(output.Name)
		metadata := artifacts["signing-metadata/"+route]
		publisher, signer := nodes[NodePublish+"/"+route], nodes[NodeSign+"/"+route]
		if metadata.Kind != ArtifactSigningMetadata || metadata.Producer != publisher.ID || !containsString(publisher.Outputs, metadata.ID) || !containsString(signer.Inputs, metadata.ID) {
			t.Fatal("signing controls do not originate at the publisher")
		}
		if artifacts["metadata/"+route].Producer != NodeVerifyPublication+"/"+route || containsString(signer.Inputs, "metadata/"+route) {
			t.Fatal("pre-sign controls reused authenticated verifier output")
		}
		for _, id := range signer.Inputs {
			if artifacts[id].Kind == PackageOCI || strings.HasPrefix(id, "package/") {
				t.Fatal("output signer still downloads a payload")
			}
		}
	}
}

type metadataSigningAdapter struct {
	referenceSigner
	verifiedSBOMs int
	signed        []sign.Subject
}

func (s *metadataSigningAdapter) VerifySBOM(ctx context.Context, subject string, bundle []byte) error {
	s.verifiedSBOMs++
	return s.referenceSigner.VerifySBOM(ctx, subject, bundle)
}

func (s *metadataSigningAdapter) SignAndAttest(ctx context.Context, subject sign.Subject, predicate []byte) (sign.SigningRecord, error) {
	s.signed = append(s.signed, subject)
	record, err := s.fakeOCISigner.SignAndAttest(ctx, subject, predicate)
	record.DryRun, record.WouldRun, record.Ran = false, nil, [][]string{{"fixture-sign"}}
	return record, err
}

func TestOutputSigningRequiresAttestedMetadataWithoutPayloadAccess(t *testing.T) {
	plan, original, _ := referenceMetadataFixture(t)
	route := outputPublicationRoute(plan.LifecycleOutputs[0].Name)
	node := planNode(plan, NodeSign+"/"+route)
	// The signer must remain executable after every complete layout has left its
	// workspace; only controls, references and signing tools are transported.
	if err := os.Rename(filepath.Join(".nvkr", "factory", "packages"), filepath.Join(t.TempDir(), "unavailable-payloads")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		fail bool
	}{
		{"verified bundles", false},
		{"invalid bundle before signing", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			artifacts := maps.Clone(original)
			for _, id := range node.Outputs {
				artifact := artifacts[id]
				artifact.Path = filepath.Join(t.TempDir(), "result.json")
				artifacts[id] = artifact
			}
			adapter := &metadataSigningAdapter{}
			adapter.identity, adapter.issuer = plan.Environment.Signing.Identity, plan.Environment.Signing.Issuer
			if tc.fail {
				adapter.sbomErr = e.New(e.CodeSignatureInvalid, "fixture rejected bundle")
			}
			newSigner := func(context.Context, *Plan, Node, map[string]Artifact) (sign.OCISigner, error) { return adapter, nil }
			err := executeSign(t.Context(), plan, node, artifacts, newSigner, io.Discard)
			if tc.fail {
				assertFactoryErrorCode(t, err, e.CodeSignatureInvalid)
				if len(adapter.signed) != 0 {
					t.Fatal("invalid SBOM reached signing")
				}
				for _, id := range node.Outputs {
					if _, err := os.Lstat(artifacts[id].Path); !os.IsNotExist(err) {
						t.Fatalf("failed signing persisted output: %v", err)
					}
				}
				return
			}
			if err != nil || adapter.verifiedSBOMs == 0 {
				t.Fatalf("metadata-only signing = %v; verified SBOMs = %d", err, adapter.verifiedSBOMs)
			}
			subject, err := readPublicationSubject(t.Context(), artifacts["publication/"+route].Path)
			if err != nil {
				t.Fatal(err)
			}
			inspection, err := delivery.InspectMetadata(t.Context(), artifacts["signing-metadata/"+route].Path)
			if err != nil {
				t.Fatal(err)
			}
			outputs, err := publicationOutputSubjects(subject.Ref, inspection)
			if err != nil || len(adapter.signed) != len(outputs)+1 || adapter.signed[len(outputs)] != subject {
				t.Fatalf("signing subjects = %v, outputs = %v, %v", adapter.signed, outputs, err)
			}
			for i := range outputs {
				if adapter.signed[i] != outputs[i] {
					t.Fatal("signer changed an independently consumable subject")
				}
			}
		})
	}
}

func TestSigningMetadataRejectsForeignScope(t *testing.T) {
	plan, artifacts, _ := referenceMetadataFixture(t)
	route := outputPublicationRoute(plan.LifecycleOutputs[0].Name)
	node := planNode(plan, NodeSign+"/"+route)
	for _, tc := range []struct {
		name   string
		change func(*sign.Subject, *delivery.Inspection)
	}{
		{"repository", func(s *sign.Subject, _ *delivery.Inspection) { s.Ref = "ghcr.io/example/other@" + s.Digest }},
		{"root", func(_ *sign.Subject, i *delivery.Inspection) { i.RootDigest = "sha256:" + strings.Repeat("a", 64) }},
		{"plan", func(_ *sign.Subject, i *delivery.Inspection) {
			i.Root.Release.PlanDigest = "sha256:" + strings.Repeat("a", 64)
		}},
		{"definition", func(_ *sign.Subject, i *delivery.Inspection) {
			i.Root.Release.DefinitionDigest = "sha256:" + strings.Repeat("a", 64)
		}},
		{"environment", func(_ *sign.Subject, i *delivery.Inspection) {
			i.Root.Release.EnvironmentDigest = "sha256:" + strings.Repeat("a", 64)
		}},
		{"run", func(_ *sign.Subject, i *delivery.Inspection) {
			i.Root.Release.InvocationID = "https://github.com/example/nvkr/actions/runs/99999/attempts/1"
		}},
		{"future attempt", func(_ *sign.Subject, i *delivery.Inspection) {
			i.Root.Release.InvocationID = "https://github.com/example/nvkr/actions/runs/12345/attempts/2"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inspection, err := delivery.InspectMetadata(t.Context(), artifacts["signing-metadata/"+route].Path)
			if err != nil {
				t.Fatal(err)
			}
			subject, err := readPublicationSubject(t.Context(), artifacts["publication/"+route].Path)
			if err != nil {
				t.Fatal(err)
			}
			tc.change(&subject, &inspection)
			assertFactoryErrorCode(t, admitSigningMetadata(plan, node, subject, inspection), e.CodeVerifyFailed)
		})
	}
}
