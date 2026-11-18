// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

func TestBootPublicationCommitsInventoryAfterFreshRecheckAndProofs(t *testing.T) {
	for _, scenario := range []string{"complete", "publication readback failure", "proof readback failure"} {
		t.Run(scenario, func(t *testing.T) {
			sources := map[string]string{}
			selection, assets, bindings, limits := bootTransferFixture(t, sources)
			store := &bootProofCorruptionStore{mirrorMemoryStore: &mirrorMemoryStore{}}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, found := store.data[strings.TrimPrefix(r.URL.Path, "/boot/")]
				if !found {
					http.NotFound(w, r)
					return
				}
				_, _ = w.Write(raw)
			}))
			defer server.Close()
			original := http.DefaultTransport
			http.DefaultTransport = server.Client().Transport
			defer func() { http.DefaultTransport = original }()
			phaseRequest := bootPhaseRequestFixture(t, selection, limits, server.URL)
			var records []BootStoredAsset
			err := withBootPhase(t.Context(), phaseRequest, BootReaderCredential{}, func(context.Context) (ObjectStore, error) { return store, nil }, func(ctx context.Context, phase *BootPhase) error {
				receipt, err := phase.Copy(ctx, assets)
				if err != nil {
					return err
				}
				records, err = receipt.Descriptions()
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			var root PublicationRoot
			objectSources := map[string]string{}
			var publications []BootProofPublication
			verifier := mirrorFixtureSigner(t)
			proofs := []*sign.VerifiedOCIEvidence{}
			for _, binding := range bindings {
				inspection, err := inspectMetadata(t.Context(), sources[binding.Output], true)
				if err != nil {
					t.Fatal(err)
				}
				if root.Release == nil {
					root = inspection.Root
					root.Objects = nil
					metadata := *root.Release
					root.Release = &metadata
				}
				root.Objects = append(root.Objects, inspection.Root.Objects...)
				for _, object := range inspection.Root.Objects {
					objectSources[object.Name] = sources[binding.Output]
				}
				sparse := filepath.Join(t.TempDir(), "sparse")
				_, pin, _ := strings.Cut(binding.Publication, "@")
				if _, err := CopySelectedMetadataWithExpectedRoot(t.Context(), sources[binding.Output], sparse, pin, []string{binding.Output}); err != nil {
					t.Fatal(err)
				}
				owner, err := OpenSelectedMetadata(t.Context(), sparse, pin, []string{binding.Output}, "")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = owner.Close() })
				publications = append(publications, BootProofPublication{Reference: binding.Publication, Metadata: owner})
				proofs = append(proofs, mirrorFixtureProof(t, verifier, binding.Publication))
				description, err := owner.Description(binding.Publication)
				if err != nil {
					t.Fatal(err)
				}
				proofs = append(proofs, mirrorFixtureProof(t, verifier, description.Outputs[0].Reference))
			}
			slices.SortFunc(root.Objects, func(a, b PublishedObject) int { return strings.Compare(a.Name, b.Name) })
			outputs, err := MetadataOutputs(root)
			if err != nil {
				t.Fatal(err)
			}
			root.Release.ContentSubject, err = ContentDigest(outputs)
			if err != nil {
				t.Fatal(err)
			}
			root.Release.InvocationID = "https://github.com/example/nvkr/actions/runs/123/attempts/1"
			aggregatePath := filepath.Join(t.TempDir(), "aggregate")
			aggregate, err := BuildMetadataRoot(t.Context(), aggregatePath, root, objectSources)
			if err != nil {
				t.Fatal(err)
			}
			aggregateReference := "registry.example.test/releases@" + aggregate.RootDigest
			owner, err := OpenSelectedMetadata(t.Context(), aggregatePath, aggregate.RootDigest, []string{"node-image", "tools"}, "")
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Close()
			publications = append(publications, BootProofPublication{Reference: aggregateReference, Metadata: owner})
			proofs = append(proofs, mirrorFixtureProof(t, verifier, aggregateReference))
			expected := BootInventoryExpectation{RootReference: aggregateReference, Invocation: root.Release.InvocationID,
				DefinitionDigest: root.Release.DefinitionDigest, EnvironmentDigest: root.Release.EnvironmentDigest, PlanDigest: root.Release.PlanDigest,
				Selection: selection, Destination: phaseRequest.Destination}
			uses := make([]BootOriginalUse, len(records))
			for index, record := range records {
				uses[index] = BootOriginalUse{Binding: record.Binding, URL: record.URL, PreparedInvocation: expected.Invocation, SignedInvocation: expected.Invocation}
			}
			writesBeforeFinal := len(store.writes)
			var locator StoredObject
			err = withBootPhase(t.Context(), phaseRequest, BootReaderCredential{}, func(context.Context) (ObjectStore, error) { return store, nil }, func(ctx context.Context, phase *BootPhase) error {
				receipt, err := phase.Recheck(ctx, bindings, records)
				if err != nil {
					return err
				}
				if scenario == "publication readback failure" {
					store.corrupt = "same-size"
				}
				if scenario == "proof readback failure" {
					_, signature, _, err := proofs[0].Bundles()
					if err != nil {
						return err
					}
					store.proofDigest = digestBytes(signature)
				}
				locator, err = phase.PublishProofs(ctx, BootPublicationRequest{Expected: expected, Availability: receipt, Uses: uses, Publications: publications, Proofs: proofs})
				return err
			})
			if scenario != "complete" {
				assertRegistryCode(t, err, e.CodeVerifyFailed)
				if len(store.writes) <= writesBeforeFinal {
					t.Fatal("failure preceded final writes")
				}
				if err == nil || locator != (StoredObject{}) {
					t.Fatal("corrupt write returned completion")
				}
				if scenario == "proof readback failure" {
					if !store.corrupted || len(store.writes) <= writesBeforeFinal+1 || store.writes[len(store.writes)-1].SHA256 != store.proofDigest {
						t.Fatal("proof failure did not follow successful control writes")
					}
				}
				for _, object := range store.writes {
					if strings.HasPrefix(object.Key, "boot-publications/") {
						t.Fatal("inventory was committed before proof completion")
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(store.writes) < 3 || store.writes[len(store.writes)-1].Key != locator.Key {
				t.Fatal("inventory was not last")
			}
			if _, err := decodeBootInventory(store.data[locator.Key], locator, expected, limits); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Corrupt only the chosen proof after earlier publication objects pass readback.
type bootProofCorruptionStore struct {
	*mirrorMemoryStore
	proofDigest string
	corrupted   bool
}

func (store *bootProofCorruptionStore) PutIfAbsent(ctx context.Context, object Object, source io.ReadSeeker) (StoredObject, error) {
	result, err := store.mirrorMemoryStore.PutIfAbsent(ctx, object, source)
	if err == nil && object.SHA256 == store.proofDigest {
		store.data[object.Key][0] ^= 1
		store.corrupted = true
	}
	return result, err
}
