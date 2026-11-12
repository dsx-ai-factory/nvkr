// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/releaseview"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/google/go-containerregistry/pkg/registry"
)

func TestCompareReleasesAdmitsBothSidesBeforeIO(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	pinned := strings.TrimPrefix(server.URL, "http://") + "/product/releases@sha256:" + repeat("a", 64)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		request ReleaseComparisonRequest
		code    e.Code
	}{
		{"canceled", canceled, ReleaseComparisonRequest{Before: pinned, After: pinned}, e.CodeTimeout},
		{"unpinned before", t.Context(), ReleaseComparisonRequest{Before: strings.Split(pinned, "@")[0] + ":latest", After: pinned}, e.CodeUnverifiedRef},
		{"unpinned after with local before", t.Context(), ReleaseComparisonRequest{Before: pinned, BeforeLayout: "/must-not-open", After: strings.Split(pinned, "@")[0]}, e.CodeUnverifiedRef},
		{"shared layout", t.Context(), ReleaseComparisonRequest{Before: pinned, After: pinned, Inspection: InspectionOptions{LayoutPath: "/must-not-open"}}, e.CodeUsage},
		{"two local sides with registry options", t.Context(), ReleaseComparisonRequest{Before: pinned, BeforeLayout: "/must-not-open", After: pinned, AfterLayout: "/must-not-open",
			Inspection: InspectionOptions{Registry: delivery.RegistryOptions{Anonymous: true}}}, e.CodeUsage},
		{"negative timeout", t.Context(), ReleaseComparisonRequest{Before: pinned, After: pinned, Inspection: InspectionOptions{Timeout: -time.Second}}, e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.request.Inspection.WorkspaceRoot = filepath.Join(t.TempDir(), "must-not-exist")
			if tc.request.BeforeLayout != "" || tc.request.AfterLayout != "" {
				tc.request.Inspection.WorkspaceRoot = ""
			}
			comparison, err := CompareReleases(tc.ctx, tc.request)
			assertCode(t, err, tc.code)
			if !reflect.DeepEqual(comparison, releaseview.Comparison{}) {
				t.Fatal("rejected comparison returned a partial result")
			}
			if root := tc.request.Inspection.WorkspaceRoot; root != "" {
				if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("rejected comparison touched workspace: %v", err)
				}
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatal("rejected comparison reached the registry")
	}
}

func TestCompareReleasesLocalLayouts(t *testing.T) {
	exported, _, artifacts, _ := exportReleaseFixture(t)
	exportedRoot, err := delivery.Inspect(t.Context(), exported)
	if err != nil {
		t.Fatal(err)
	}
	prepared, preparedDigest := testPreparedPublicationLayout(t)
	exportedRef := "registry.example.test/product/releases@" + exportedRoot.RootDigest
	preparedRef := "registry.example.test/product/releases@" + preparedDigest

	same, err := CompareReleases(t.Context(), ReleaseComparisonRequest{Before: exportedRef, BeforeLayout: exported, After: exportedRef, AfterLayout: exported})
	if err != nil || !same.Equal() || same.Unchanged != len(artifacts) {
		t.Fatalf("identical inventories differ: %+v, %v", same, err)
	}
	different, err := CompareReleases(t.Context(), ReleaseComparisonRequest{Before: preparedRef, BeforeLayout: prepared, After: exportedRef, AfterLayout: exported})
	if err != nil {
		t.Fatal(err)
	}
	if different.Equal() || len(different.Added) != len(artifacts) || len(different.Removed) != 1 || different.Before.RootDigest != preparedDigest ||
		different.After.RootDigest != exportedRoot.RootDigest || different.Before.TotalOutputs != 1 || different.After.TotalOutputs != len(artifacts) {
		t.Fatalf("distinct inventories were not reported as added and removed: %+v", different)
	}
}

