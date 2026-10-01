package factory

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/google/go-containerregistry/pkg/v1/layout"
)

func acquiredChartBytes(t *testing.T, invalid bool) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	// Deliberately preserve non-normalized upstream gzip and tar metadata.
	gz.Name, gz.Comment, gz.ModTime = "upstream-package", "must survive packaging", time.Unix(1234567890, 0)
	tw := tar.NewWriter(gz)
	entries := []struct{ name, body string }{
		{"demo/Chart.yaml", "apiVersion: v2\nname: demo\nversion: 1.2.3\n"},
		{"demo/charts/sub/templates/item.yaml", "upstream bytes\n"},
		{"demo/charts/sub/templates/item.yaml", "upstream bytes\n"},
	}
	if invalid {
		entries = append(entries, struct{ name, body string }{"demo/../escape", "bad"})
	}
	for _, entry := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: entry.name, Size: int64(len(entry.body)), Mode: 0640, ModTime: time.Unix(1234567890, 0)}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func acquiredHelmPlan(t *testing.T, body []byte, oci bool) *Plan {
	t.Helper()
	digest := pipeline.FileDigest(body)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/":
			w.WriteHeader(http.StatusOK)
		case "/demo.tgz", "/v2/charts/demo/blobs/" + digest:
			_, _ = w.Write(body)
		default:
			t.Errorf("unexpected acquisition request: %s", r.URL)
			http.NotFound(w, r)
		}
	})
	prebuilt := &catalog.Prebuilt{Digests: map[string]string{"amd64": digest}, Evidence: &catalog.ExternalEvidence{None: true}}
	if oci {
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		prebuilt.OCIArchive = strings.TrimPrefix(server.URL, "http://") + "/charts/demo@" + digest
	} else {
		server := httptest.NewTLSServer(handler)
		t.Cleanup(server.Close)
		original := http.DefaultTransport
		http.DefaultTransport = server.Client().Transport
		t.Cleanup(func() { http.DefaultTransport = original })
		prebuilt.URL = server.URL + "/demo.tgz"
	}
	stack, err := catalog.ApplyStack(&catalog.Catalog{Components: map[string]catalog.Component{"chart": {Platforms: []string{"linux/amd64"}, Tag: "1.2.3", OutputType: "raw", Prebuilt: prebuilt}}})
	if err != nil {
		t.Fatal(err)
	}
	definition := authored.Definition{APIVersion: authored.DefinitionAPIVersion, Kind: authored.DefinitionKind, Metadata: authored.Metadata{Name: "acquired-helm"}, Catalog: authored.Catalog{Base: "catalog"}, Outputs: []authored.DefinitionOutput{{Name: "charts", Platform: "linux/amd64", Format: authored.OutputHelm, Components: []string{"chart"}}}}
	environment := authored.Environment{APIVersion: authored.EnvironmentAPIVersion, Kind: authored.EnvironmentKind, Metadata: authored.Metadata{Name: "local"}, UtilityContainer: fixtureUtilityContainer, UtilityPlatform: "linux/amd64"}
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestAcquiredHelmPackagesExactPinnedBytes(t *testing.T) {
	for _, oci := range []bool{false, true} {
		t.Run(map[bool]string{false: "HTTPS", true: "OCI"}[oci], func(t *testing.T) {
			t.Chdir(t.TempDir())
			body := acquiredChartBytes(t, false)
			plan := acquiredHelmPlan(t, body, oci)
			for _, node := range topologicalFactoryNodes(plan.Graph) {
				if err := ExecuteNode(t.Context(), plan, node.ID, io.Discard, io.Discard); err != nil {
					t.Fatalf("%s: %v", node.ID, err)
				}
			}
			_, artifacts := planIndex(plan)
			output := artifacts["package/charts"]
			imageLayout, err := layout.FromPath(output.Path)
			if err != nil {
				t.Fatal(err)
			}
			index, err := imageLayout.ImageIndex()
			if err != nil {
				t.Fatal(err)
			}
			im, err := index.IndexManifest()
			if err != nil {
				t.Fatal(err)
			}
			image, err := imageLayout.Image(im.Manifests[0].Digest)
			if err != nil {
				t.Fatal(err)
			}
			layers, err := image.Layers()
			if err != nil || len(layers) != 1 {
				t.Fatalf("layers: %v %v", layers, err)
			}
			digest, err := layers[0].Digest()
			if err != nil || digest.String() != pipeline.FileDigest(body) {
				t.Fatalf("changed pin: %s %v", digest, err)
			}
			reader, err := layers[0].Compressed()
			if err != nil {
				t.Fatal(err)
			}
			actual, err := io.ReadAll(reader)
			_ = reader.Close()
			if err != nil || !bytes.Equal(actual, body) {
				t.Fatal("upstream bytes were repackaged")
			}
			observed, err := ObserveLifecyclePayload(t.Context(), plan, plan.LifecycleOutputs[0], artifacts)
			if err != nil || observed.Digest != im.Manifests[0].Digest.String() {
				t.Fatalf("observation: %+v %v", observed, err)
			}
			record, err := readProducerResult(t.Context(), artifacts["result/charts/chart"].Path)
			if err != nil || record.EngineClass != "prebuilt-acquire" || record.SHA256 != digest.String() {
				t.Fatalf("acquisition identity lost: %+v %v", record, err)
			}
		})
	}
}

