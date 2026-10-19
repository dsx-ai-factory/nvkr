// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

// These synthetic bundles test the handoff contract. The injected verifier is
// not cryptographic evidence or a substitute for the production signer policy.
func TestMetadataAttestationCompletesWithoutPayloadCopies(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			request, _ := diskPublicationRequest(t, arch)
			parent := t.TempDir()
			source := filepath.Join(parent, "prepared")
			prepared, err := BuildPublicationRoot(t.Context(), source, request)
			if err != nil {
				t.Fatal(err)
			}
			payload := request.Objects[0].Output.Disk.PayloadDigest
			before, err := os.Stat(fixtureBlobPath(source, payload))
			if err != nil {
				t.Fatal(err)
			}
			controls := filepath.Join(parent, "controls")
			exported, err := ExportMetadataWithExpectedRoot(t.Context(), source, controls, prepared.RootDigest)
			if err != nil {
				t.Fatal(err)
			}
			attested := filepath.Join(parent, "attested")
			signed, err := AttestPreparedMetadata(t.Context(), controls, attested, exported.RootDigest, fixtureMetadataAttester)
			if err != nil {
				t.Fatal(err)
			}
			for _, metadata := range []string{controls, attested} {
				if _, err := os.Lstat(fixtureBlobPath(metadata, payload)); !os.IsNotExist(err) {
					t.Fatalf("metadata transported disk payload: %v", err)
				}
				if _, err := InspectMetadata(t.Context(), metadata); err != nil {
					t.Fatal(err)
				}
				if _, err := Inspect(t.Context(), metadata); err == nil {
					t.Fatal("metadata produced full payload inspection")
				}
			}
			out := filepath.Join(parent, "final")
			verified := 0
			completed, err := CompleteAttestedPreparation(t.Context(), source, attested, out, prepared.RootDigest, signed.RootDigest, func(_ context.Context, subject string, bundle []byte) error {
				if _, err := os.Stat(source); err != nil {
					t.Fatal("source consumed before bundle verification", err)
				}
				verified++
				_, err := sbom.AttestedDocument(bundle, subject)
				return err
			})
			if err != nil || completed.RootDigest != signed.RootDigest || verified != len(request.Objects)-1 {
				t.Fatalf("completion: verified=%d result=%+v err=%v", verified, completed, err)
			}
			after, err := os.Stat(fixtureBlobPath(out, payload))
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("completion copied disk payload: %v", err)
			}
			if _, err := os.Lstat(source); !os.IsNotExist(err) {
				t.Fatalf("completion retained full preparation: %v", err)
			}
			inspection, err := Inspect(t.Context(), out)
			if err != nil || inspection.RootDigest != signed.RootDigest {
				t.Fatalf("final full closure: %v", err)
			}
			if err := RequireAttestedEvidence(inspection); err != nil {
				t.Fatal(err)
			}
			assertNoAttestationStage(t, parent)
		})
	}
}

func TestMetadataAttestationRejectsAdmissionBeforeSigning(t *testing.T) {
	for _, scenario := range []string{"changed root", "payload present", "missing adapter", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			source, root := releasePublicationFixture(t)
			parent := t.TempDir()
			controls, out := filepath.Join(parent, "controls"), filepath.Join(parent, "attested")
			if _, err := ExportMetadataWithExpectedRoot(t.Context(), source, controls, root); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			called := false
			attest := func(ctx context.Context, subject string, document []byte) ([]byte, error) {
				called = true
				return fixtureMetadataAttester(ctx, subject, document)
			}
			code := e.CodeVerifyFailed
			switch scenario {
			case "changed root":
				root = digestBytes([]byte("different preparation"))
			case "payload present":
				controls = source
			case "missing adapter":
				attest, code = nil, e.CodeEngineCapability
			case "canceled":
				cancel()
				code = e.CodeTimeout
			}
			_, err := AttestPreparedMetadata(ctx, controls, out, root, attest)
			assertRegistryCode(t, err, code)
			if called {
				t.Fatal("rejected metadata reached signer")
			}
			assertMetadataCompletionUnclaimed(t, source, out, parent)
		})
	}
}

