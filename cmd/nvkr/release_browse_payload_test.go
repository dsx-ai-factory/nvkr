// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/runpresenter"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/urfave/cli/v2"
)

func TestReleaseBrowseNeverDownloadsOutputLayers(t *testing.T) {
	// Metadata-only transport and presentation do not branch on architecture;
	// delivery owns both native disk profile round trips.
	fixture := releaseBrowsePayloadFixture(t, "amd64")
	var payloadGets atomic.Int64
	var corruptConfig atomic.Bool
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if fixture.payloads[filepath.Base(r.URL.Path)] && strings.Contains(r.URL.Path, "/blobs/") {
				payloadGets.Add(1)
				http.Error(w, "browsing must not download output payloads", http.StatusForbidden)
				return
			}
			if corruptConfig.Load() && strings.HasSuffix(r.URL.Path, "/blobs/"+fixture.diskConfig) {
				raw := append([]byte(nil), fixture.diskConfigBytes...)
				raw[len(raw)-1] ^= 1
				_, _ = w.Write(raw)
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	transport := delivery.RegistryOptions{Anonymous: true, PlainHTTP: true}
	reference, err := delivery.Push(t.Context(), fixture.layout, strings.TrimPrefix(server.URL, "http://")+"/product/releases", transport)
	if err != nil {
		t.Fatal(err)
	}
	payloadGets.Store(0)
	for _, format := range []string{"text", "json", "mermaid"} {
		for _, output := range []string{"", "tools", "node-image"} {
			t.Run(format+"/"+output, func(t *testing.T) {
				payloadGets.Store(0)
				raw, err := runReleaseBrowse(t, reference, format, output)
				if got := payloadGets.Load(); got != 0 {
					t.Errorf("browse downloaded output layers: %d GETs", got)
				}
				if err != nil {
					t.Fatalf("browsing metadata with inaccessible payloads: %v", err)
				}
				for _, scope := range []string{"metadata inspection", "payloads, signatures and policy not verified"} {
					if !strings.Contains(raw, scope) {
						t.Errorf("%s omits inspection scope %q: %s", format, scope, raw)
					}
				}
				for _, name := range []string{"tools", "node-image"} {
					if strings.Contains(raw, name) != (output == "" || output == name) {
						t.Errorf("%s output filter %q did not preserve %q membership: %s", format, output, name, raw)
					}
				}
				if format == "json" {
					var view struct {
						SelectedOutput string `json:"selectedOutput"`
						TotalOutputs   int    `json:"totalOutputs"`
						delivery.PublicationDescription
					}
					if err := json.Unmarshal([]byte(raw), &view); err != nil {
						t.Fatal(err)
					}
					want := 2
					if output != "" {
						want = 1
					}
					if view.SelectedOutput != output || view.TotalOutputs != 2 || len(view.Outputs) != want || len(view.Evidence) != want {
						t.Fatalf("JSON lost complete or selected membership: %s", raw)
					}
				}
			})
		}
	}
	for _, corrupt := range []bool{false, true} {
		t.Run("TUI/corrupt="+strconv.FormatBool(corrupt), func(t *testing.T) {
			corruptConfig.Store(corrupt)
			defer corruptConfig.Store(false)
			payloadGets.Store(0)
			shows := 0
			var snapshot progress.Snapshot
			services := runPresentationServices{eligible: func(io.Writer) bool { return true }, show: func(_ context.Context, _ runpresenter.Options, source runSnapshotSource, work func(context.Context) error) runpresenter.Result {
				shows++
				snapshot = source.Snapshot()
				if work != nil {
					t.Fatal("read-only browser retained inspection work")
				}
				return runpresenter.Result{}
			}}
			var out bytes.Buffer
			app := releasePresentationTestApp(executeReleaseVerification, &out, services)
			err := app.RunContext(t.Context(), []string{"nvkr", "release", "browse", "--reference", reference, "--output", "tools", "--ui=tui", "--anonymous", "--plain-http"})
			if corrupt {
				assertLifecycleCommandCode(t, err, e.CodeFetchFailed)
				if len(snapshot.Items) != 0 || snapshot.Status != "failed" {
					t.Fatal("TUI hid invalid sibling metadata behind output selection")
				}
			} else if err != nil || len(snapshot.Items) != 2 || snapshot.Status != "complete" {
				t.Fatalf("metadata-only browser failed: %+v, %v", snapshot, err)
			}
			if payloadGets.Load() != 0 || shows != 1 || out.Len() != 0 {
				t.Fatalf("TUI downloaded payloads or changed output: GETs=%d shows=%d stdout=%q", payloadGets.Load(), shows, out.String())
			}
		})
	}
	t.Run("reject corrupt unselected metadata", func(t *testing.T) {
		corruptConfig.Store(true)
		defer corruptConfig.Store(false)
		payloadGets.Store(0)
		raw, err := runReleaseBrowse(t, reference, "json", "tools")
		if cliErrorCode(err) != e.CodeFetchFailed || raw != "" {
			t.Fatalf("filtered browse accepted changed disk metadata or emitted a partial view: %q, %v", raw, err)
		}
		if got := payloadGets.Load(); got != 0 {
			t.Fatalf("metadata rejection downloaded output layers: %d GETs", got)
		}
	})
	t.Run("full publication still requires payloads", func(t *testing.T) {
		payloadGets.Store(0)
		publication, err := delivery.PullPublication(t.Context(), reference, t.TempDir(), transport)
		if publication != nil {
			_ = publication.Close()
		}
		// This control proves payload acquisition and coded failure; registry
		// retry classification has separate transport contract coverage.
		if cliErrorCode(err) == "" || publication != nil || payloadGets.Load() == 0 {
			t.Fatalf("full publication accepted inaccessible payloads: GETs=%d, publication=%v, error=%v", payloadGets.Load(), publication, err)
		}
	})
}

func runReleaseBrowse(t *testing.T, reference, format, output string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	app := &cli.App{Commands: []*cli.Command{cmdRelease()}, Writer: &out}
	args := []string{"nvkr", "release", "browse", "--reference", reference, "--format", format, "--anonymous", "--plain-http", "--workspace", t.TempDir()}
	if output != "" {
		args = append(args, "--output", output)
	}
	err := app.RunContext(t.Context(), args)
	return out.String(), err
}

type browsePayloadFixture struct {
	layout          string
	payloads        map[string]bool
	diskConfig      string
	diskConfigBytes []byte
}

// Synthetic envelope bytes and SBOMs exercise metadata transport and membership;
// they make no claim about disk filesystems, signatures or production evidence.
func releaseBrowsePayloadFixture(t *testing.T, arch string) browsePayloadFixture {
	t.Helper()
	root := t.TempDir()
	build := pipeline.FileDigest([]byte("release browse fixture build"))
	fixture := browsePayloadFixture{payloads: map[string]bool{}}
	var outputs []delivery.Output
	var objects []delivery.ObjectInput
	for _, outputName := range []string{"tools", "node-image"} {
		component, kind, artifactType := "cli", "executable", delivery.MediaBinaryBundle
		if outputName == "node-image" {
			component, kind, artifactType = "disk", "disk-image", delivery.MediaDiskImage
		}
		payload := []byte("opaque " + component + " fixture bytes")
		payloadDigest := pipeline.FileDigest(payload)
		source := filepath.Join(root, component)
		if err := os.WriteFile(source, payload, 0o600); err != nil {
			t.Fatal(err)
		}
		layout := filepath.Join(root, outputName)
		var built delivery.Result
		var err error
		if kind == "disk-image" {
			built, err = delivery.BuildDiskArtifact(t.Context(), layout, delivery.DiskArtifactRequest{
				Path: source, Digest: payloadDigest, Size: int64(len(payload)), Platform: "linux/" + arch,
				Format: "qcow2", Encryption: "none", ArtifactType: artifactType,
			})
		} else {
			built, err = delivery.BuildBinaryBundle(t.Context(), layout, delivery.BinaryBundleRequest{OS: "linux", Arch: arch, Purpose: "tools",
				Files: []delivery.BinaryFileInput{{Name: component, Path: source, InstallPath: "/usr/local/bin/cli", Mode: 0o755}},
			})
		}
		if err != nil {
			t.Fatal(err)
		}
		descriptor, err := delivery.PayloadDescriptor(t.Context(), layout)
		if err != nil {
			t.Fatal(err)
		}
		manifestBytes := readBrowseBlob(t, layout, descriptor.Digest)
		var manifest v1.Manifest
		if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
			t.Fatal(err)
		}
		for _, layer := range manifest.Layers {
			fixture.payloads[layer.Digest.String()] = true
		}
		output := delivery.Output{Name: outputName, Kind: kind, Digest: descriptor.Digest, BuildDigest: build, Components: []string{component}}
		if kind == "disk-image" {
			output.Disk = &delivery.DiskOutput{PayloadDigest: payloadDigest, Producer: "disk-producer", Component: component,
				Format: "qcow2", Encryption: "none", Size: int64(len(payload)), PlanDigest: build, Outcome: "succeeded"}
			fixture.diskConfig = manifest.Config.Digest.String()
			fixture.diskConfigBytes = readBrowseBlob(t, layout, fixture.diskConfig)
		}
		outputs = append(outputs, output)
		objectKind := kind
		if kind == "executable" {
			objectKind = "binary-bundle"
		}
		objects = append(objects, delivery.ObjectInput{Name: outputName, Kind: objectKind, Path: layout, Layout: true,
			ArtifactType: artifactType, SemanticDigest: built.SemanticDigest, BlobDigest: built.RootDigest, OS: "linux", Arch: arch,
			Output: &delivery.OutputBinding{Repository: outputName, Format: kind, BuildDigest: build, Components: output.Components, Disk: output.Disk,
				BuildSubjects: []delivery.BuildSubject{{Component: component, Digest: payloadDigest}}},
		})
		bom, err := sbom.NewSubject(build, component, payloadDigest)
		if err != nil {
			t.Fatal(err)
		}
		bom, err = sbom.ForSubject(bom, payloadDigest, "build-output", payloadDigest)
		if err != nil {
			t.Fatal(err)
		}
		if kind == "disk-image" {
			bom.Metadata.Component.Type = "operating-system"
			bom.Metadata.Component.BOMRef = "artifact:" + strings.TrimPrefix(payloadDigest, "sha256:")
			dependency := "pkg:generic/fixture-installed-package@1"
			bom.Components = []sbom.Component{{Type: "library", Name: "fixture-installed-package", Version: "1", PURL: dependency, BOMRef: dependency}}
			bom.Dependencies = []sbom.Dependency{{Ref: bom.Metadata.Component.BOMRef, DependsOn: []string{dependency}}}
			bom.Metadata.Props = append(bom.Metadata.Props, sbom.Property{Name: "nvkr:dependencyInspection", Value: "disk-reconciled/v1"})
			for _, key := range []string{"nvkr:dependencyScannerDigest", "nvkr:diskInspectionRuntimeDigest", "nvkr:diskInspectorDigest", "nvkr:diskCodecDigest"} {
				bom.Metadata.Props = append(bom.Metadata.Props, sbom.Property{Name: key, Value: build})
			}
			sort.Slice(bom.Metadata.Props, func(i, j int) bool { return bom.Metadata.Props[i].Name < bom.Metadata.Props[j].Name })
		}
		semantic, err := bom.Digest()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := bom.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		objects = append(objects, delivery.ObjectInput{Name: "sbom-" + strings.TrimPrefix(semantic, "sha256:"), Kind: "sbom",
			ArtifactType: "application/vnd.cyclonedx+json", LayerMediaType: "application/vnd.cyclonedx+json", SemanticDigest: semantic, Bytes: raw,
			Evidence: &delivery.EvidenceBinding{Outputs: []string{outputName}, Component: component, Stage: "build-output", SubjectKind: "file", SubjectDigest: payloadDigest},
		})
	}
	content, err := delivery.ContentDigest(outputs)
	if err != nil {
		t.Fatal(err)
	}
	fixture.layout = filepath.Join(root, "release")
	_, err = delivery.BuildPublicationRoot(t.Context(), fixture.layout, delivery.PublicationRequest{
		Release: &delivery.ReleaseMetadata{DefinitionDigest: build, EnvironmentDigest: build, PlanDigest: build,
			InvocationID: "metadata-fixture", ContentSubject: content}, Objects: objects,
	})
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func readBrowseBlob(t *testing.T, layout, digest string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(layout, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:")))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
