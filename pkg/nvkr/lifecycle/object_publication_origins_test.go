// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/provenance"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

type objectOriginLayout struct {
	path, reference string
	inspection      delivery.Inspection
}

func objectOriginInvocation(attempt string) string {
	return "https://github.com/example/project/actions/runs/123/attempts/" + attempt
}

func objectOriginPredicate(t *testing.T, metadata delivery.ReleaseMetadata, attempt string) []byte {
	t.Helper()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	env := map[string]string{"GITHUB_SERVER_URL": "https://github.com", "GITHUB_REPOSITORY": "example/project", "GITHUB_WORKFLOW_REF": "example/project/.github/workflows/build.yml@refs/heads/main", "GITHUB_RUN_ID": "123", "GITHUB_RUN_ATTEMPT": attempt}
	run, err := provenance.NewGitHubRunContext(func(key string) string { return env[key] }, now, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := provenance.MarshalProduction(provenance.ProductionPredicate(run, provenance.ProductionInputs{BuildType: provenance.BuildTypeHermeticBuild, PlanDigest: metadata.PlanDigest, RunPlanReference: "nvkr:synthetic-exact-plan", DefinitionDigest: metadata.DefinitionDigest, EnvironmentDigest: metadata.EnvironmentDigest, CatalogDigest: "sha256:" + strings.Repeat("1", 64), GeneratorVersion: "fixture", SigningMode: provenance.SigningModeCosign}))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func objectOriginLocal(t *testing.T, fixture exactInputFixture, name, attempt string) objectOriginLayout {
	t.Helper()
	metadata := *fixture.request.Release
	metadata.InvocationID = objectOriginInvocation(attempt)
	request := delivery.PublicationRequest{Release: &metadata}
	for _, object := range fixture.request.Objects {
		keep := object.Output != nil && object.Name == name
		if object.Evidence != nil {
			for _, output := range object.Evidence.Outputs {
				keep = keep || output == name
			}
		}
		if keep {
			request.Objects = append(request.Objects, object)
		}
	}
	original, err := delivery.OpenPublication(t.Context(), fixture.layout, fixture.rootDigest)
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := original.Outputs()
	if err != nil {
		t.Fatal(err)
	}
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	var selected []delivery.Output
	for _, output := range outputs {
		if output.Name == name {
			selected = append(selected, output)
		}
	}
	metadata.ContentSubject, err = delivery.ContentDigest(selected)
	if err != nil {
		t.Fatal(err)
	}
	prepared := filepath.Join(t.TempDir(), "prepared")
	if _, err := delivery.BuildPublicationRoot(t.Context(), prepared, request); err != nil {
		t.Fatal(err)
	}
	path, result := attestFixtureLayout(t, prepared)
	inspection, err := delivery.Inspect(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	return objectOriginLayout{path: path, reference: "registry.example.test/product/releases-" + name + "@" + result.RootDigest, inspection: inspection}
}

func objectOriginAggregate(t *testing.T, fixture exactInputFixture, locals []objectOriginLayout, origins map[string]objectOriginLayout, attempt string) objectOriginLayout {
	t.Helper()
	metadata := *fixture.request.Release
	metadata.InvocationID = objectOriginInvocation(attempt)
	root := delivery.PublicationRoot{SchemaVersion: delivery.PublicationRootSchema, Qualification: delivery.QualificationDiagnostic, Release: &metadata}
	sources := map[string]string{}
	for _, local := range locals {
		for _, object := range local.inspection.Root.Objects {
			if object.Output != nil {
				binding := *object.Output
				object.Output = &binding
				origin := origins[object.Name]
				invocation := origin.inspection.Root.Release.InvocationID
				binding.Publication = &delivery.PublicationOrigin{Ref: origin.reference, PreparedInvocationID: invocation, SignedInvocationID: invocation}
			}
			root.Objects = append(root.Objects, object)
			sources[object.Name] = local.path
		}
	}
	sort.Slice(root.Objects, func(i, j int) bool {
		return root.Objects[i].Name+"\x00"+root.Objects[i].Kind < root.Objects[j].Name+"\x00"+root.Objects[j].Kind
	})
	out := filepath.Join(t.TempDir(), "aggregate")
	result, err := delivery.BuildMetadataRoot(t.Context(), out, root, sources)
	if err != nil {
		t.Fatal(err)
	}
	for _, local := range locals {
		objectOriginJoinPayloads(t, local.path, out)
	}
	inspection, err := delivery.Inspect(t.Context(), out)
	if err != nil {
		t.Fatal(err)
	}
	return objectOriginLayout{path: out, reference: "registry.example.test/product/releases@" + result.RootDigest, inspection: inspection}
}

// The metadata owner emits the new aggregate's own controls. Join only source
// payload closure, excluding the old local root manifest and config so the
// aggregate remains a valid closed OCI graph rather than an arbitrary file bag.
func objectOriginJoinPayloads(t *testing.T, source, destination string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(source, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var index struct {
		Manifests []delivery.Descriptor `json:"manifests"`
	}
	if err := json.Unmarshal(raw, &index); err != nil || len(index.Manifests) != 1 {
		t.Fatalf("original index: %v", err)
	}
	manifestDigest := strings.TrimPrefix(index.Manifests[0].Digest, "sha256:")
	raw, err = os.ReadFile(filepath.Join(source, "blobs", "sha256", manifestDigest))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Config delivery.Descriptor `json:"config"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(source, "blobs", "sha256"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == manifestDigest || entry.Name() == strings.TrimPrefix(manifest.Config.Digest, "sha256:") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(source, "blobs", "sha256", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(destination, "blobs", "sha256", entry.Name()), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func objectOriginConsumer(t *testing.T, verifier *objectIntegrationVerifier, certificate []byte, layouts []objectOriginLayout, attempts map[string]string) *Consumer {
	t.Helper()
	byRef := map[string]objectOriginLayout{}
	for _, local := range layouts {
		byRef[local.reference] = local
		publication, err := delivery.OpenPublication(t.Context(), local.path, local.inspection.RootDigest)
		if err != nil {
			t.Fatal(err)
		}
		description, err := publication.Description(local.reference)
		if err != nil {
			t.Fatal(err)
		}
		if err := publication.Close(); err != nil {
			t.Fatal(err)
		}
		add := func(reference, attempt string, metadata delivery.ReleaseMetadata) {
			digest := referenceDigest(reference)
			verifier.bundles[reference] = [2][]byte{objectIntegrationBundle(t, certificate, digest, sign.CosignSignaturePredicateType, []byte(`{}`)), objectIntegrationBundle(t, certificate, digest, sign.ProvenancePredicateType, objectOriginPredicate(t, metadata, attempt))}
		}
		add(local.reference, attempts[local.reference], description.Metadata)
		for _, output := range description.Outputs {
			attempt := attempts[local.reference]
			metadata := description.Metadata
			if output.Publication != nil {
				attempt = strings.TrimPrefix(output.Publication.SignedInvocationID, objectOriginInvocation(""))
				metadata.InvocationID = output.Publication.PreparedInvocationID
			}
			add(output.Reference, attempt, metadata)
		}
	}
	return &Consumer{verifier: verifier, materialize: func(ctx context.Context, reference string) (*delivery.Publication, error) {
		local, ok := byRef[reference]
		if !ok {
			return nil, e.New(e.CodeVerifyFailed, "unexpected original source locator")
		}
		return delivery.OpenPublication(ctx, local.path, local.inspection.RootDigest)
	}}
}

func TestObjectPublicationRetainsEarlierOutputOrigins(t *testing.T) {
	fixture, _, verifier, certificate, _ := objectIntegrationSource(t)
	locals := []objectOriginLayout{objectOriginLocal(t, fixture, "cli", "1"), objectOriginLocal(t, fixture, "runtime", "2")}
	origins := map[string]objectOriginLayout{"cli": locals[0], "runtime": locals[1]}
	aggregate := objectOriginAggregate(t, fixture, locals, origins, "3")
	layouts := append(append([]objectOriginLayout(nil), locals...), aggregate)
	consumer := objectOriginConsumer(t, verifier, certificate, layouts, map[string]string{locals[0].reference: "1", locals[1].reference: "2", aggregate.reference: "3"})
	expectation, err := PrepareExactInputs(fixture.definition, fixture.environment, fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	store := &objectIntegrationStore{data: map[string][]byte{}}
	limits := delivery.MirrorLimits{MaxObjectBytes: 64 << 20, MaxTotalBytes: 256 << 20, MaxObjects: 4096}
	locator, err := consumer.MirrorExactInputs(t.Context(), aggregate.reference, objectIntegrationPolicy(), expectation, objectOriginInvocation("3"), store, limits)
	if err != nil {
		t.Fatal(err)
	}
	consumer.materialize = func(context.Context, string) (*delivery.Publication, error) {
		t.Fatal("offline origin verification consulted original source registry")
		return nil, nil
	}
	verified, receipt, err := consumer.VerifyObjectPublication(t.Context(), store, locator, ObjectPublicationVerification{Reference: aggregate.reference, Invocation: objectOriginInvocation("3"), Policy: objectIntegrationPolicy(), Expectation: expectation, Limits: limits})
	if err != nil {
		t.Fatal(err)
	}
	defer verified.Close()
	if receipt == nil {
		t.Fatal("earlier output origins lost exact-input acceptance")
	}
}

// A raw-DEB output keeps its raw SHA independent of its wrapper descriptor.
// Add a standard OCI annotation to make a second valid wrapper over the same
// producer bytes; this avoids a detached or already-invalid substitution test.
func objectOriginAlternateCarrier(t *testing.T, source string) delivery.Result {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(source, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var index struct {
		SchemaVersion int                   `json:"schemaVersion"`
		MediaType     string                `json:"mediaType"`
		Manifests     []delivery.Descriptor `json:"manifests"`
	}
	if err := json.Unmarshal(raw, &index); err != nil || len(index.Manifests) != 1 {
		t.Fatalf("carrier index: %v", err)
	}
	original := index.Manifests[0]
	raw, err = os.ReadFile(filepath.Join(source, "blobs", "sha256", strings.TrimPrefix(original.Digest, "sha256:")))
	if err != nil {
		t.Fatal(err)
	}
	// A distinct encoding preserves every admitted carrier field and raw payload.
	// The aggregate must still reject a different actual descriptor.
	var alternate bytes.Buffer
	if err := json.Indent(&alternate, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	raw = alternate.Bytes()
	descriptor := original
	descriptor.Digest = objectIntegrationDigest(raw)
	descriptor.Size = int64(len(raw))
	index.Manifests[0] = descriptor
	out := filepath.Join(t.TempDir(), "alternate-carrier")
	if err := os.MkdirAll(filepath.Join(out, "blobs", "sha256"), 0700); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(source, "blobs", "sha256"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == strings.TrimPrefix(original.Digest, "sha256:") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(source, "blobs", "sha256", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, "blobs", "sha256", entry.Name()), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(out, "blobs", "sha256", strings.TrimPrefix(descriptor.Digest, "sha256:")), raw, 0600); err != nil {
		t.Fatal(err)
	}
	indexRaw, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "index.json"), indexRaw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "oci-layout"), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	return delivery.Result{LayoutPath: out, RootDigest: objectIntegrationDigest(indexRaw)}
}

func TestObjectPublicationRejectsIndependentlyValidDifferentLocalDescriptor(t *testing.T) {
	_, _, verifier, certificate, _ := objectIntegrationSource(t)
	definition, environment := testDefinition(t), testEnvironment()
	definition.Outputs[0].Format = OutputDebianPackage
	plan, err := Compile(t.Context(), "definition.yaml", definition, "environment.yaml", environment, "test")
	if err != nil {
		t.Fatal(err)
	}
	var carrier delivery.Result
	fixture := exactInputFixtureForPlanAndInvocation(t, definition, environment, plan, objectOriginInvocation("1"), func(request *delivery.PublicationRequest) {
		for i := range request.Objects {
			object := &request.Objects[i]
			if object.Output == nil {
				continue
			}
			raw := []byte("exact " + object.Output.Components[0] + " bytes")
			path := filepath.Join(t.TempDir(), "original.deb")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			var err error
			carrier, err = delivery.BuildDebianArtifact(t.Context(), filepath.Join(t.TempDir(), "carrier"), object.Name, path, objectIntegrationDigest(raw))
			if err != nil {
				t.Fatal(err)
			}
			object.Path, object.ArtifactType, object.Kind, object.SemanticDigest, object.BlobDigest = carrier.LayoutPath, delivery.MediaDeliveryObject, factory.PackageDebian, objectIntegrationDigest(raw), carrier.RootDigest
			object.Output.Format = OutputDebianPackage
		}
		request.Release.ContentSubject, err = delivery.ContentDigest([]delivery.Output{{Name: "cli", Kind: OutputDebianPackage, Digest: request.Objects[0].SemanticDigest, BuildDigest: plan.Builds[0].BuildDigest, Components: []string{"kubectl"}}})
		if err != nil {
			t.Fatal(err)
		}
	})
	localA := objectOriginLocal(t, fixture, "cli", "1")
	alternate := objectOriginAlternateCarrier(t, carrier.LayoutPath)
	requestB := fixture.request
	metadata := *fixture.request.Release
	requestB.Release = &metadata
	requestB.Objects = append([]delivery.ObjectInput(nil), fixture.request.Objects...)
	for i := range requestB.Objects {
		if requestB.Objects[i].Output != nil {
			requestB.Objects[i].Path, requestB.Objects[i].BlobDigest = alternate.LayoutPath, alternate.RootDigest
		}
	}
	preparedB := filepath.Join(t.TempDir(), "prepared-B")
	if _, err := delivery.BuildPublicationRoot(t.Context(), preparedB, requestB); err != nil {
		t.Fatal(err)
	}
	pathB, resultB := attestFixtureLayout(t, preparedB)
	inspectionB, err := delivery.Inspect(t.Context(), pathB)
	if err != nil {
		t.Fatal(err)
	}
	localB := objectOriginLayout{path: pathB, reference: "registry.example.test/product/releases-cli@" + resultB.RootDigest, inspection: inspectionB}
	aggregate := objectOriginAggregate(t, fixture, []objectOriginLayout{localA}, map[string]objectOriginLayout{"cli": localB}, "2")
	layouts := []objectOriginLayout{aggregate, localB}
	consumer := objectOriginConsumer(t, verifier, certificate, layouts, map[string]string{aggregate.reference: "2", localB.reference: "1"})
	viewA, err := delivery.MetadataOutputs(localA.inspection.Root)
	if err != nil {
		t.Fatal(err)
	}
	viewB, err := delivery.MetadataOutputs(localB.inspection.Root)
	if err != nil {
		t.Fatal(err)
	}
	if viewA[0].Digest != viewB[0].Digest {
		t.Fatal("fixture changed the raw subject rather than only the valid OCI descriptor")
	}
	var descriptorA, descriptorB string
	for _, object := range localA.inspection.Root.Objects {
		if object.Output != nil {
			descriptorA = object.DescriptorDigest
		}
	}
	for _, object := range localB.inspection.Root.Objects {
		if object.Output != nil {
			descriptorB = object.DescriptorDigest
		}
	}
	if descriptorA == descriptorB {
		t.Fatal("fixture failed to create different valid original descriptors")
	}
	expectation, err := PrepareExactInputs(definition, environment, plan)
	if err != nil {
		t.Fatal(err)
	}
	limits := delivery.MirrorLimits{MaxObjectBytes: 64 << 20, MaxTotalBytes: 256 << 20, MaxObjects: 4096}
	store := &objectIntegrationStore{data: map[string][]byte{}}
	_, err = consumer.MirrorExactInputs(t.Context(), aggregate.reference, objectIntegrationPolicy(), expectation, objectOriginInvocation("2"), store, limits)
	assertCode(t, err, e.CodeVerifyFailed)
	if len(store.data) != 0 {
		t.Fatal("invalid aggregate/local descriptor join performed object writes")
	}
	transport := delivery.MirrorRequest{RootReference: aggregate.reference, Limits: limits}
	var owners []*delivery.Publication
	defer func() {
		for _, owner := range owners {
			_ = owner.Close()
		}
	}()
	for _, layout := range layouts {
		owner, err := delivery.OpenPublication(t.Context(), layout.path, layout.inspection.RootDigest)
		if err != nil {
			t.Fatal(err)
		}
		owners = append(owners, owner)
		transport.Publications = append(transport.Publications, delivery.MirrorPublication{Reference: layout.reference, Publication: owner})
	}
	for reference := range verifier.bundles {
		if reference != aggregate.reference && reference != localB.reference && !strings.HasSuffix(reference, "@"+descriptorA) && !strings.HasSuffix(reference, "@"+descriptorB) {
			continue
		}
		_, proof, err := verifier.VerifyAndRetain(t.Context(), sign.Subject{Ref: reference, Digest: referenceDigest(reference)}, sign.VerifyPolicy{IdentityRegexp: objectIntegrationPolicy().IdentityRegexp, Issuer: objectIntegrationIssuer, RequireAttestation: true})
		if err != nil {
			t.Fatal(err)
		}
		transport.Proofs = append(transport.Proofs, proof)
	}
	locator, err := delivery.MirrorPublications(t.Context(), store, transport)
	if err != nil {
		t.Fatal(err)
	}
	consumer.materialize = func(context.Context, string) (*delivery.Publication, error) {
		t.Fatal("offline P1 verification consulted original source registry")
		return nil, nil
	}
	verified, receipt, err := consumer.VerifyObjectPublication(t.Context(), store, locator, ObjectPublicationVerification{Reference: aggregate.reference, Invocation: objectOriginInvocation("2"), Policy: objectIntegrationPolicy(), Expectation: expectation, Limits: limits})
	assertCode(t, err, e.CodeVerifyFailed)
	if verified != nil || receipt != nil {
		t.Fatal("valid but mismatched local descriptor produced consumer acceptance")
	}
}
