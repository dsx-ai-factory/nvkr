// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

func checkLocalBuildEvidenceFixture(t *testing.T, workspace string, inputs *localQualificationInputs, capture *localQualificationCapture) {
	t.Helper()
	policy := localPolicyFixture(t)
	policy.PlanDigest, policy.DefinitionDigest, policy.EnvironmentDigest = inputs.run.PlanDigest, inputs.run.DefinitionDigest, inputs.run.EnvironmentDigest
	policy.Platform = inputs.plan.Builds[0].Platform
	policy.Smoke.Images = map[string]string{policy.Platform: "registry.example.test/smoke@" + nativeSmokeTestDigest("1")}
	policy.Smoke.Tools = map[string]string{}
	for _, output := range inputs.plan.LifecycleOutputs {
		if output.Kind != OutputExecutable {
			continue
		}
		for _, component := range output.Components {
			policy.Smoke.Tools[component] = "helm-version"
		}
	}
	if path := os.Getenv("NVKR_TEST_LOCAL_SMOKE_PUB"); path != "" {
		public, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		policy.SmokeKey = string(public)
		builderPublic, err := os.ReadFile(os.Getenv("NVKR_TEST_LOCAL_BUILDER_PUB"))
		if err != nil {
			t.Fatal(err)
		}
		policy.BuilderKey = string(builderPublic)
		if qualifierPath := os.Getenv("NVKR_TEST_LOCAL_QUALIFIER_PUB"); qualifierPath != "" {
			qualifierPublic, err := os.ReadFile(qualifierPath)
			if err != nil {
				t.Fatal(err)
			}
			policy.QualifierKey = string(qualifierPublic)
		}
		policy.CosignDigest = os.Getenv("NVKR_TEST_COSIGN_SHA256")
	}
	checkLocalBuilderSigningFixture(t, workspace, inputs, capture, policy)
	raw, err := prepareLocalBuildEvidence(t.Context(), inputs, capture, policy)
	if err != nil {
		t.Fatal(err)
	}
	checkLocalBuildDocumentAdmission(t, raw, policy)
	var doc localBuildEvidence
	if err := canonical.UnmarshalExact(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Origin != "retained-local-run" || doc.Lane != factory.TierLocalExecution.String() || len(doc.Outputs) != len(inputs.run.Observed) || doc.OrdinaryRoot != capture.ordinaryRoot || len(doc.Inputs) != 3+len(capture.disks.members) {
		t.Fatalf("lost local evidence bindings: %+v", doc)
	}
	if doc.Inputs[0].Digest != pipeline.FileDigest(inputs.inputBytes) || doc.Inputs[0].Size != int64(len(inputs.inputBytes)) || doc.Outputs[0].Artifact.Digest != inputs.run.Observed[0].Digest {
		t.Fatal("evidence changed exact input or payload bytes")
	}
	for _, output := range doc.Outputs {
		if output.Artifact.Kind == OutputDiskImage {
			view := capture.disks.members[output.Artifact.Name].Inspection()
			if output.TransportDigest != view.ManifestDigest || output.Artifact.Digest != view.PayloadDigest || output.TransportDigest == output.Artifact.Digest {
				t.Fatal("disk transport and payload identities conflated")
			}
			if doc.Inputs[1].Name != "disk-materials.json" || doc.Inputs[1].Digest != pipeline.FileDigest([]byte(capture.materialBytes)) {
				t.Fatal("disk material bytes not bound")
			}
		}
	}
	again, err := prepareLocalBuildEvidence(t.Context(), inputs, capture, policy)
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatalf("nondeterministic evidence: %v", err)
	}
	for _, mode := range []string{"changed Run", "changed Plan bytes", "missing compiler reads", "missing ordinary owner", "wrong policy", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			changedInputs, changedCapture, changedPolicy := *inputs, *capture, policy
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			code := e.CodeVerifyFailed
			switch mode {
			case "changed Run":
				changedInputs.run.Attempt = "different"
			case "changed Plan bytes":
				changedInputs.planBytes = []byte("{}")
			case "missing compiler reads":
				changedInputs.inputBytes = nil
				code = e.CodeProvenanceMissing
			case "missing ordinary owner":
				changedCapture.ordinary = nil
			case "wrong policy":
				changedPolicy.PlanDigest = nativeSmokeTestDigest("9")
				code = e.CodeTrustPolicyMissing
			case "canceled":
				cancel()
				code = e.CodeTimeout
			}
			got, err := prepareLocalBuildEvidence(ctx, &changedInputs, &changedCapture, changedPolicy)
			assertCode(t, err, code)
			if len(got) != 0 {
				t.Fatal("refusal returned partial signable bytes")
			}
		})
	}
	if len(capture.disks.members) > 0 {
		for _, raw := range []string{"", "{}", capture.materialBytes + "\n"} {
			changedCapture := *capture
			changedCapture.materialBytes = raw
			got, err := prepareLocalBuildEvidence(t.Context(), inputs, &changedCapture, policy)
			assertCode(t, err, e.CodeVerifyFailed)
			if len(got) != 0 {
				t.Fatal("changed material records returned signable evidence")
			}
		}
	}
	changedPolicy := policy
	changedPolicy.BuilderKey, changedPolicy.QualifierKey = policy.QualifierKey, policy.BuilderKey
	changed, err := prepareLocalBuildEvidence(t.Context(), inputs, capture, changedPolicy)
	if err != nil || bytes.Equal(raw, changed) {
		t.Fatalf("evidence failed to bind signer role policy: %v", err)
	}
	// Closing a copied owner revokes the original capture too. No stale metadata
	// may be prepared for signing after its held payload capability is gone.
	smoke := checkLocalSmokeFixture(t, workspace, inputs, capture, policy, raw)
	copyCapture := *capture
	if err := copyCapture.Close(); err != nil {
		t.Fatal(err)
	}
	_, smokeErr := smoke.description(t.Context())
	assertCode(t, smokeErr, e.CodeGateUnmet)
	got, err := prepareLocalBuildEvidence(t.Context(), inputs, capture, policy)
	assertCode(t, err, e.CodeGateUnmet)
	if len(got) != 0 {
		t.Fatal("closed capture returned signable evidence")
	}
}

