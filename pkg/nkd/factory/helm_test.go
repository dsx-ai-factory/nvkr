// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/transportarchive"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/assemble"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"golang.org/x/sys/unix"
)

func TestSourceHelmArchiveBoundary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers []tar.Header
	}{
		{"multiple roots", []tar.Header{{Name: "helm", Typeflag: tar.TypeDir, Mode: 0o755}, {Name: "other", Typeflag: tar.TypeDir, Mode: 0o755}}},
		{"root is file", []tar.Header{{Name: "helm", Typeflag: tar.TypeReg, Mode: 0o644}}},
		{"escaping symlink", []tar.Header{{Name: "helm", Typeflag: tar.TypeDir, Mode: 0o755}, {Name: "helm/link", Typeflag: tar.TypeSymlink, Linkname: "../../outside"}}},
		{"duplicate directory", []tar.Header{{Name: "helm", Typeflag: tar.TypeDir, Mode: 0o755}, {Name: "helm", Typeflag: tar.TypeDir, Mode: 0o755}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "source.tar")
			file, err := os.Create(archive)
			if err != nil {
				t.Fatal(err)
			}
			writer := tar.NewWriter(file)
			for _, header := range tc.headers {
				if err := writer.WriteHeader(&header); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			root, _, cleanup, err := helmPackageTree(context.Background(), Artifact{ID: "charts", Kind: "tarball", BuildTarget: "charts", Path: archive})
			defer cleanup()
			if root != "" || e.ExitCode(err) != e.ExitCode(e.New(e.CodeVerifyFailed, "expected")) {
				t.Fatalf("unsafe archive accepted: %q %v", root, err)
			}
		})
	}
}

func TestSourceHelmGNUTarDirectory(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "source.tar")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(file)
	if err := writer.WriteHeader(&tar.Header{Name: "helm/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	root, relative, cleanup, err := helmPackageTree(context.Background(), Artifact{ID: "charts", Kind: "tarball", BuildTarget: "charts", Path: archive})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if info, err := os.Stat(filepath.Join(root, relative)); err != nil || !info.IsDir() {
		t.Fatalf("GNU directory missing: %v", err)
	}
}

func TestSourceHelmProducerObservation(t *testing.T) {
	for _, binding := range []bool{false, true} {
		name := "source only"
		if binding {
			name = "bound image"
		}
		t.Run(name, func(t *testing.T) { testSourceHelmProducerObservation(t, binding, false) })
	}
}

func TestSourceHelmProducerObservationAcrossUmasks(t *testing.T) {
	if os.Getenv("NKD_HELM_UMASK_HELPER") == "1" {
		// This is a dedicated subprocess: never change the suite's global umask.
		unix.Umask(0o022)
		testSourceHelmProducerObservation(t, true, true)
		return
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, "-test.run=^TestSourceHelmProducerObservationAcrossUmasks$")
	cmd.Env = append(os.Environ(), "NKD_HELM_UMASK_HELPER=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cross-umask Helm observation: %v\n%s", err, output)
	}
}

