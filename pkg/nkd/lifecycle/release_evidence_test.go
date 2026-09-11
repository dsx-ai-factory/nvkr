package lifecycle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

func TestReleaseConstructionRejectsIncompleteOrSubstitutedEvidence(t *testing.T) {
	source := filepath.Join(t.TempDir(), "cli")
	payload := []byte("exact executable")
	if err := os.WriteFile(source, payload, 0755); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(t.TempDir(), "bundle")
	bundle, err := delivery.BuildBinaryBundle(t.Context(), bundlePath, delivery.BinaryBundleRequest{OS: "linux", Arch: "amd64", Purpose: "cli", Files: []delivery.BinaryFileInput{{Name: "cli", Path: source, InstallPath: "/usr/local/bin/cli", Mode: 0755}}})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := delivery.PayloadDescriptor(t.Context(), bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	request := testReleaseRequest(t, []Artifact{{Name: "cli", Kind: OutputExecutable, Digest: descriptor.Digest, RecipeDigest: "sha256:" + repeat("a", 64), Components: []string{"cli"}}}, []delivery.ObjectInput{{Name: "cli", Kind: factory.PackageBinaryBundle, Path: bundlePath, Layout: true, ArtifactType: delivery.MediaBinaryBundle, SemanticDigest: bundle.SemanticDigest, BlobDigest: bundle.RootDigest}}, map[string]string{"cli": pipeline.FileDigest(payload)})
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*delivery.PublicationRequest)
		code   e.Code
	}{
		{"missing", func(r *delivery.PublicationRequest) { r.Objects = r.Objects[:1] }, e.CodeProvenanceMissing},
		{"substituted document", func(r *delivery.PublicationRequest) { r.Objects[1].Bytes = []byte(`{}`) }, e.CodeVerifyFailed},
		{"duplicate", func(r *delivery.PublicationRequest) { r.Objects = append(r.Objects, r.Objects[1]) }, e.CodeInvalidCatalog},
		{"unbound", func(r *delivery.PublicationRequest) { r.Objects[1].Evidence = nil }, e.CodeVerifyFailed},
		{"wrong stage", func(r *delivery.PublicationRequest) { r.Objects[1].Evidence.Stage = "source-build" }, e.CodeVerifyFailed},
		{"wrong subject", func(r *delivery.PublicationRequest) {
			r.Objects[1].Evidence.SubjectDigest = "sha256:" + repeat("b", 64)
		}, e.CodeVerifyFailed},
		{"wrong input", func(r *delivery.PublicationRequest) {
			r.Objects[1].Evidence.InspectionInputDigest = "sha256:" + repeat("b", 64)
		}, e.CodeVerifyFailed},
		{"extra membership", func(r *delivery.PublicationRequest) {
			r.Objects[1].Evidence.Outputs = append(r.Objects[1].Evidence.Outputs, "other")
		}, e.CodeVerifyFailed},
		{"duplicate membership", func(r *delivery.PublicationRequest) {
			r.Objects[1].Evidence.Outputs = append(r.Objects[1].Evidence.Outputs, "cli")
		}, e.CodeVerifyFailed},
		{"omitted build subject", func(r *delivery.PublicationRequest) { r.Objects[0].Output.BuildSubjects = nil }, e.CodeVerifyFailed},
		{"omitted components", func(r *delivery.PublicationRequest) {
			r.Objects[0].Output.Components = nil
			r.Objects[0].Output.BuildSubjects = nil
			r.Objects = r.Objects[:1]
		}, e.CodeProvenanceMissing},
		{"external host", func(r *delivery.PublicationRequest) { r.Objects[0].Output.Repository = "evil.example/cli" }, e.CodeVerifyFailed},
		{"traversal repository", func(r *delivery.PublicationRequest) { r.Objects[0].Output.Repository = "../cli" }, e.CodeVerifyFailed},
		{"mutable reference", func(r *delivery.PublicationRequest) { r.Objects[0].Output.Repository = "cli:latest" }, e.CodeVerifyFailed},
		{"missing repository", func(r *delivery.PublicationRequest) { r.Objects[0].Output.Repository = "" }, e.CodeVerifyFailed},
		{"unsupported format", func(r *delivery.PublicationRequest) { r.Objects[0].Output.Format = "unknown" }, e.CodeVerifyFailed},
		{"old lifecycle wrapper", func(r *delivery.PublicationRequest) {
			r.Objects[1].Evidence = nil
			r.Objects[1].Kind = "lifecycle-binding"
		}, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var changed delivery.PublicationRequest
			if err := json.Unmarshal(encoded, &changed); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&changed)
			out := filepath.Join(t.TempDir(), "release")
			_, err := delivery.BuildPublicationRoot(t.Context(), out, changed)
			assertCode(t, err, tc.code)
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatalf("rejected publication left output: %v", err)
			}
		})
	}
}

func TestConsumerRejectsDuplicateOCIEvidenceDescriptor(t *testing.T) {
	layout, _ := testPublicationLayout(t)
	path := filepath.Join(layout, "index.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var index map[string]any
	if err := json.Unmarshal(raw, &index); err != nil {
		t.Fatal(err)
	}
	descriptors := index["manifests"].([]any)
	inspection, err := delivery.Inspect(t.Context(), layout)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range inspection.Root.Objects {
		if object.Evidence != nil {
			index["manifests"] = append(descriptors, map[string]any{"mediaType": delivery.MediaOCIManifest, "artifactType": object.ArtifactType, "digest": object.DescriptorDigest, "size": object.Size})
			break
		}
	}
	raw, err = json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = delivery.Inspect(t.Context(), layout)
	assertCode(t, err, e.CodeVerifyFailed)
}
