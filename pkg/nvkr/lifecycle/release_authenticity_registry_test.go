// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/assemble"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"github.com/google/go-containerregistry/pkg/registry"
)

// Crypto is deliberately substituted. These tests exercise real registry
// transport and metadata/evidence binding; they do not establish Sigstore trust.
type authenticityRegistryVerifier struct {
	t                 *testing.T
	expectedSubjects  map[string]bool
	expectedBundles   map[string][]byte
	subjects          []string
	bundles           map[string]bool
	failSubject       string
	missingProvenance string
	failSBOM          bool
}

func (*authenticityRegistryVerifier) SignAndAttest(context.Context, sign.Subject, []byte) (sign.SigningRecord, error) {
	return sign.SigningRecord{}, e.New(e.CodeUsage, "verification-only fixture")
}

func (*authenticityRegistryVerifier) AttestSBOM(context.Context, string, []byte) ([]byte, error) {
	return nil, e.New(e.CodeUsage, "verification-only fixture")
}

func (verifier *authenticityRegistryVerifier) Verify(ctx context.Context, subject sign.Subject, policy sign.VerifyPolicy) (sign.VerificationRecord, error) {
	verifier.t.Helper()
	if _, ok := ctx.Deadline(); !ok {
		verifier.t.Fatal("authentication did not bound signature work")
	}
	if !verifier.expectedSubjects[subject.Ref] || subject.Digest != referenceDigest(subject.Ref) || !policy.RequireAttestation || policy.IdentityRegexp != "^builder$" || policy.Issuer != "issuer" {
		verifier.t.Fatalf("unexpected signature subject or policy: %+v, %+v", subject, policy)
	}
	verifier.subjects = append(verifier.subjects, subject.Ref)
	if subject.Ref == verifier.failSubject {
		return sign.VerificationRecord{}, e.New(e.CodeSignatureInvalid, "fixture signature rejected")
	}
	return sign.VerificationRecord{SchemaVersion: sign.VerificationRecordSchemaVersion,
		Ref: subject.Ref, Digest: subject.Digest, Identity: policy.IdentityRegexp, Issuer: policy.Issuer,
		PredicateType: sign.ProvenancePredicateType, SignatureVerified: true,
		AttestationVerified: subject.Ref != verifier.missingProvenance,
		ObservedIdentity:    "builder", ObservedIssuer: "issuer", Ran: [][]string{{"fixture", "verify"}}}, nil
}

func (verifier *authenticityRegistryVerifier) VerifySBOM(ctx context.Context, subject string, raw []byte) error {
	verifier.t.Helper()
	if _, ok := ctx.Deadline(); !ok {
		verifier.t.Fatal("authentication did not bound SBOM verification")
	}
	if !bytes.Equal(raw, verifier.expectedBundles[subject]) {
		verifier.t.Fatalf("SBOM verification did not receive exact bound bytes for %s", subject)
	}
	verifier.bundles[subject] = true
	if verifier.failSBOM {
		return e.New(e.CodeSignatureInvalid, "fixture SBOM signature rejected")
	}
	return nil
}

type authenticityRegistryFixture struct {
	reference     string
	description   delivery.PublicationDescription
	bundles       map[string][]byte
	options       delivery.RegistryOptions
	payloadGets   atomic.Int64
	requests      atomic.Int64
	corruptConfig atomic.Bool
}