func TestAcquiredHelmRejectsTamperingBeforePackageOutput(t *testing.T) {
	for _, tc := range []struct {
		name    string
		invalid bool
		mutate  func(*testing.T, map[string]Artifact)
		code    e.Code
	}{
		{name: "unsafe archive", invalid: true, code: e.CodeVerifyFailed},
		{name: "changed payload", mutate: func(t *testing.T, a map[string]Artifact) {
			if err := os.WriteFile(a["artifact/charts/chart"].Path, []byte("changed"), 0600); err != nil {
				t.Fatal(err)
			}
		}, code: e.CodeVerifyFailed},
		{name: "changed result", mutate: func(t *testing.T, a map[string]Artifact) {
			p := a["result/charts/chart"].Path
			r, err := readProducerResult(t.Context(), p)
			if err != nil {
				t.Fatal(err)
			}
			r.SHA256 = "sha256:" + strings.Repeat("b", 64)
			raw, err := canonical.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, raw, 0600); err != nil {
				t.Fatal(err)
			}
		}, code: e.CodeVerifyFailed},
		{name: "symlink payload", mutate: func(t *testing.T, a map[string]Artifact) {
			p := a["artifact/charts/chart"].Path
			if err := os.Rename(p, p+".saved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Base(p)+".saved", p); err != nil {
				t.Fatal(err)
			}
		}, code: e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			plan := acquiredHelmPlan(t, acquiredChartBytes(t, tc.invalid), false)
			if err := ExecuteNode(t.Context(), plan, "charts/pull-chart", nil, nil); err != nil {
				t.Fatal(err)
			}
			_, artifacts := planIndex(plan)
			if tc.mutate != nil {
				tc.mutate(t, artifacts)
			}
			err := ExecuteNode(t.Context(), plan, "package/charts", nil, nil)
			assertFactoryErrorCode(t, err, tc.code)
			if _, err := os.Stat(artifacts["package/charts"].Path); !os.IsNotExist(err) {
				t.Fatalf("failed package exposed output: %v", err)
			}
		})
	}
}

func TestAcquiredHelmCancellationBeforeOutput(t *testing.T) {
	t.Chdir(t.TempDir())
	plan := acquiredHelmPlan(t, acquiredChartBytes(t, false), false)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assertFactoryErrorCode(t, ExecuteNode(ctx, plan, "package/charts", nil, nil), e.CodeTimeout)
	if _, err := os.Stat(".nkd"); !os.IsNotExist(err) {
		t.Fatalf("cancellation touched workspace: %v", err)
	}
}

func TestAcquiredHelmCompilerAndTransportAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Plan)
	}{
		{"raw source build", func(p *Plan) {
			for i := range p.Builds[0].Graph.Nodes {
				n := &p.Builds[0].Graph.Nodes[i]
				if n.Component == "chart" {
					n.Kind = "build"
				}
			}
		}},
		{"acquired tarball cannot be repacked", func(p *Plan) {
			for i := range p.Builds[0].Graph.Nodes {
				n := &p.Builds[0].Graph.Nodes[i]
				if n.Component == "chart" {
					n.OutputType = "tarball"
				}
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := acquiredHelmPlan(t, acquiredChartBytes(t, false), false)
			tc.mutate(plan)
			specs, _, _, err := derivePublication(plan.Definition, plan.Environment)
			if err != nil {
				t.Fatal(err)
			}
			_, err = compileArtifactGraph(*plan, nil, specs)
			assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
		})
	}
	t.Run("missing result transport", func(t *testing.T) {
		plan := acquiredHelmPlan(t, acquiredChartBytes(t, false), false)
		for i := range plan.Graph.Nodes {
			node := &plan.Graph.Nodes[i]
			if node.ID == "package/charts" {
				var inputs []string
				for _, id := range node.Inputs {
					if id != "result/charts/chart" {
						inputs = append(inputs, id)
					}
				}
				node.Inputs = inputs
			}
		}
		assertFactoryErrorCode(t, plan.Validate(), e.CodePipelineDrift)
	})
	t.Run("image rebinding", func(t *testing.T) {
		artifacts := map[string]Artifact{"chart": {Kind: "raw", BuildTarget: "charts"}, "image": {Kind: PackageImage}}
		err := validatePackageInputs(packageSpec{Kind: PackageHelm, HelmImage: &HelmImageBinding{Image: "image", Chart: "demo"}}, []string{"chart", "image"}, artifacts)
		assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
	})
}