func checkLocalBuilderSigningFixture(t *testing.T, workspace string, inputs *localQualificationInputs, capture *localQualificationCapture, policy LocalQualificationPolicy) {
	t.Helper()
	calls := 0
	signer := func(_ context.Context, document, _, publicKey []byte) (sign.LocalSignedDocument, error) {
		calls++
		if len(document) == 0 || string(publicKey) != policy.BuilderKey {
			t.Fatal("builder signer received wrong document or role")
		}
		return sign.LocalSignedDocument{}, nil
	}
	got, err := signLocalBuildEvidenceWith(t.Context(), inputs, capture, policy, nil, signer)
	assertCode(t, err, e.CodeSignatureInvalid)
	if got != nil || calls != 1 {
		t.Fatal("unissued signature receipt accepted")
	}
	broken := *inputs
	broken.planBytes = nil
	got, err = signLocalBuildEvidenceWith(t.Context(), &broken, capture, policy, nil, signer)
	assertCode(t, err, e.CodeVerifyFailed)
	if got != nil || calls != 1 {
		t.Fatal("failed evidence reached signing")
	}
	binary, pin := os.Getenv("NVKR_TEST_COSIGN_BINARY"), os.Getenv("NVKR_TEST_COSIGN_SHA256")
	keyPath, pubPath := os.Getenv("NVKR_TEST_LOCAL_BUILDER_KEY"), os.Getenv("NVKR_TEST_LOCAL_BUILDER_PUB")
	if binary == "" || pin == "" || keyPath == "" || pubPath == "" {
		return
	}
	private, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	public, err := os.ReadFile(pubPath)
	if err != nil {
		t.Fatal(err)
	}
	policy.BuilderKey, policy.CosignDigest = string(public), pin
	signed, err := signLocalBuildEvidence(t.Context(), inputs, capture, policy, binary, private)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := policy.freeze()
	if err != nil {
		t.Fatal(err)
	}
	if err := signed.signed.RequireFor(t.Context(), []byte(signed.document), []byte(frozen.BuilderKey)); err != nil {
		t.Fatal(err)
	}
	signature, err := signed.signed.Signature()
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := sign.NewLocalCosign(binary, pin)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, document, key string
		pass                bool
	}{
		{"exact", signed.document, frozen.BuilderKey, true},
		{"changed document", signed.document + "\n", frozen.BuilderKey, false},
		{"qualifier role", signed.document, frozen.QualifierKey, false},
	} {
		t.Run("real builder/"+tc.name, func(t *testing.T) {
			err := verifier.VerifyDocument(t.Context(), []byte(tc.document), signature, []byte(tc.key))
			if tc.pass {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertCode(t, err, e.CodeSignatureInvalid)
			}
		})
	}
	checkLocalBuildConsumerFixture(t, workspace, inputs, policy, binary, []byte(signed.document), signature)
	wrong := policy
	wrong.BuilderKey = policy.SmokeKey
	wrong.SmokeKey = policy.BuilderKey
	refused, err := signLocalBuildEvidence(t.Context(), inputs, capture, wrong, binary, private)
	assertCode(t, err, e.CodeSignatureInvalid)
	if refused != nil {
		t.Fatal("mismatched private key returned builder receipt")
	}
}
