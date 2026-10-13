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
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/google/go-containerregistry/pkg/registry"
)

func TestExportReleaseAdmissionPrecedesIO(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	reference := strings.TrimPrefix(server.URL, "http://") + "/product/releases@sha256:" + repeat("a", 64)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		ref     string
		options InspectionOptions
		code    e.Code
	}{
		{"nil context", nil, reference, InspectionOptions{}, e.CodeUsage},
		{"canceled", canceled, reference, InspectionOptions{}, e.CodeTimeout},
		{"empty reference", t.Context(), "", InspectionOptions{}, e.CodeUnverifiedRef},
		{"tag", t.Context(), strings.Split(reference, "@")[0] + ":latest", InspectionOptions{}, e.CodeUnverifiedRef},
		{"invalid digest", t.Context(), strings.Split(reference, "@")[0] + "@sha256:bad", InspectionOptions{}, e.CodeUnverifiedRef},
		{"negative timeout", t.Context(), reference, InspectionOptions{Timeout: -time.Second}, e.CodeUsage},
		{"partial credential", t.Context(), reference, InspectionOptions{Registry: delivery.RegistryOptions{Username: "fixture"}}, e.CodeUsage},
		{"local with registry options", t.Context(), reference, InspectionOptions{LayoutPath: "/must-not-open", Registry: delivery.RegistryOptions{Anonymous: true}}, e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.options.WorkspaceRoot = filepath.Join(t.TempDir(), "must-not-exist")
			raw, err := ExportRelease(tc.ctx, tc.ref, tc.options)
			assertCode(t, err, tc.code)
			if len(raw) != 0 {
				t.Fatal("failed export returned partial output")
			}
			if _, err := os.Stat(tc.options.WorkspaceRoot); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected export touched workspace: %v", err)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatal("rejected export reached registry")
	}
}

type exportedReleaseFixture struct {
	SchemaVersion string `json:"schemaVersion"`
	Inspection    string `json:"inspection"`
	TotalOutputs  int    `json:"totalOutputs"`
	delivery.PublicationDescription
}

func TestExportReleasePreservesCompletePublishedMetadataWithoutPayloadReads(t *testing.T) {
	layout, payloads, artifacts, sources := exportReleaseFixture(t)
	var forbid atomic.Bool
	var payloadRequests atomic.Int64
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if forbid.Load() && payloads[r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]] {
			payloadRequests.Add(1)
			http.Error(w, "artifact payload reads forbidden", http.StatusForbidden)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	transport := delivery.RegistryOptions{Anonymous: true, PlainHTTP: true}
	reference, err := delivery.Push(t.Context(), layout, strings.TrimPrefix(server.URL, "http://")+"/product/releases:fixture", transport)
	if err != nil {
		t.Fatal(err)
	}
	forbid.Store(true)
	// Export has no access to a producer checkout, Plan, mapping or workspace.
	t.Chdir(t.TempDir())
	options := InspectionOptions{Registry: transport}
	raw, err := ExportRelease(t.Context(), reference, options)
	if err != nil || payloadRequests.Load() != 0 {
		t.Fatalf("metadata export requested artifact payloads or failed: %v", err)
	}
	var exported exportedReleaseFixture
	if err := json.Unmarshal(raw, &exported); err != nil {
		t.Fatal(err)
	}
	if exported.SchemaVersion != "nvkr.dev/release-description/v1" || exported.Reference != reference ||
		exported.TotalOutputs != len(artifacts) || len(exported.Outputs) != len(artifacts) || len(exported.Evidence) != len(artifacts) {
		t.Fatalf("incomplete versioned release description: %s", raw)
	}
	if !strings.Contains(exported.Inspection, "payloads, signatures and policy not verified") || exported.DeclaredQualification != delivery.QualificationDiagnostic {
		t.Fatalf("export overstated inspection or qualification: %s", raw)
	}
	for i, output := range exported.Outputs {
		want := artifacts[i]
		if output.Name != want.Name || output.Digest != want.Digest || output.OS != want.OS || output.Arch != want.Arch ||
			!reflect.DeepEqual(output.Components, want.Components) || !strings.HasSuffix(output.Reference, "@"+want.Digest) {
			t.Fatalf("named output was filtered, relabeled or changed: %+v, want %+v", output, want)
		}
	}
	for _, evidence := range exported.Evidence {
		if len(evidence.Binding.Outputs) != 1 {
			t.Fatalf("lost exact evidence membership: %+v", evidence)
		}
		name := evidence.Binding.Outputs[0]
		if !reflect.DeepEqual(evidence.Source, sources[name]) {
			t.Fatalf("source metadata for %q was collapsed or invented: %+v, want %+v", name, evidence.Source, sources[name])
		}
	}
	// Repeated export and a cached complete publication have identical bytes.
	for _, inspection := range []InspectionOptions{options, {LayoutPath: layout}} {
		again, err := ExportRelease(t.Context(), reference, inspection)
		if err != nil || !bytes.Equal(raw, again) {
			t.Fatalf("nondeterministic or transport-dependent export: %v", err)
		}
	}
	if payloadRequests.Load() != 0 {
		t.Fatal("repeated export requested artifact payloads")
	}
}

func TestExportReleaseDoesNotRequireSourceMetadata(t *testing.T) {
	layout, digest := testPreparedPublicationLayout(t)
	reference := "registry.example.test/product/releases@" + digest
	raw, err := ExportRelease(t.Context(), reference, InspectionOptions{LayoutPath: layout})
	if err != nil {
		t.Fatal(err)
	}
	var exported exportedReleaseFixture
	if err := json.Unmarshal(raw, &exported); err != nil {
		t.Fatal(err)
	}
	if len(exported.Outputs) != 1 || len(exported.Evidence) != 1 || exported.Evidence[0].Source != nil {
		t.Fatalf("source-free publication was filtered or given a source: %s", raw)
	}
}