func newAuthenticityRegistryFixture(t *testing.T, root string) *authenticityRegistryFixture {
	t.Helper()
	inspection, err := delivery.Inspect(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	metadata := filepath.Join(t.TempDir(), "metadata")
	if _, err := delivery.ExportMetadata(t.Context(), root, metadata); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "blobs", "sha256"))
	if err != nil {
		t.Fatal(err)
	}
	payloads := map[string]bool{}
	for _, entry := range entries {
		_, err := os.Stat(filepath.Join(metadata, "blobs", "sha256", entry.Name()))
		if os.IsNotExist(err) {
			payloads["sha256:"+entry.Name()] = true
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if len(payloads) == 0 {
		t.Fatal("fixture contains no output payloads to forbid")
	}
	config, err := os.ReadFile(filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(inspection.RootConfig.Digest, "sha256:")))
	if err != nil {
		t.Fatal(err)
	}
	fixture := &authenticityRegistryFixture{options: delivery.RegistryOptions{Anonymous: true, PlainHTTP: true}, bundles: map[string][]byte{}}
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	var published atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if published.Load() {
			fixture.requests.Add(1)
			if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/blobs/") {
				if payloads[filepath.Base(r.URL.Path)] {
					fixture.payloadGets.Add(1)
					http.Error(w, "output payloads deliberately unavailable", http.StatusForbidden)
					return
				}
				if fixture.corruptConfig.Load() && filepath.Base(r.URL.Path) == inspection.RootConfig.Digest {
					changed := append([]byte(nil), config...)
					changed[len(changed)-1] ^= 1
					_, _ = w.Write(changed)
					return
				}
			}
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	fixture.reference, err = delivery.Push(t.Context(), root, strings.TrimPrefix(server.URL, "http://")+"/product/releases", fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	published.Store(true)
	fixture.description, err = delivery.InspectMetadataDescription(t.Context(), metadata, fixture.reference)
	if err != nil {
		t.Fatal(err)
	}
	if delivery.RequireAttestedEvidence(inspection) == nil {
		if err := delivery.VisitMetadataEvidence(t.Context(), metadata, inspection.RootDigest, func(_ context.Context, subject string, raw []byte) error {
			fixture.bundles[subject] = append([]byte(nil), raw...)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

func (fixture *authenticityRegistryFixture) consumer(t *testing.T) (*Consumer, *authenticityRegistryVerifier, string) {
	t.Helper()
	workspace := filepath.Join(t.TempDir(), "authentication")
	consumer, err := NewConsumer(ConsumerOptions{Registry: fixture.options, WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	verifier := &authenticityRegistryVerifier{t: t, expectedSubjects: map[string]bool{fixture.reference: true}, expectedBundles: fixture.bundles, bundles: map[string]bool{}}
	for _, output := range fixture.description.Outputs {
		verifier.expectedSubjects[output.Reference] = true
	}
	consumer.verifier = verifier
	return consumer, verifier, workspace
}

func assertAuthenticityWorkspaceClean(t *testing.T, workspace string) {
	t.Helper()
	entries, err := os.ReadDir(workspace)
	if os.IsNotExist(err) {
		return
	}
	if err != nil || len(entries) != 0 {
		t.Fatalf("authentication leaked workspace: %v, %v", entries, err)
	}
}

func authenticityRegistryPolicy() VerificationPolicy {
	return VerificationPolicy{Lane: LanePipeline, Scope: ScopeAuthenticity, IdentityRegexp: "^builder$", Issuer: "issuer"}
}

func TestAuthenticateRegistryNeverDownloadsPayloads(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fixture func(*testing.T) (string, string)
	}{
		{"binary", testPublicationLayout},
		{"APT-and-binary", authenticityRegistryAPTLayout},
		{"disk-amd64", func(t *testing.T) (string, string) {
			return testDiskPublication(t, "amd64", string(DiskEvidenceBuildProvenance), "")
		}},
		{"disk-arm64", func(t *testing.T) (string, string) {
			return testDiskPublication(t, "arm64", string(DiskEvidenceBuildProvenance), "")
		}},
		{"fde-amd64", func(t *testing.T) (string, string) {
			return testDiskPublication(t, "amd64", string(DiskEvidenceBuildProvenance), "", "luks2")
		}},
		{"fde-arm64", func(t *testing.T) (string, string) {
			return testDiskPublication(t, "arm64", string(DiskEvidenceBuildProvenance), "", "luks2")
		}},
		{"aggregate-image", authenticityRegistryIndexLayout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			layout, _ := tc.fixture(t)
			fixture := newAuthenticityRegistryFixture(t, layout)
			consumer, verifier, workspace := fixture.consumer(t)
			authenticated, err := consumer.Authenticate(t.Context(), fixture.reference, authenticityRegistryPolicy())
			if err != nil {
				t.Fatalf("authentication fetched unavailable payload or rejected valid metadata: %v", err)
			}
			defer authenticated.Close()
			view, err := authenticated.Description()
			if err != nil || view.PayloadIntegrityVerified || view.Subject != fixture.reference || view.Scope != ScopeAuthenticity || view.ContentSubject != fixture.description.Metadata.ContentSubject {
				t.Fatalf("incorrect authenticity scope: %+v, %v", view, err)
			}
			expected := make([]Artifact, 0, len(fixture.description.Outputs))
			for _, output := range fixture.description.Outputs {
				expected = append(expected, output.Output)
			}
			if !reflect.DeepEqual(view.Artifacts, expected) {
				t.Fatalf("authentication changed exact artifact membership: %+v, want %+v", view.Artifacts, expected)
			}
			for _, artifact := range view.Artifacts {
				if artifact.Disk != nil {
					artifact.Disk.Producer = "mutated"
				}
				if artifact.ImageIndex != nil {
					artifact.ImageIndex.RequiredPlatforms[0] = "mutated"
					artifact.ImageIndex.Children[0].ManifestDigest = "mutated"
				}
			}
			view, err = authenticated.Description()
			if err != nil || !reflect.DeepEqual(view.Artifacts, expected) {
				t.Fatalf("nested description mutation changed authenticated membership: %+v, %v", view.Artifacts, err)
			}
			if len(verifier.subjects) != len(verifier.expectedSubjects) || len(verifier.bundles) != len(fixture.bundles) {
				t.Fatalf("incomplete authentication: subjects=%v bundles=%v", verifier.subjects, verifier.bundles)
			}
			seen := map[string]bool{}
			for _, subject := range verifier.subjects {
				if seen[subject] {
					t.Fatalf("repeated subject %s replaced an independent output check", subject)
				}
				seen[subject] = true
			}
			for _, artifact := range view.Artifacts {
				if artifact.Kind == OutputAPT && artifact.Digest == referenceDigest(artifact.Reference) {
					t.Fatal("APT fixture does not distinguish semantic and transport identity")
				}
			}
			if fixture.payloadGets.Load() != 0 || fixture.requests.Load() == 0 {
				t.Fatalf("metadata requests=%d, forbidden payload GETs=%d", fixture.requests.Load(), fixture.payloadGets.Load())
			}
			assertAuthenticityWorkspaceClean(t, workspace)
			if tc.name == "binary" || strings.HasPrefix(tc.name, "fde-") {
				policy := authenticityRegistryPolicy()
				policy.Scope = ScopeArtifactPublication
				ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
				defer cancel()
				verified, err := consumer.Verify(ctx, fixture.reference, policy)
				assertCode(t, err, e.CodeUnavailable)
				if verified != nil || fixture.payloadGets.Load() == 0 {
					t.Fatal("full verification accepted inaccessible artifact bytes")
				}
				assertAuthenticityWorkspaceClean(t, workspace)
			}
		})
	}
}

func TestAuthenticateRegistryRejectsMissingOrCorruptEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		code e.Code
	}{
		{"root signature", e.CodeSignatureInvalid},
		{"output signature", e.CodeSignatureInvalid},
		{"output provenance", e.CodeSignatureInvalid},
		{"SBOM signature", e.CodeSignatureInvalid},
		{"metadata corruption", e.CodeFetchFailed},
		{"raw SBOM", e.CodeProvenanceMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var layout string
			if tc.name == "raw SBOM" {
				layout, _ = testPreparedPublicationLayout(t)
			} else {
				layout, _ = testPublicationLayout(t)
			}
			fixture := newAuthenticityRegistryFixture(t, layout)
			consumer, verifier, workspace := fixture.consumer(t)
			switch tc.name {
			case "root signature":
				verifier.failSubject = fixture.reference
			case "output signature":
				verifier.failSubject = fixture.description.Outputs[0].Reference
			case "output provenance":
				verifier.missingProvenance = fixture.description.Outputs[0].Reference
			case "SBOM signature":
				verifier.failSBOM = true
			case "metadata corruption":
				fixture.corruptConfig.Store(true)
			}
			authenticated, err := consumer.Authenticate(t.Context(), fixture.reference, authenticityRegistryPolicy())
			assertCode(t, err, tc.code)
			if authenticated != nil || fixture.payloadGets.Load() != 0 {
				t.Fatal("rejection returned partial authentication or downloaded payloads")
			}
			if tc.name == "root signature" && fixture.requests.Load() != 0 {
				t.Fatal("unauthenticated root reached metadata acquisition")
			}
			assertAuthenticityWorkspaceClean(t, workspace)
		})
	}
}

func TestAuthenticateRegistryEnforcesDiskEvidenceMinimum(t *testing.T) {
	for _, tc := range []struct{ arch, mode, encryption string }{
		{"amd64", "", "none"},
		{"amd64", string(DiskEvidenceBuildProvenance), "none"},
		{"amd64", string(DiskEvidenceBuildProvenance), "luks2"},
		{"arm64", string(DiskEvidenceBuildProvenance), "luks2"},
	} {
		t.Run(tc.arch+"/"+tc.encryption+"/"+tc.mode, func(t *testing.T) {
			layout, _ := testDiskPublication(t, tc.arch, tc.mode, "", tc.encryption)
			fixture := newAuthenticityRegistryFixture(t, layout)
			consumer, _, workspace := fixture.consumer(t)
			policy := authenticityRegistryPolicy()
			policy.MinimumDiskEvidence = DiskEvidenceStrict
			authenticated, err := consumer.Authenticate(t.Context(), fixture.reference, policy)
			if tc.mode != "" {
				assertCode(t, err, e.CodeGateUnmet)
				if authenticated != nil {
					t.Fatal("producer-only disk passed strict evidence policy")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if err := authenticated.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if fixture.payloadGets.Load() != 0 {
				t.Fatal("disk evidence policy read QCOW2 payloads")
			}
			assertAuthenticityWorkspaceClean(t, workspace)
		})
	}
}

func authenticityRegistryAPTLayout(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	binary := filepath.Join(root, "agent")
	if err := os.WriteFile(binary, []byte("agent bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	deb := filepath.Join(root, "agent.deb")
	//nolint:staticcheck // Compatibility assembler provides exact Debian fixture bytes.
	if _, err := assemble.Deb(binary, "agent", "1.0.0", 0, "amd64", deb); err != nil {
		t.Fatal(err)
	}
	transportPath := ".nvkr/factory/packages/apt"
	apt, err := delivery.BuildAPTRepository(t.Context(), filepath.Join(root, filepath.FromSlash(transportPath)), delivery.APTRequest{Distribution: "stable", Component: "main", Packages: []delivery.DebianPackageInput{{Path: deb}}})
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "repository.tar")
	if err := transportarchive.Pack(t.Context(), archive, root, []string{transportPath}); err != nil {
		t.Fatal(err)
	}
	aptBytes, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	debBytes, err := os.ReadFile(deb)
	if err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join(root, "tools")
	bundle, err := delivery.BuildBinaryBundle(t.Context(), tools, delivery.BinaryBundleRequest{OS: "linux", Arch: "amd64", Purpose: "tools",
		Files: []delivery.BinaryFileInput{{Name: "agent-bin", Path: binary, InstallPath: "/usr/local/bin/agent", Mode: 0o755}}})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := delivery.PayloadDescriptor(t.Context(), tools)
	if err != nil {
		t.Fatal(err)
	}
	build := "sha256:" + repeat("5", 64)
	artifacts := []Artifact{{Name: "apt", Kind: OutputAPT, Digest: apt.Digest, BuildDigest: build, Components: []string{"agent"}},
		{Name: "tools", Kind: OutputExecutable, Digest: manifest.Digest, BuildDigest: build, Components: []string{"agent-bin"}}}
	objects := []delivery.ObjectInput{{Name: "apt", Kind: "apt-repository", ArtifactType: "application/vnd.nvkr.apt-repository.v1",
		LayerMediaType: "application/vnd.nvkr.apt-repository.v1.tar", SemanticDigest: apt.Digest, Bytes: aptBytes},
		{Name: "tools", Kind: "binary-bundle", Path: tools, Layout: true, ArtifactType: delivery.MediaBinaryBundle, SemanticDigest: bundle.SemanticDigest, BlobDigest: bundle.RootDigest}}
	request := testReleaseRequest(t, artifacts, objects, map[string]string{"agent": pipeline.FileDigest(debBytes), "agent-bin": pipeline.FileDigest([]byte("agent bytes"))})
	request.Objects[0].Output.TransportPath = transportPath
	prepared := filepath.Join(root, "publication")
	if _, err := delivery.BuildPublicationRoot(t.Context(), prepared, request); err != nil {
		t.Fatal(err)
	}
	attested, result := attestFixtureLayout(t, prepared)
	return attested, result.RootDigest
}

// Public constructors create a two-platform aggregate and exact per-platform
// SBOMs. Only the aggregate is an independent signature/provenance subject.
func authenticityRegistryIndexLayout(t *testing.T) (string, string) {
	t.Helper()
	platforms := []string{"linux/amd64", "linux/arm64"}
	var inputs []delivery.ImageIndexInput
	for _, arch := range []string{"amd64", "arm64"} {
		root := t.TempDir()
		binary := filepath.Join(root, "worker")
		if err := os.WriteFile(binary, []byte("synthetic worker"), 0o755); err != nil {
			t.Fatal(err)
		}
		layout := filepath.Join(root, "image")
		manifest, err := assemble.OCIImage(binary, "worker", 1, arch, layout)
		if err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, delivery.ImageIndexInput{Platform: "linux/" + arch, LayoutPath: layout, ManifestDigest: manifest})
	}
	index, err := delivery.AssembleImageIndex(t.Context(), platforms, inputs)
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	description, err := index.Description()
	if err != nil {
		t.Fatal(err)
	}
	layout := filepath.Join(t.TempDir(), "index")
	if err := index.WriteLayout(t.Context(), layout); err != nil {
		t.Fatal(err)
	}
	binding := &delivery.ImageIndexBinding{RequiredPlatforms: platforms}
	var builds []ir.ImageIndexBuildChild
	for _, child := range description.Children {
		build := pipeline.FileDigest([]byte("build " + child.Platform))
		binding.Children = append(binding.Children, delivery.ImageIndexChildBinding{Platform: child.Platform,
			ManifestDigest: child.ManifestDigest, Size: child.Size, BuildDigest: build,
			Component: "controller", BuildSubject: pipeline.FileDigest([]byte("export " + child.Platform))})
		builds = append(builds, ir.ImageIndexBuildChild{Platform: child.Platform, BuildDigest: build, Component: "controller"})
	}
	build, err := ir.ImageIndexBuildDigest(platforms, builds)
	if err != nil {
		t.Fatal(err)
	}
	objects := []delivery.ObjectInput{{Name: "cwe", Kind: "container-image", ArtifactType: delivery.MediaOCIIndex, Path: layout, Layout: true,
		SemanticDigest: description.Digest, BlobDigest: description.Digest,
		Output: &delivery.OutputBinding{Repository: "cwe", Format: "oci", BuildDigest: build, Components: []string{"controller"}, ImageIndex: binding}}}
	for _, child := range binding.Children {
		bom, err := sbom.NewSubject(child.BuildDigest, child.Component, child.ManifestDigest)
		if err != nil {
			t.Fatal(err)
		}
		bom, err = sbom.ForSubject(bom, child.ManifestDigest, "build-output", child.BuildSubject)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := bom.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		semantic, err := bom.Digest()
		if err != nil {
			t.Fatal(err)
		}
		objects = append(objects, delivery.ObjectInput{Name: "sbom-" + strings.TrimPrefix(semantic, "sha256:"), Kind: "sbom",
			ArtifactType: "application/vnd.cyclonedx+json", LayerMediaType: "application/vnd.cyclonedx+json", SemanticDigest: semantic, Bytes: raw,
			Evidence: &delivery.EvidenceBinding{Outputs: []string{"cwe"}, Platform: child.Platform, Component: child.Component,
				Stage: "build-output", SubjectKind: "oci-manifest", SubjectDigest: child.ManifestDigest, InspectionInputDigest: child.BuildSubject}})
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].Name < objects[j].Name })
	content, err := delivery.ContentDigest([]delivery.Output{{Name: "cwe", Kind: "oci", Digest: description.Digest, BuildDigest: build,
		Components: []string{"controller"}, ImageIndex: binding}})
	if err != nil {
		t.Fatal(err)
	}
	prepared := filepath.Join(t.TempDir(), "publication")
	if _, err := delivery.BuildPublicationRoot(t.Context(), prepared, delivery.PublicationRequest{
		Release: &delivery.ReleaseMetadata{DefinitionDigest: build, EnvironmentDigest: build, PlanDigest: build, ContentSubject: content}, Objects: objects,
	}); err != nil {
		t.Fatal(err)
	}
	attested, result := attestFixtureLayout(t, prepared)
	return attested, result.RootDigest
}
