// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/releaseview"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

func TestBundleConsumerAndReleaseExportPreserveArtifactIdentity(t *testing.T) {
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	content := []byte("apiVersion: argoproj.io/v1alpha1\nkind: Application\n")
	if err := tw.WriteHeader(&tar.Header{Name: "applications/runtime.yaml", Mode: 0o644, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "source.tar.gz")
	if err := os.WriteFile(path, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	archiveDigest := pipeline.FileDigest(archive.Bytes())
	bundlePath := filepath.Join(t.TempDir(), "bundle")
	bundle, err := delivery.BuildBundleArtifact(t.Context(), bundlePath, "runtime", path, archiveDigest)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := delivery.PayloadDescriptor(t.Context(), bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	artifact := Artifact{Name: "runtime", Kind: OutputBundle, Digest: descriptor.Digest, BuildDigest: "sha256:" + repeat("5", 64), Components: []string{"renderer"}}
	request := testReleaseRequest(t, []Artifact{artifact}, []delivery.ObjectInput{{Name: artifact.Name, Kind: factory.PackageBundle,
		Path: bundlePath, Layout: true, ArtifactType: delivery.MediaBundle, SemanticDigest: bundle.SemanticDigest, BlobDigest: bundle.RootDigest}}, map[string]string{"renderer": archiveDigest})
	layout := filepath.Join(t.TempDir(), "publication")
	if _, err := delivery.BuildPublicationRoot(t.Context(), layout, request); err != nil {
		t.Fatal(err)
	}
	layout, result := attestFixtureLayout(t, layout)
	reference := "registry.example.test/product@" + result.RootDigest
	raw, err := ExportRelease(t.Context(), reference, InspectionOptions{LayoutPath: layout})
	if err != nil {
		t.Fatal(err)
	}
	var exported releaseview.Description
	if err := json.Unmarshal(raw, &exported); err != nil {
		t.Fatal(err)
	}
	if exported.SchemaVersion != releaseview.DescriptionSchemaVersion || len(exported.Outputs) != 1 || exported.Outputs[0].Kind != OutputBundle || exported.Outputs[0].Digest != descriptor.Digest || exported.Outputs[0].ChartVersion != "" || len(exported.Outputs[0].BuildSubjects) != 1 || exported.Outputs[0].BuildSubjects[0].Digest != archiveDigest {
		t.Fatalf("export changed bundle semantics: %+v", exported.Outputs)
	}
	verifier := &releaseVerifier{record: sign.VerificationRecord{SchemaVersion: sign.VerificationRecordSchemaVersion,
		Ref: reference, Digest: result.RootDigest, Identity: "^builder$", Issuer: "issuer", PredicateType: sign.ProvenancePredicateType,
		SignatureVerified: true, AttestationVerified: true, ObservedIdentity: "builder", ObservedIssuer: "issuer", Ran: [][]string{{"cosign", "verify"}}}}
	consumer := &Consumer{verifier: verifier, materialize: func(ctx context.Context, _ string) (*delivery.Publication, error) {
		return delivery.OpenPublication(ctx, layout, result.RootDigest)
	}}
	verified, err := consumer.Verify(t.Context(), reference, VerificationPolicy{Lane: LanePipeline, Scope: ScopeArtifactPublication, IdentityRegexp: "^builder$", Issuer: "issuer"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = verified.Close() }()
	description, err := verified.Description()
	if err != nil {
		t.Fatal(err)
	}
	if len(description.Artifacts) != 1 || description.Artifacts[0].Kind != OutputBundle || description.Artifacts[0].Digest != descriptor.Digest || description.ContentSubject != request.Release.ContentSubject {
		t.Fatalf("consumer changed bundle binding: %+v", description)
	}
}
