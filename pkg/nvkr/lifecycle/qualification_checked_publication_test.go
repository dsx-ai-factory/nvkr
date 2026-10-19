// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/google/go-containerregistry/pkg/registry"
)

// Signatures use the existing verifier fixture. Registry transport, complete
// payload checking and retained executable reads use the production owners.
func TestQualificationUsesCheckedRegistryPublication(t *testing.T) {
	request, fixtureConsumer, verifier := newQualificationFixture(t)
	publication, err := fixtureConsumer.materialize(t.Context(), request.Reference)
	if err != nil {
		t.Fatal(err)
	}
	layout := filepath.Join(t.TempDir(), "publication")
	if err := publication.WriteLayout(t.Context(), layout); err != nil {
		t.Fatal(err)
	}
	if err := publication.Close(); err != nil {
		t.Fatal(err)
	}
	inspection, err := delivery.Inspect(t.Context(), layout)
	if err != nil {
		t.Fatal(err)
	}
	payloads := map[string]string{}
	for _, object := range inspection.Root.Objects {
		if object.Output == nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(layout, "blobs", "sha256", strings.TrimPrefix(object.DescriptorDigest, "sha256:")))
		if err != nil {
			t.Fatal(err)
		}
		var manifest struct{ Layers []delivery.Descriptor }
		if err := json.Unmarshal(raw, &manifest); err != nil || len(manifest.Layers) != 1 {
			t.Fatalf("fixture output layers: %+v, %v", manifest, err)
		}
		payloads[manifest.Layers[0].Digest] = object.Name
	}
	if len(payloads) != len(request.Plan.LifecycleOutputs) || len(payloads) < 2 {
		t.Fatal("fixture must contain independent sibling payloads")
	}
	var mu sync.Mutex
	seen := map[string]bool{}
	missing := ""
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/blobs/") {
			mu.Lock()
			name := payloads[filepath.Base(r.URL.Path)]
			if name != "" {
				seen[name] = true
			}
			blocked := name != "" && name == missing
			mu.Unlock()
			if blocked {
				http.Error(w, "sibling payload unavailable", http.StatusNotFound)
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	options := delivery.RegistryOptions{Anonymous: true, PlainHTTP: true}
	reference, err := delivery.Push(t.Context(), layout, strings.TrimPrefix(server.URL, "http://")+"/product/releases", options)
	if err != nil {
		t.Fatal(err)
	}
	// Only the root transport location changes; output provenance retains its
	// original authenticated publishing invocation and target identity.
	oldReference := request.Reference
	request.Reference = reference
	for i := range request.Predicates {
		if request.Predicates[i].Reference == oldReference {
			request.Predicates[i].Reference = reference
		}
	}
	// Use the publication owner to obtain its exact transport projection rather
	// than treating root-relative reference construction as authority.
	local, err := delivery.OpenPublication(t.Context(), layout, inspection.RootDigest)
	if err != nil {
		t.Fatal(err)
	}
	view, err := local.Description(reference)
	if err != nil {
		t.Fatal(err)
	}
	if err := local.Close(); err != nil {
		t.Fatal(err)
	}
	for i, output := range view.Outputs {
		request.Predicates[i+1].Reference = output.Reference
	}
	for _, tc := range []struct {
		name    string
		missing string
	}{
		{name: "complete checked owner"},
		{name: "missing final sibling", missing: view.Outputs[len(view.Outputs)-1].Name},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			missing, seen = tc.missing, map[string]bool{}
			mu.Unlock()
			workspace := filepath.Join(t.TempDir(), "checks")
			consumer, err := NewConsumer(ConsumerOptions{Registry: options, WorkspaceRoot: workspace})
			if err != nil {
				t.Fatal(err)
			}
			consumer.verifier = verifier
			consumer.materialize = func(context.Context, string) (*delivery.Publication, error) {
				return nil, e.New(e.CodeInternal, "qualification used the full retained release materializer")
			}
			receipt, report, err := consumer.checkQualification(t.Context(), request, verifier.source)
			if tc.missing != "" {
				assertCode(t, err, e.CodeFetchFailed)
				if err == nil || receipt != nil || report != nil {
					t.Fatalf("missing sibling issued authority: %v, %+v, %v", receipt, report, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = receipt.Close() }()
				if _, ok := receipt.lifetime.verified.publication.(*delivery.CheckedPublication); !ok {
					t.Fatal("qualification retained a full release owner")
				}
				for _, output := range report.Outputs {
					bundle, err := receipt.lifetime.verified.publication.ExecutableBundle(output.Name)
					if err != nil {
						t.Fatal(err)
					}
					entry := bundle.Entries[0]
					var data bytes.Buffer
					member, err := receipt.WriteExecutableMember(t.Context(), output.Name, entry.Name, bundle.OS+"/"+bundle.Arch, entry.Size, &data)
					if err != nil || member.PayloadDigest != pipeline.FileDigest(data.Bytes()) || int64(data.Len()) != entry.Size {
						t.Fatalf("retained executable unavailable after complete checking: %+v, %v", member, err)
					}
				}
				if err := receipt.Close(); err != nil {
					t.Fatal(err)
				}
				_, err = receipt.Description()
				assertCode(t, err, e.CodeGateUnmet)
			}
			mu.Lock()
			for _, name := range payloads {
				if !seen[name] {
					t.Errorf("complete check skipped sibling %s", name)
				}
			}
			mu.Unlock()
			assertAuthenticityWorkspaceClean(t, workspace)
		})
	}
	t.Run("ordinary verification retains full export", func(t *testing.T) {
		mu.Lock()
		missing = ""
		mu.Unlock()
		consumer, err := NewConsumer(ConsumerOptions{Registry: options, WorkspaceRoot: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		consumer.verifier = verifier
		consumer.materializeChecked = func(context.Context, string) (qualificationPublication, error) {
			t.Fatal("ordinary release verification used the narrowed qualification owner")
			return nil, nil
		}
		expectation, err := PrepareExactInputs(request.Definition, request.Environment, request.Plan)
		if err != nil {
			t.Fatal(err)
		}
		policy := VerificationPolicy{Lane: LanePipeline, Scope: ScopeArtifactPublication,
			IdentityRegexp: "^" + regexp.QuoteMeta(verifier.origin.BuilderID) + "$", Issuer: request.Environment.Signing.Issuer}
		verified, inputs, err := consumer.VerifyExactInputs(t.Context(), request.Reference, policy, expectation)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = verified.Close() }()
		exported := filepath.Join(t.TempDir(), "exported")
		if err := verified.ExportForInvocation(t.Context(), inputs, verifier.origin.InvocationID, exported); err != nil {
			t.Fatal(err)
		}
		complete, err := delivery.Inspect(t.Context(), exported)
		if err != nil || complete.RootDigest != inspection.RootDigest {
			t.Fatalf("ordinary verification lost complete export: %+v, %v", complete, err)
		}
	})
}

