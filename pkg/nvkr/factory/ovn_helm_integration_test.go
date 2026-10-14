// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

// This file checks NVIDIA's supplied OVN chart against generic Helm packaging.
// Component-specific Go tests are not a requirement for adding a catalog.

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"go.yaml.in/yaml/v3"
)

// This opt-in test checks the actual fork templates against a controlled digest.
// Its loopback chart push/pull is interoperability, not GHCR publication,
// an image build, or an authenticated Release test.
func TestOVNForkHelmDigestRendering(t *testing.T) {
	source := os.Getenv("NVKR_OVN_CHART_DIRECTORY")
	if source == "" {
		t.Skip("set NVKR_OVN_CHART_DIRECTORY to the pinned fork's charts/ovn-kubernetes directory")
	}
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cat, err := catalog.LoadContext(ctx, catalog.Dir("../../../definitions/nvke-ovn-source/catalog"))
	if err != nil {
		t.Fatal(err)
	}
	mapping := cat.Components["ovn-kubernetes-chart"].Helm
	if mapping == nil || len(mapping.Images) != 6 {
		t.Fatal("missing six-slot source chart mapping")
	}
	stage := t.TempDir()
	if err := os.CopyFS(filepath.Join(stage, "ovn-kubernetes"), os.DirFS(source)); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(stage, "ovn-kubernetes", "values-single-node-zone.yaml")
	if target, err := os.Readlink(alias); err == nil {
		if target != "values.yaml" {
			t.Fatalf("unexpected chart alias target %q", target)
		}
		if err := os.Remove(alias); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(stage, "chart.tar")
	if err := transportarchive.Pack(ctx, archive, stage, []string{"ovn-kubernetes"}); err != nil {
		t.Fatal(err)
	}
	root, relative, cleanup, err := helmPackageTree(ctx, Artifact{Kind: "tarball", BuildTarget: "ovn-chart", Path: archive})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	chart := filepath.Join(root, relative)
	before, err := digestFactoryPath(ctx, filepath.Join(chart, "charts"))
	if err != nil {
		t.Fatal(err)
	}
	const hex = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	binding := HelmImageBinding{Chart: mapping.Chart, Images: map[string]HelmImage{}}
	digests := map[string]string{}
	for slot, image := range mapping.Images {
		binding.Images[slot] = HelmImage{From: "package/ovn-image", Values: image.Values}
		digests[slot] = "sha256:" + hex
	}
	if err := bindHelmImages(ctx, chart, binding, digests); err != nil {
		t.Fatal(err)
	}
	after, err := digestFactoryPath(ctx, filepath.Join(chart, "charts"))
	if err != nil || before != after {
		t.Fatal("binding changed upstream subchart bytes")
	}
	for _, repository := range []string{"ghcr.io/dsx-ai-factory/nvkr/m2-candidate/ovn-image-arm64", "mirror.example.test/ovn"} {
		args := []string{"template", "ovn", chart, "--set", "global.useImageDigest=true", "--set", "global.skipCallToK8s=true", "--set-string", "global.imagePullSecretName=", "--set-string", "k8sAPIServer=https://127.0.0.1:6443", "--set-string", "podNetwork=10.244.0.0/16"}
		for _, image := range mapping.Images {
			repositoryPath := strings.TrimSuffix(image.Values.Digest, ".sha256") + ".repository"
			args = append(args, "--set-string", repositoryPath+"="+repository)
		}
		cmd := exec.CommandContext(ctx, helm, args...)
		cmd.WaitDelay = 5 * time.Second
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Helm render: %v\n%s", err, out)
		}
		decoder := yaml.NewDecoder(strings.NewReader(string(out)))
		images := 0
		var visit func(*yaml.Node)
		visit = func(node *yaml.Node) {
			if node.Kind == yaml.MappingNode {
				for i := 0; i < len(node.Content); i += 2 {
					if node.Content[i].Value == "image" && node.Content[i+1].Kind == yaml.ScalarNode {
						image := node.Content[i+1].Value
						if !strings.HasPrefix(image, repository+":") || !strings.HasSuffix(image, "@sha256:"+hex) || strings.Contains(image, "sha256:sha256:") {
							t.Fatalf("unbound/malformed image: %s", image)
						}
						images++
					}
				}
			}
			for _, child := range node.Content {
				visit(child)
			}
		}
		for {
			var document yaml.Node
			err := decoder.Decode(&document)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			visit(&document)
		}
		if images != len(mapping.Images) {
			t.Fatalf("Helm rendered %d container images, want all %d bound slots", images, len(mapping.Images))
		}
		t.Logf("actual fork chart rendered %d digest-bound images for %s; source subcharts unchanged", images, repository)
	}
	boundArchive := filepath.Join(root, "bound.tar")
	if err := transportarchive.Pack(ctx, boundArchive, root, []string{relative}); err != nil {
		t.Fatal(err)
	}
	chartArchive := filepath.Join(root, "chart.tgz")
	if err := rerootHelmChartLayer(boundArchive, chartArchive, relative, relative, "ovn-kubernetes"); err != nil {
		t.Fatal(err)
	}
	chartData, err := os.ReadFile(chartArchive)
	if err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(t.TempDir(), "oci")
	if _, err := delivery.BuildHelmChartArtifact(ctx, artifact, packageMediaType(PackageHelm), chartData); err != nil {
		t.Fatal(err)
	}
	descriptor, err := delivery.PayloadDescriptor(ctx, artifact)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := v1.NewHash(descriptor.Digest)
	if err != nil {
		t.Fatal(err)
	}
	image, err := layout.Path(artifact).Image(hash)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	defer server.Close()
	repository := strings.TrimPrefix(server.URL, "http://") + "/ovn-chart-arm64"
	ref, err := name.NewTag(repository+":2.4.0", name.StrictValidation, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, image, remote.WithContext(ctx), remote.WithAuth(authn.Anonymous)); err != nil {
		t.Fatal(err)
	}
	pulled := t.TempDir()
	cmd := exec.CommandContext(ctx, helm, "pull", "oci://"+repository, "--version", "2.4.0", "--plain-http", "--destination", pulled, "--registry-config", filepath.Join(pulled, "registry.json"))
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("real Helm OCI pull: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(filepath.Join(pulled, "ovn-chart-arm64-2.4.0.tgz"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, chartData) {
		t.Fatal("Helm pull changed the bound chart layer bytes")
	}
	t.Logf("loopback OCI chart push and real Helm pull preserved %d layer bytes; manifest %s", len(raw), descriptor.Digest)
}