func testSourceHelmProducerObservation(t *testing.T, binding, fileOnly bool) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	// An image-bound tree is a wrapper ("helm") one level above the named chart
	// ("controller"); an unbound tree's discovered archive root is the chart
	// itself, since nothing else names which subdirectory would hold it.
	chartDir := "helm"
	if binding && !fileOnly {
		chartDir = filepath.Join("helm", "controller")
	}
	chart := filepath.Join(root, chartDir)
	if err := os.MkdirAll(filepath.Join(chart, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chart, "Chart.yaml"), []byte("apiVersion: v2\nname: controller\nversion: 0.1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chart, "templates", "controller.yaml"), []byte(helmDeploymentFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chart, "values.yaml"), []byte("image:\n  repository: old\n  tag: dev\n  digest: ''\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "source.tar")
	if fileOnly {
		// Real `helm package` output has regular members without directory
		// headers; extraction must assign deterministic implicit parent modes.
		file, err := os.Create(archive)
		if err != nil {
			t.Fatal(err)
		}
		gz := gzip.NewWriter(file)
		writer := tar.NewWriter(gz)
		for _, name := range []string{"Chart.yaml", "values.yaml", "templates/controller.yaml"} {
			raw, err := os.ReadFile(filepath.Join(chart, name))
			if err != nil {
				t.Fatal(err)
			}
			if err := writer.WriteHeader(&tar.Header{Name: "helm/" + name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(raw))}); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write(raw); err != nil {
				t.Fatal(err)
			}
		}
		for _, closer := range []io.Closer{writer, gz, file} {
			if err := closer.Close(); err != nil {
				t.Fatal(err)
			}
		}
	} else if err := transportarchive.Pack(ctx, archive, root, []string{"helm"}); err != nil {
		t.Fatal(err)
	}
	digest, err := digestFactoryPath(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(archive)
	if err != nil {
		t.Fatal(err)
	}
	record := pipeline.ArtifactResult{Component: "charts", OutputType: "tarball", SHA256: digest, Size: info.Size()}
	recordPath := filepath.Join(root, "result.json")
	writeRecord := func() {
		raw, marshalErr := canonical.Marshal(record)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if err := os.WriteFile(recordPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeRecord()
	artifacts := map[string]Artifact{
		"source":         {ID: "source", Name: "charts", Kind: "tarball", Component: "charts", BuildTarget: "charts", Path: archive},
		"result":         {ID: "result", Name: "charts-result", Kind: "producer-result", Component: "charts", BuildTarget: "charts", Path: recordPath},
		"package/charts": {ID: "package/charts", Name: "charts", Kind: PackageHelm, Producer: "package/charts", MediaType: packageMediaType(PackageHelm), Path: filepath.Join(root, "packaged")},
	}
	node := Node{ID: "package/charts", Kind: NodePackage, PackageKind: PackageHelm, Inputs: []string{"source"}, Outputs: []string{"package/charts"}}
	buildDigest := "sha256:" + strings.Repeat("a", 64)
	plan := &Plan{Builds: []BuildPlan{{Name: "charts", BuildDigest: buildDigest, Platform: "linux/amd64",
		Graph: &ir.Graph{Nodes: []ir.Node{{Kind: ir.KindBuild, Component: "charts", OutputType: "tarball"}}}}}}
	if binding {
		binary := filepath.Join(root, "worker")
		if err := os.WriteFile(binary, []byte("fixture worker"), 0o755); err != nil {
			t.Fatal(err)
		}
		imagePath := filepath.Join(root, "image")
		if _, err := assemble.OCIImage(binary, "worker", 1, "amd64", imagePath); err != nil {
			t.Fatal(err)
		}
		artifacts["package/image"] = Artifact{ID: "package/image", Name: "image", Kind: PackageImage, Producer: "package/image", Path: imagePath}
		artifacts["rawimage"] = Artifact{ID: "rawimage", Kind: "oci-image", BuildTarget: "charts", Component: "image"}
		plan.Graph.Nodes = append(plan.Graph.Nodes, Node{ID: "package/image", Kind: NodePackage, PackageKind: PackageImage, Inputs: []string{"rawimage"}, Outputs: []string{"package/image"}})
		node.HelmImage = testHelmBinding("package/image", "controller")
		if fileOnly {
			node.HelmImage.Chart = "."
		}
		node.Inputs = append(node.Inputs, "package/image")
	}
	plan.Graph.Nodes = append(plan.Graph.Nodes, node)
	for _, artifact := range artifacts {
		plan.Graph.Artifacts = append(plan.Graph.Artifacts, artifact)
	}
	var log bytes.Buffer
	if err := executePackage(ctx, plan, node, artifacts, &log); err != nil {
		t.Fatal(err)
	}
	if binding {
		image, err := inspectFactoryImage(ctx, plan, artifacts["package/image"])
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(log.String(), image.ManifestDigest) || !strings.Contains(log.String(), fmt.Sprintf("Bound Helm chart %q to 1 image slots", node.HelmImage.Chart)) {
			t.Fatalf("missing exact binding log: %s", &log)
		}
	} else if strings.Contains(log.String(), "Bound Helm") {
		t.Fatalf("claimed an image binding without an image: %s", &log)
	}
	if binding {
		first, err := digestFactoryPath(ctx, artifacts["package/charts"].Path)
		if err != nil {
			t.Fatal(err)
		}
		originalOutput := artifacts["package/charts"]
		for i, prefix := range []string{"registry.operator-a.test/nke", "mirror.operator-b.test/platform"} {
			alternate := originalOutput
			alternate.Path = filepath.Join(root, fmt.Sprintf("mirror-%d", i))
			artifacts["package/charts"] = alternate
			plan.Environment.Publication = &definition.Publication{RepositoryPrefix: prefix}
			if err := executePackage(ctx, plan, node, artifacts, &log); err != nil {
				t.Fatal(err)
			}
			next, err := digestFactoryPath(ctx, artifacts["package/charts"].Path)
			if err != nil || next != first {
				t.Fatalf("Environment registry changed chart bytes: %s %s %v", first, next, err)
			}
		}
		artifacts["package/charts"] = originalOutput
	}
	expected := LifecycleOutputPlan{Name: "charts", Kind: "helm-oci", ArtifactID: "package/charts", BuildDigest: buildDigest, Components: []string{"charts"}}
	if fileOnly {
		// Reproduce the exact packaged subject in a restrictive host process,
		// unlike the producing container's 022 mask.
		unix.Umask(0o077)
	}
	observed, err := ObserveLifecyclePayload(ctx, plan, expected, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := delivery.PayloadDescriptor(ctx, artifacts["package/charts"].Path)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Digest != inspection.Digest || len(observed.Components) != 1 || observed.Components[0] != "charts" {
		t.Fatalf("observation: %#v", observed)
	}
	if binding {
		plan.Environment.Publication = &definition.Publication{Mode: "per-output-v1"}
		plan.LifecycleOutputs = []LifecycleOutputPlan{expected}
		nodes, _ := planIndex(plan)
		publication := Node{ID: "package/publication-charts", Kind: NodePackage, PackageKind: PackageOCI, Inputs: []string{"package/charts"}}
		publication.Inputs = uniqueSorted(append(publication.Inputs, packageProducerResults(publication.Inputs, nodes, artifacts)...))
		plan.Graph.Nodes = append(plan.Graph.Nodes, publication)
		if _, err := lifecyclePublicationOutputs(ctx, plan, publication, artifacts); err != nil {
			t.Fatalf("output-local chart reproduction lost its bound image: %v", err)
		}
		var inputs []Artifact
		for _, id := range publication.Inputs {
			inputs = append(inputs, artifacts[id])
		}
		objects, cleanup, err := collectPublicationObjects(ctx, plan, Artifact{Producer: publication.ID}, inputs, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		if len(objects) != 1 || objects[0].Name != "charts" {
			t.Fatalf("published reproduction dependencies as sibling outputs: %+v", objects)
		}
	}
	record.SHA256 = "sha256:" + strings.Repeat("b", 64)
	writeRecord()
	_, err = ObserveLifecyclePayload(ctx, plan, expected, artifacts)
	if e.ExitCode(err) != e.ExitCode(e.New(e.CodeVerifyFailed, "expected")) {
		t.Fatalf("substituted producer: %v", err)
	}
}

// A bound wrapper can carry a sibling chart alongside the one image-bound
// chart. Silently publishing only the bound chart would drop the sibling from
// the release with no signal; packaging must fail closed instead, naming what
// it would have dropped.
func TestHelmPackageFailsClosedOnUnpublishedSiblingContent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	chart := filepath.Join(root, "helm", "controller")
	if err := os.MkdirAll(filepath.Join(chart, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chart, "Chart.yaml"), []byte("apiVersion: v2\nname: controller\nversion: 0.1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chart, "templates", "controller.yaml"), []byte(helmDeploymentFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chart, "values.yaml"), []byte("image:\n  repository: old\n  tag: dev\n  digest: ''\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A sibling chart alongside the bound one, exactly like NKE's real wrapper
	// (007-controller plus 002-postgresql, 004-spire-crds-post).
	sibling := filepath.Join(root, "helm", "sidecar")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "Chart.yaml"), []byte("apiVersion: v2\nname: sidecar\nversion: 0.1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "source.tar")
	if err := transportarchive.Pack(ctx, archive, root, []string{"helm"}); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "worker")
	if err := os.WriteFile(binary, []byte("fixture worker"), 0o755); err != nil {
		t.Fatal(err)
	}
	imagePath := filepath.Join(root, "image")
	if _, err := assemble.OCIImage(binary, "worker", 1, "amd64", imagePath); err != nil {
		t.Fatal(err)
	}
	artifacts := map[string]Artifact{
		"source":         {ID: "source", Name: "charts", Kind: "tarball", Component: "charts", BuildTarget: "charts", Path: archive},
		"package/charts": {ID: "package/charts", Name: "charts", Kind: PackageHelm, Producer: "package/charts", MediaType: packageMediaType(PackageHelm), Path: filepath.Join(root, "packaged")},
		"package/image":  {ID: "package/image", Name: "image", Kind: PackageImage, Producer: "package/image", Path: imagePath},
		"rawimage":       {ID: "rawimage", Kind: "oci-image", BuildTarget: "charts", Component: "image"},
	}
	node := Node{ID: "package/charts", Kind: NodePackage, PackageKind: PackageHelm,
		Inputs: []string{"source", "package/image"}, Outputs: []string{"package/charts"},
		HelmImage: testHelmBinding("package/image", "controller")}
	buildDigest := "sha256:" + strings.Repeat("a", 64)
	plan := &Plan{Builds: []BuildPlan{{Name: "charts", BuildDigest: buildDigest, Platform: "linux/amd64",
		Graph: &ir.Graph{Nodes: []ir.Node{{Kind: ir.KindBuild, Component: "charts", OutputType: "tarball"}}}}}}
	plan.Graph.Nodes = append(plan.Graph.Nodes, Node{ID: "package/image", Kind: NodePackage, PackageKind: PackageImage, Inputs: []string{"rawimage"}, Outputs: []string{"package/image"}}, node)
	for _, artifact := range artifacts {
		plan.Graph.Artifacts = append(plan.Graph.Artifacts, artifact)
	}
	err := executePackage(ctx, plan, node, artifacts, io.Discard)
	if e.ExitCode(err) != e.ExitCode(e.New(e.CodeInvalidCatalog, "expected")) {
		t.Fatalf("expected a fail-closed error naming the dropped sibling, got %v", err)
	}
	if !strings.Contains(err.Error(), "sidecar") {
		t.Fatalf("error does not name the dropped sibling: %v", err)
	}
}

// An unbound source archive's chart must live at the discovered archive root
// itself; a chart nested one level deeper (with nothing naming that deeper
// path, since there is no HelmImage binding) must fail closed rather than
// silently package an empty or wrong-rooted chart.
func TestSourceHelmArchiveRequiresChartAtDiscoveredRootWhenUnbound(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	nested := filepath.Join(root, "helm", "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "Chart.yaml"), []byte("apiVersion: v2\nname: nested\nversion: 0.1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "source.tar")
	if err := transportarchive.Pack(ctx, archive, root, []string{"helm"}); err != nil {
		t.Fatal(err)
	}
	artifacts := map[string]Artifact{
		"source":         {ID: "source", Name: "charts", Kind: "tarball", Component: "charts", BuildTarget: "charts", Path: archive},
		"package/charts": {ID: "package/charts", Name: "charts", Kind: PackageHelm, Producer: "package/charts", MediaType: packageMediaType(PackageHelm), Path: filepath.Join(root, "packaged")},
	}
	node := Node{ID: "package/charts", Kind: NodePackage, PackageKind: PackageHelm, Inputs: []string{"source"}, Outputs: []string{"package/charts"}}
	plan := &Plan{Graph: ArtifactGraph{Nodes: []Node{node}, Artifacts: []Artifact{artifacts["source"], artifacts["package/charts"]}}}
	err := executePackage(ctx, plan, node, artifacts, io.Discard)
	if e.ExitCode(err) != e.ExitCode(e.New(e.CodeVerifyFailed, "expected")) {
		t.Fatalf("expected a fail-closed error, got %v", err)
	}
}
