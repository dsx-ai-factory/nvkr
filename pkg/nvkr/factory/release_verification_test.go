// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"log"
	"maps"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"github.com/google/go-containerregistry/pkg/registry"
)

func releaseFileBytes(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		files[rel] = string(raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestReferenceReleaseParallelPreservesExactBytes(t *testing.T) {
	plan, artifacts, signer := referenceMetadataFixtureWithSigner(t, referenceTestPlan(t), &referenceSigner{distinctBundles: true})
	before, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce the original sequential loop on the SAME authenticated inputs.
	var receipts []authenticatedOutputMetadata
	for _, output := range plan.LifecycleOutputs {
		route := outputPublicationRoute(output.Name)
		receipt, err := authenticateReferenceMetadata(t.Context(), plan, Route{Name: route, Target: route}, artifacts, referenceSignerFactory(signer))
		if err != nil {
			t.Fatal(err)
		}
		observed, err := delivery.MetadataOutputs(receipt.inspection.Root)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateLifecycleOutputs([]LifecycleOutputPlan{output}, observed); err != nil {
			t.Fatal(err)
		}
		receipts = append(receipts, receipt)
	}
	distinctSharedBundle := false
	for _, first := range receipts[0].inspection.Root.Objects {
		for _, second := range receipts[1].inspection.Root.Objects {
			if first.Evidence != nil && second.Evidence != nil && first.Name == second.Name && first.BlobDigest != second.BlobDigest {
				distinctSharedBundle = true
			}
		}
	}
	if !distinctSharedBundle {
		t.Fatal("fixture must distinguish each owner's signed copy of shared evidence")
	}
	baseline := filepath.Join(t.TempDir(), "baseline")
	if err := assembleReferenceRelease(t.Context(), plan, baseline, receipts); err != nil {
		t.Fatal(err)
	}
	want := releaseFileBytes(t, baseline)
	for _, workers := range []int{1, 4} {
		t.Run(strconv.Itoa(workers), func(t *testing.T) {
			t.Setenv("NVKR_VERIFY_WORKERS", strconv.Itoa(workers))
			copyArtifacts := maps.Clone(artifacts)
			out := copyArtifacts["package/release"]
			out.Path = filepath.Join(t.TempDir(), "release")
			copyArtifacts[out.ID] = out
			factory := referenceSignerFactory(signer)
			if workers > 1 {
				// The first canonical owner cannot verify until the second has
				// finished its SBOM checks. Shared evidence must still select the
				// first owner's original bundle, not the first worker to finish.
				secondDone := make(chan struct{})
				factory = func(_ context.Context, _ *Plan, node Node, _ map[string]Artifact) (sign.OCISigner, error) {
					adapter := &reverseMetadataSigner{referenceSigner: signer}
					if node.Target == outputPublicationRoute(plan.LifecycleOutputs[0].Name) {
						adapter.wait = secondDone
					} else {
						adapter.done = secondDone
						for _, object := range receipts[1].inspection.Root.Objects {
							if object.Evidence != nil {
								adapter.remaining++
							}
						}
					}
					return adapter, nil
				}
			}
			var progress bytes.Buffer
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			if err := executeReferenceReleaseWithProgress(ctx, plan, planNode(plan, "package/release"), copyArtifacts, factory, &progress); err != nil {
				t.Fatal(err)
			}
			if got := releaseFileBytes(t, out.Path); !reflect.DeepEqual(want, got) {
				t.Fatal("parallel assembly changed release files, descriptors or signed evidence bytes")
			}
			if after, err := plan.Digest(); err != nil || after != before {
				t.Fatalf("Plan identity (including every Build) changed: %s -> %s, %v", before, after, err)
			}
			t.Logf("Plan %s; all %d release/control/evidence files byte-identical to sequential assembly", before, len(want))
		})
	}
}

type reverseMetadataSigner struct {
	*referenceSigner
	wait      <-chan struct{}
	done      chan struct{}
	remaining int
}

func (s *reverseMetadataSigner) Verify(ctx context.Context, subject sign.Subject, policy sign.VerifyPolicy) (sign.VerificationRecord, error) {
	if s.wait != nil {
		select {
		case <-s.wait:
		case <-ctx.Done():
			return sign.VerificationRecord{}, classifyContext(ctx, ctx.Err())
		}
	}
	return s.referenceSigner.Verify(ctx, subject, policy)
}

func (s *reverseMetadataSigner) VerifySBOM(ctx context.Context, subject string, bundle []byte) error {
	if err := s.referenceSigner.VerifySBOM(ctx, subject, bundle); err != nil {
		return err
	}
	if s.done != nil {
		s.remaining--
		if s.remaining == 0 {
			close(s.done)
		}
	}
	return nil
}

type aggregateVerificationSigner struct {
	referenceSigner
	verifications map[string]int
	signatures    int
	fail          bool
	root          string
}

func (s *aggregateVerificationSigner) SignAndAttest(_ context.Context, subject sign.Subject, predicate []byte) (sign.SigningRecord, error) {
	s.signatures++
	return sign.SigningRecord{SchemaVersion: sign.SigningRecordSchemaVersion, Ref: subject.Ref, Digest: subject.Digest,
		Identity: s.identity, Issuer: s.issuer, PredicateType: sign.ProvenancePredicateType,
		PredicateSHA256: pipeline.FileDigest(predicate), Ran: [][]string{{"fixture-sign"}}}, nil
}

func (s *aggregateVerificationSigner) Verify(ctx context.Context, subject sign.Subject, policy sign.VerifyPolicy) (sign.VerificationRecord, error) {
	s.mu.Lock()
	s.verifications[subject.Ref]++
	s.mu.Unlock()
	if s.fail && subject.Ref != s.root {
		return sign.VerificationRecord{}, e.New(e.CodeSignatureInvalid, "rejected exact subject")
	}
	return s.referenceSigner.Verify(ctx, subject, policy)
}

func TestAggregateDeliverablesVerifiedOnce(t *testing.T) {
	plan, artifacts, fixtureSigner := referenceMetadataFixture(t)
	if err := executeReferenceRelease(t.Context(), plan, planNode(plan, "package/release"), artifacts, referenceSignerFactory(fixtureSigner)); err != nil {
		t.Fatal(err)
	}
	inspection, err := delivery.InspectMetadata(t.Context(), artifacts["package/release"].Path)
	if err != nil {
		t.Fatal(err)
	}
	reference := plan.Targets["release"].Ref + "@" + inspection.RootDigest
	writeReferenceFile(t, artifacts["publication/release"].Path, reference)
	signer := &aggregateVerificationSigner{verifications: map[string]int{}}
	signer.identity, signer.issuer = plan.Environment.Signing.Identity, plan.Environment.Signing.Issuer
	newSigner := func(context.Context, *Plan, Node, map[string]Artifact) (sign.OCISigner, error) { return signer, nil }
	if err := executeSign(t.Context(), plan, planNode(plan, "sign/release"), artifacts, newSigner, io.Discard); err != nil {
		t.Fatal(err)
	}
	if signer.signatures != 1 || len(signer.verifications) != 0 {
		t.Fatalf("aggregate signing duplicated output operations: signatures=%d verifications=%v", signer.signatures, signer.verifications)
	}
	for _, fail := range []bool{false, true} {
		signer.fail = fail
		signer.verifications = map[string]int{}
		err := verifyPublicationOutputs(t.Context(), signer, reference, inspection, plan.Environment.Signing, io.Discard)
		if fail {
			assertFactoryErrorCode(t, err, e.CodeSignatureInvalid)
		} else if err != nil {
			t.Fatal(err)
		}
		subjects, err := publicationOutputSubjects(reference, inspection)
		if err != nil {
			t.Fatal(err)
		}
		for _, subject := range subjects {
			if signer.verifications[subject.Ref] != 1 {
				t.Fatalf("subject %s verified %d times", subject.Ref, signer.verifications[subject.Ref])
			}
		}
	}
}
func TestAggregateVerificationGateRejectsEveryFailedDeliverable(t *testing.T) {
	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(server.Close)
	original := referenceTestPlan(t)
	environment := original.Environment
	environment.Publication.RepositoryPrefix = strings.TrimPrefix(server.URL, "http://") + "/candidate"
	environment.Publication.PlainHTTP = true
	environment.Publication.Anonymous = true
	environment.Publication.CredentialRef = ""
	stack, err := catalog.ApplyStack(original.CatalogSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := Compile(t.Context(), original.Definition, environment, stack, original.GeneratorVersion)
	if err != nil {
		t.Fatal(err)
	}
	plan, artifacts, fixtureSigner := referenceMetadataFixtureForPlan(t, compiled)
	options := delivery.RegistryOptions{PlainHTTP: true, Anonymous: true}
	for _, output := range plan.LifecycleOutputs {
		route := outputPublicationRoute(output.Name)
		source := filepath.Join(".nvkr", "factory", "packages", outputPublicationPackage(output.Name))
		if _, err := delivery.Push(t.Context(), source, plan.Targets[route].Ref, options); err != nil {
			t.Fatal(err)
		}
	}
	if err := executeReferenceRelease(t.Context(), plan, planNode(plan, "package/release"), artifacts, referenceSignerFactory(fixtureSigner)); err != nil {
		t.Fatal(err)
	}
	reference, err := delivery.PushMetadata(t.Context(), artifacts["package/release"].Path, plan.Targets["release"].Ref, options)
	if err != nil {
		t.Fatal(err)
	}
	writeReferenceFile(t, artifacts["publication/release"].Path, reference)
	signer := &aggregateVerificationSigner{root: reference, verifications: map[string]int{}}
	signer.identity, signer.issuer = plan.Environment.Signing.Identity, plan.Environment.Signing.Issuer
	newSigner := func(context.Context, *Plan, Node, map[string]Artifact) (sign.OCISigner, error) { return signer, nil }
	if err := executeSign(t.Context(), plan, planNode(plan, "sign/release"), artifacts, newSigner, io.Discard); err != nil {
		t.Fatal(err)
	}
	inspection, err := delivery.InspectMetadata(t.Context(), artifacts["package/release"].Path)
	if err != nil {
		t.Fatal(err)
	}
	subjects, err := publicationOutputSubjects(reference, inspection)
	if err != nil {
		t.Fatal(err)
	}
	for _, fail := range []bool{true, false} {
		t.Run(strconv.FormatBool(fail), func(t *testing.T) {
			signer.fail = fail
			signer.verifications = map[string]int{}
			isolated := maps.Clone(artifacts)
			for _, id := range []string{"verification/release", "metadata/release"} {
				artifact := isolated[id]
				artifact.Path = filepath.Join(t.TempDir(), "result")
				isolated[id] = artifact
			}
			err := executeVerifyPublication(t.Context(), plan, planNode(plan, "verify-publication/release"), isolated, newSigner, io.Discard)
			if fail {
				assertFactoryErrorCode(t, err, e.CodeSignatureInvalid)
				assertNotWritten(t, isolated["verification/release"].Path)
				assertNotWritten(t, isolated["metadata/release"].Path)
			} else if err != nil {
				t.Fatal(err)
			} else if _, err := os.Stat(isolated["verification/release"].Path); err != nil {
				t.Fatal(err)
			}
			if signer.verifications[reference] != 1 {
				t.Fatal("aggregate root was not independently authenticated")
			}
			for _, subject := range subjects {
				if signer.verifications[subject.Ref] != 1 || (fail && !strings.Contains(err.Error(), subject.Ref)) {
					t.Fatalf("missing exact-subject verification/error for %s: %v", subject.Ref, err)
				}
			}
		})
	}
}