func TestCompareReleasesRejectsEitherInvalidSideWithoutResult(t *testing.T) {
	good, _, _, _ := exportReleaseFixture(t)
	goodRoot, err := delivery.Inspect(t.Context(), good)
	if err != nil {
		t.Fatal(err)
	}
	corrupt, _, _, _ := exportReleaseFixture(t)
	corruptRoot, err := delivery.Inspect(t.Context(), corrupt)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range corruptRoot.Root.Objects {
		if object.Evidence != nil {
			path := filepath.Join(corrupt, "blobs", "sha256", strings.TrimPrefix(object.BlobDigest, "sha256:"))
			if err := os.WriteFile(path, []byte("substituted evidence"), 0o600); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	goodRef := "registry.example.test/product/releases@" + goodRoot.RootDigest
	corruptRef := "registry.example.test/product/releases@" + corruptRoot.RootDigest
	for _, request := range []ReleaseComparisonRequest{
		{Before: goodRef, BeforeLayout: good, After: corruptRef, AfterLayout: corrupt},
		{Before: corruptRef, BeforeLayout: corrupt, After: goodRef, AfterLayout: good},
	} {
		comparison, err := CompareReleases(t.Context(), request)
		assertCode(t, err, e.CodeVerifyFailed)
		if !reflect.DeepEqual(comparison, releaseview.Comparison{}) {
			t.Fatal("malformed metadata produced a comparison")
		}
	}
	comparison, err := CompareReleases(t.Context(), ReleaseComparisonRequest{Before: goodRef, BeforeLayout: good, After: goodRef, AfterLayout: good,
		Inspection: InspectionOptions{Timeout: time.Nanosecond}})
	assertCode(t, err, e.CodeTimeout)
	if !reflect.DeepEqual(comparison, releaseview.Comparison{}) {
		t.Fatal("expired comparison produced a result")
	}
}

func TestCompareReleasesRegistryReadsNoPayloadsAndSeparatesReferences(t *testing.T) {
	t.Setenv("NVKR_METADATA_CACHE", "off")
	layout, payloads, artifacts, _ := exportReleaseFixture(t)
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
	host := strings.TrimPrefix(server.URL, "http://")
	before, err := delivery.Push(t.Context(), layout, host+"/product/releases:before", transport)
	if err != nil {
		t.Fatal(err)
	}
	after, err := delivery.Push(t.Context(), layout, host+"/mirror/releases:after", transport)
	if err != nil {
		t.Fatal(err)
	}
	forbid.Store(true)
	comparison, err := CompareReleases(t.Context(), ReleaseComparisonRequest{Before: before, After: after, Inspection: InspectionOptions{Registry: transport}})
	if err != nil || payloadRequests.Load() != 0 {
		t.Fatalf("comparison failed or read payloads: GETs=%d, %v", payloadRequests.Load(), err)
	}
	if len(comparison.Changed) != len(artifacts) || len(comparison.Added) != 0 || len(comparison.Removed) != 0 || comparison.Unchanged != 0 {
		t.Fatalf("mirrored release did not report every reference change: %+v", comparison)
	}
	for _, change := range comparison.Changed {
		if !change.ReferenceOnly || change.Before.Digest != change.After.Digest {
			t.Fatalf("transport-only change was reported as a payload change: %+v", change)
		}
	}
	// A cached local copy of the before side compares with the registry side;
	// registry options apply only to the registry side.
	mixed, err := CompareReleases(t.Context(), ReleaseComparisonRequest{Before: before, BeforeLayout: layout, After: before,
		Inspection: InspectionOptions{Registry: transport}})
	if err != nil || !mixed.Equal() || payloadRequests.Load() != 0 {
		t.Fatalf("local and registry copies of one release differ: %+v, GETs=%d, %v", mixed, payloadRequests.Load(), err)
	}
}

// chartPublicationLayout publishes one Helm output from a minimal chart at the
// given version, so two layouts differ only where a comparison test needs them to.
func chartPublicationLayout(t *testing.T, output, version string) (string, string) {
	t.Helper()
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	chart := "apiVersion: v2\nname: fixture\nversion: " + version + "\n"
	if err := tw.WriteHeader(&tar.Header{Name: "fixture/Chart.yaml", Mode: 0o644, Size: int64(len(chart)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(chart)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	layout := filepath.Join(t.TempDir(), "chart")
	artifact, err := delivery.BuildHelmChartArtifact(t.Context(), layout, "application/vnd.nvkr.helm-bundle.v1", archive.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := delivery.PayloadDescriptor(t.Context(), layout)
	if err != nil {
		t.Fatal(err)
	}
	build := pipeline.FileDigest([]byte("chart build"))
	content, err := delivery.ContentDigest([]delivery.Output{{Name: output, Kind: "helm-oci", Digest: manifest.Digest, BuildDigest: build}})
	if err != nil {
		t.Fatal(err)
	}
	data := archive.Bytes()
	chartPath := filepath.Join(t.TempDir(), "chart.tgz")
	if err := os.WriteFile(chartPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	inventory, err := sbom.InspectDependencies(t.Context(), chartPath, pipeline.FileDigest(data), "tarball", sbom.Scanner{})
	if err != nil {
		t.Fatal(err)
	}
	bom, err := sbom.NewSubject(build, output, manifest.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if bom, err = sbom.WithDependencies(bom, inventory.Components, inventory.Method, inventory.ScannerDigest); err != nil {
		t.Fatal(err)
	}
	if bom, err = sbom.ForSubject(bom, manifest.Digest, "packaging", pipeline.FileDigest(data)); err != nil {
		t.Fatal(err)
	}
	document, err := bom.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	semantic, err := bom.Digest()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "publication")
	result, err := delivery.BuildPublicationRoot(t.Context(), root, delivery.PublicationRequest{
		Release: &delivery.ReleaseMetadata{DefinitionDigest: build, EnvironmentDigest: build, PlanDigest: build, ContentSubject: content},
		Objects: []delivery.ObjectInput{
			{Name: output, Kind: "helm-oci", Path: layout, Layout: true, ArtifactType: "application/vnd.nvkr.helm-bundle.v1",
				SemanticDigest: artifact.SemanticDigest, BlobDigest: artifact.RootDigest,
				Output: &delivery.OutputBinding{Repository: output, Format: "helm-oci", BuildDigest: build}},
			{Name: "sbom-" + strings.TrimPrefix(semantic, "sha256:"), Kind: "sbom", ArtifactType: "application/vnd.cyclonedx+json",
				LayerMediaType: "application/vnd.cyclonedx+json", SemanticDigest: semantic, Bytes: document,
				Evidence: &delivery.EvidenceBinding{Outputs: []string{output}, Component: output, Stage: "packaging", SubjectKind: "helm-bundle",
					SubjectDigest: manifest.Digest, InspectionInputDigest: pipeline.FileDigest(data)}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return root, result.RootDigest
}

// Each case reads both sides from a real local layout through the same
// inspector a registry comparison uses, not from an in-memory projection.
func TestCompareReleasesLocalLayoutsReportEachKindOfChange(t *testing.T) {
	ref := func(repository, digest string) string { return repository + "/product/releases@" + digest }
	changed := func(t *testing.T, comparison releaseview.Comparison, name, field string, referenceOnly bool) {
		t.Helper()
		if len(comparison.Added) != 0 || len(comparison.Removed) != 0 || len(comparison.Changed) != 1 {
			t.Fatalf("want exactly one changed output: %+v", comparison)
		}
		change := comparison.Changed[0]
		if change.Name != name || !slices.Contains(change.Fields, field) || change.ReferenceOnly != referenceOnly {
			t.Fatalf("change %+v, want %s with field %s and referenceOnly=%v", change, name, field, referenceOnly)
		}
	}
	compare := func(t *testing.T, before, beforeLayout, after, afterLayout string) releaseview.Comparison {
		t.Helper()
		comparison, err := CompareReleases(t.Context(), ReleaseComparisonRequest{Before: before, BeforeLayout: beforeLayout, After: after, AfterLayout: afterLayout})
		if err != nil {
			t.Fatal(err)
		}
		return comparison
	}

	t.Run("aggregate image child and platform set", func(t *testing.T) {
		both, bothDigest := indexPublicationLayout(t, "amd64", "arm64")
		one, oneDigest := indexPublicationLayout(t, "amd64")
		comparison := compare(t, ref("registry.example.test", bothDigest), both, ref("registry.example.test", oneDigest), one)
		changed(t, comparison, "cwe", "imageIndex", false)
		if before, after := comparison.Changed[0].Before.ImageIndex, comparison.Changed[0].After.ImageIndex; before == nil || after == nil ||
			len(before.Children) != 2 || len(after.Children) != 1 || len(after.RequiredPlatforms) != 1 {
			t.Fatalf("image index change lost its children: %+v", comparison.Changed[0])
		}
	})
	t.Run("chart version", func(t *testing.T) {
		older, olderDigest := chartPublicationLayout(t, "chart", "1.0.0")
		newer, newerDigest := chartPublicationLayout(t, "chart", "1.1.0")
		comparison := compare(t, ref("registry.example.test", olderDigest), older, ref("registry.example.test", newerDigest), newer)
		changed(t, comparison, "chart", "chartVersion", false)
		if comparison.Changed[0].Before.ChartVersion != "1.0.0" || comparison.Changed[0].After.ChartVersion != "1.1.0" {
			t.Fatalf("chart versions were not carried: %+v", comparison.Changed[0])
		}
	})
	t.Run("equal Build Digests never pair different names", func(t *testing.T) {
		original, originalDigest := chartPublicationLayout(t, "chart", "1.0.0")
		renamed, renamedDigest := chartPublicationLayout(t, "chart-next", "1.0.0")
		comparison := compare(t, ref("registry.example.test", originalDigest), original, ref("registry.example.test", renamedDigest), renamed)
		if len(comparison.Changed) != 0 || len(comparison.Added) != 1 || comparison.Added[0].Name != "chart-next" ||
			len(comparison.Removed) != 1 || comparison.Removed[0].Name != "chart" || comparison.Added[0].BuildDigest != comparison.Removed[0].BuildDigest {
			t.Fatalf("a renamed output with the same Build Digest was paired: %+v", comparison)
		}
	})
	t.Run("transport reference only", func(t *testing.T) {
		layout, digest := chartPublicationLayout(t, "chart", "1.0.0")
		comparison := compare(t, ref("registry.example.test", digest), layout, ref("mirror.example.test", digest), layout)
		changed(t, comparison, "chart", "reference", true)
		if comparison.Changed[0].Before.Digest != comparison.Changed[0].After.Digest {
			t.Fatalf("a reference-only change altered the payload digest: %+v", comparison.Changed[0])
		}
	})
}