func TestExportReleaseDeadlineRejectsCachedPublicationWithoutPartialOutput(t *testing.T) {
	layout, digest := testPreparedPublicationLayout(t)
	reference := "registry.example.test/product/releases@" + digest
	raw, err := ExportRelease(t.Context(), reference, InspectionOptions{LayoutPath: layout, Timeout: time.Nanosecond})
	assertCode(t, err, e.CodeTimeout)
	if len(raw) != 0 {
		t.Fatal("expired export returned a partial description")
	}
}

func TestExportReleaseRejectsCorruptEvidenceWithoutPartialOutput(t *testing.T) {
	layout, _, _, _ := exportReleaseFixture(t)
	inspection, err := delivery.Inspect(t.Context(), layout)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range inspection.Root.Objects {
		if object.Evidence == nil {
			continue
		}
		path := filepath.Join(layout, "blobs", "sha256", strings.TrimPrefix(object.BlobDigest, "sha256:"))
		if err := os.WriteFile(path, []byte("substituted evidence"), 0o600); err != nil {
			t.Fatal(err)
		}
		break
	}
	raw, err := ExportRelease(t.Context(), "registry.example.test/product/releases@"+inspection.RootDigest, InspectionOptions{LayoutPath: layout})
	assertCode(t, err, e.CodeVerifyFailed)
	if len(raw) != 0 {
		t.Fatal("corrupt evidence returned a partial description")
	}
}

// Build real digest-bound artifacts and evidence with deliberately unrelated
// names. Different revisions of one repository and different repositories are
// valid release members; an acquired component need not have source properties.
func exportReleaseFixture(t *testing.T) (string, map[string]bool, []Artifact, map[string]*delivery.PublicationSource) {
	t.Helper()
	root := t.TempDir()
	var artifacts []Artifact
	var objects []delivery.ObjectInput
	subjects := map[string]string{}
	payloads := map[string]bool{}
	sources := map[string]*delivery.PublicationSource{}
	for i, spec := range []struct{ name, arch, repository, commit string }{
		{"alpha", "amd64", "https://example.test/one", repeat("a", 40)},
		{"beta", "arm64", "https://example.test/one", repeat("b", 40)},
		{"gamma", "amd64", "https://example.test/two", repeat("c", 40)},
		{"omega", "arm64", "", ""},
	} {
		payload := []byte("synthetic binary: " + spec.name)
		source := filepath.Join(root, spec.name)
		if err := os.WriteFile(source, payload, 0o755); err != nil {
			t.Fatal(err)
		}
		bundlePath := filepath.Join(root, spec.name+"-bundle")
		bundle, err := delivery.BuildBinaryBundle(t.Context(), bundlePath, delivery.BinaryBundleRequest{
			OS: "linux", Arch: spec.arch, Purpose: "tools", Files: []delivery.BinaryFileInput{
				{Name: spec.name, Path: source, InstallPath: "/usr/local/bin/" + spec.name, Mode: 0o755},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		descriptor, err := delivery.PayloadDescriptor(t.Context(), bundlePath)
		if err != nil {
			t.Fatal(err)
		}
		artifacts = append(artifacts, Artifact{Name: spec.name, Kind: OutputExecutable, Digest: descriptor.Digest,
			BuildDigest: "sha256:" + repeat(string(rune('a'+i)), 64), Components: []string{spec.name}, OS: "linux", Arch: spec.arch})
		objects = append(objects, delivery.ObjectInput{Name: spec.name, Kind: factory.PackageBinaryBundle, Path: bundlePath,
			Layout: true, ArtifactType: delivery.MediaBinaryBundle, SemanticDigest: bundle.SemanticDigest, BlobDigest: bundle.RootDigest})
		subjects[spec.name] = pipeline.FileDigest(payload)
		if spec.repository != "" {
			sources[spec.name] = &delivery.PublicationSource{Repo: spec.repository, Commit: spec.commit}
		}
		var manifest struct {
			Layers []struct {
				Digest string `json:"digest"`
			} `json:"layers"`
		}
		raw, err := os.ReadFile(filepath.Join(bundlePath, "blobs", "sha256", strings.TrimPrefix(descriptor.Digest, "sha256:")))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &manifest); err != nil {
			t.Fatal(err)
		}
		for _, layer := range manifest.Layers {
			payloads[layer.Digest] = true
		}
	}
	request := testReleaseRequest(t, artifacts, objects, subjects)
	for i := range request.Objects {
		object := &request.Objects[i]
		if object.Evidence == nil {
			continue
		}
		source := sources[object.Evidence.Outputs[0]]
		if source == nil {
			continue
		}
		var bom sbom.BOM
		if err := json.Unmarshal(object.Bytes, &bom); err != nil {
			t.Fatal(err)
		}
		bom.Metadata.Component.Properties = []sbom.Property{
			{Name: "nvkr:commit", Value: source.Commit},
			{Name: "nvkr:fromSource", Value: "true"},
			{Name: "nvkr:repo", Value: source.Repo},
		}
		var err error
		object.Bytes, err = bom.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		object.SemanticDigest, err = bom.Digest()
		if err != nil {
			t.Fatal(err)
		}
		object.Name = "sbom-" + strings.TrimPrefix(object.SemanticDigest, "sha256:")
	}
	layout := filepath.Join(root, "publication")
	if _, err := delivery.BuildPublicationRoot(t.Context(), layout, request); err != nil {
		t.Fatal(err)
	}
	return layout, payloads, artifacts, sources
}