type qualificationCleanupPublication struct {
	qualificationPublication
	closeErr error
	closed   int
	mismatch bool
}

func (publication *qualificationCleanupPublication) Close() error {
	publication.closed++
	return errors.Join(publication.qualificationPublication.Close(), publication.closeErr)
}

func (publication *qualificationCleanupPublication) Description(reference string) (delivery.PublicationDescription, error) {
	description, err := publication.qualificationPublication.Description(reference)
	if publication.mismatch {
		description.Metadata.PlanDigest = "sha256:" + strings.Repeat("a", 64)
	}
	return description, err
}

func TestQualificationCheckedOwnerPreservesCleanupFailures(t *testing.T) {
	request, fixtureConsumer, verifier := newQualificationFixture(t)
	cleanup := e.New(e.CodeImmutableViolation, "checked publication cleanup failed")
	for _, tc := range []struct {
		name     string
		mismatch bool
		checkErr error
		code     e.Code
	}{
		{name: "exact inputs", mismatch: true, code: e.CodeVerifyFailed},
		{name: "late evidence", checkErr: e.New(e.CodeSignatureInvalid, "evidence rejected"), code: e.CodeSignatureInvalid},
		{name: "successful receipt close"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verifier.evidenceErr = tc.checkErr
			var held *qualificationCleanupPublication
			consumer := &Consumer{verifier: verifier, materializeChecked: func(ctx context.Context, reference string) (qualificationPublication, error) {
				publication, err := fixtureConsumer.materialize(ctx, reference)
				if err != nil {
					return nil, err
				}
				held = &qualificationCleanupPublication{qualificationPublication: publication, closeErr: cleanup, mismatch: tc.mismatch}
				return held, nil
			}}
			receipt, report, err := consumer.checkQualification(t.Context(), request, verifier.source)
			if tc.code != "" {
				assertCode(t, err, tc.code)
				if receipt != nil || !errors.Is(err, cleanup) {
					t.Fatalf("failed qualification issued a receipt or lost cleanup cause: %v, %v", receipt, err)
				}
				if tc.checkErr != nil && (report == nil || !errors.Is(err, tc.checkErr)) {
					t.Fatalf("late evidence lost report or original cause: %+v, %v", report, err)
				}
			} else {
				if err != nil || receipt == nil {
					t.Fatalf("successful qualification failed: %v", err)
				}
				copyReceipt := *receipt
				for _, owner := range []*ArtifactCheckReceipt{receipt, &copyReceipt} {
					if err := owner.Close(); !errors.Is(err, cleanup) {
						t.Fatalf("close lost cleanup error: %v", err)
					}
					_, err := owner.Description()
					assertCode(t, err, e.CodeGateUnmet)
				}
			}
			if held == nil || held.closed != 1 {
				t.Fatalf("checked owner not closed exactly once: %+v", held)
			}
			if err := verifier.source.Require(); err != nil {
				t.Fatalf("borrowed source was closed: %v", err)
			}
		})
	}
}