func TestCompleteAttestedPreparationRejectsBeforeConsumption(t *testing.T) {
	for _, scenario := range []string{"changed preparation root", "changed attested root", "changed release", "verification failure", "metadata mutation", "canceled", "missing verifier"} {
		t.Run(scenario, func(t *testing.T) {
			source, preparedRoot := releasePublicationFixture(t)
			parent := t.TempDir()
			controls, attested, out := filepath.Join(parent, "controls"), filepath.Join(parent, "attested"), filepath.Join(parent, "final")
			if _, err := ExportMetadataWithExpectedRoot(t.Context(), source, controls, preparedRoot); err != nil {
				t.Fatal(err)
			}
			signed, err := AttestPreparedMetadata(t.Context(), controls, attested, preparedRoot, fixtureMetadataAttester)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			code := e.CodeVerifyFailed
			called := false
			verify := func(_ context.Context, _ string, _ []byte) error {
				called = true
				switch scenario {
				case "verification failure":
					return e.New(e.CodeSignatureInvalid, "fixture rejected signer identity")
				case "metadata mutation":
					if err := os.WriteFile(filepath.Join(attested, "index.json"), []byte("changed"), 0o600); err != nil {
						t.Fatal(err)
					}
				case "canceled":
					cancel()
				}
				return nil
			}
			expectVerifier := false
			switch scenario {
			case "changed preparation root":
				preparedRoot = digestBytes([]byte("another preparation"))
			case "changed attested root":
				signed.RootDigest = digestBytes([]byte("another attestation"))
			case "changed release":
				inspected, err := InspectMetadata(t.Context(), attested)
				if err != nil {
					t.Fatal(err)
				}
				inspected.Root.Release.DefinitionDigest = digestBytes([]byte("another definition"))
				sources := map[string]string{}
				for _, object := range inspected.Root.Objects {
					sources[object.Name] = attested
				}
				changed := filepath.Join(parent, "substituted")
				signed, err = BuildMetadataRoot(t.Context(), changed, inspected.Root, sources)
				if err != nil {
					t.Fatal(err)
				}
				attested = changed
			case "verification failure":
				code, expectVerifier = e.CodeSignatureInvalid, true
			case "metadata mutation":
				expectVerifier = true
			case "canceled":
				code, expectVerifier = e.CodeTimeout, true
			case "missing verifier":
				verify, code = nil, e.CodeEngineCapability
			}
			result, err := CompleteAttestedPreparation(ctx, source, attested, out, preparedRoot, signed.RootDigest, verify)
			assertRegistryCode(t, err, code)
			if result != (Result{}) || called != expectVerifier {
				t.Fatalf("failed completion result=%+v verifier=%t", result, called)
			}
			assertMetadataCompletionUnclaimed(t, source, out, parent)
		})
	}
}

func TestAttestationBindingsPreserveEveryRootField(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*PublicationRoot)
	}{
		{"definition", func(r *PublicationRoot) { r.Release.DefinitionDigest = digestBytes([]byte("other")) }},
		{"environment", func(r *PublicationRoot) { r.Release.EnvironmentDigest = digestBytes([]byte("other")) }},
		{"plan", func(r *PublicationRoot) { r.Release.PlanDigest = digestBytes([]byte("other")) }},
		{"repository", func(r *PublicationRoot) { r.Objects[0].Output.Repository = "another-output" }},
		{"payload digest", func(r *PublicationRoot) { r.Objects[0].Output.Disk.PayloadDigest = digestBytes([]byte("other")) }},
		{"payload size", func(r *PublicationRoot) { r.Objects[0].Output.Disk.Size++ }},
		{"build subjects", func(r *PublicationRoot) { r.Objects[0].Output.BuildSubjects[0].Digest = digestBytes([]byte("other")) }},
		{"output descriptor", func(r *PublicationRoot) { r.Objects[0].DescriptorDigest = digestBytes([]byte("other")) }},
		{"evidence subject", func(r *PublicationRoot) { r.Objects[1].Evidence.SubjectDigest = digestBytes([]byte("other")) }},
		{"evidence membership", func(r *PublicationRoot) { r.Objects[1].Evidence.Outputs = []string{"other"} }},
		{"removed evidence", func(r *PublicationRoot) { r.Objects = r.Objects[:len(r.Objects)-1] }},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, _ := diskPublicationRequest(t, "amd64")
			source := filepath.Join(t.TempDir(), "prepared")
			if _, err := BuildPublicationRoot(t.Context(), source, request); err != nil {
				t.Fatal(err)
			}
			inspection, err := Inspect(t.Context(), source)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := canonical.Marshal(inspection.Root)
			if err != nil {
				t.Fatal(err)
			}
			var attested PublicationRoot
			if err := canonical.UnmarshalExact(raw, &attested); err != nil {
				t.Fatal(err)
			}
			for i := range attested.Objects {
				if attested.Objects[i].Evidence != nil {
					attested.Objects[i].ArtifactType = sbom.BundleMediaType
				}
			}
			if err := unchangedAttestationBindings(inspection.Root, attested); err != nil {
				t.Fatal("permitted bundle fields rejected", err)
			}
			test.change(&attested)
			assertRegistryCode(t, unchangedAttestationBindings(inspection.Root, attested), e.CodeVerifyFailed)
		})
	}
}

func fixtureMetadataAttester(_ context.Context, subject string, document []byte) ([]byte, error) {
	return imageIndexFixtureAttestation(subject, document)
}

func assertMetadataCompletionUnclaimed(t *testing.T, source, out, parent string) {
	t.Helper()
	if _, err := os.Stat(source); err != nil {
		t.Fatal("failure consumed original preparation", err)
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Fatalf("failure exposed completed layout: %v", err)
	}
	assertNoAttestationStage(t, parent)
}
