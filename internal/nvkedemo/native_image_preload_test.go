// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

func TestGuestPreparationCarriesAndEnforcesCallerDeadline(t *testing.T) {
	tool, err := exec.LookPath("timeout")
	if err != nil {
		tool, err = exec.LookPath("gtimeout")
	}
	if err != nil {
		t.Fatal("guest deadline contract requires coreutils timeout")
	}
	tools := privateTestDir(t)
	if err := os.Symlink(tool, filepath.Join(tools, "timeout")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, script string
		exit         int
	}{
		{"completion", "printf completed", 0},
		{"guest deadline", "printf started; exec sleep 60", 124},
	} {
		t.Run(tc.name, func(t *testing.T) {
			work, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			command, err := guestWorkCommand(work, tc.script)
			if err != nil {
				t.Fatal(err)
			}
			// The host allows more time, so only the generated guest command
			// can enforce the original deadline in this real subprocess test.
			host, stop := context.WithTimeout(t.Context(), 10*time.Second)
			defer stop()
			runner := Runner{Env: []string{"PATH=" + tools + ":" + os.Getenv("PATH")}}
			raw, err := runner.Capture(host, []string{"/bin/bash", "-ec", command}, "guest-budget", 0)
			if tc.exit == 0 && (err != nil || string(raw) != "completed") {
				t.Fatalf("guest completion: %s %v", raw, err)
			}
			if tc.exit != 0 && (CommandExitCode(err) != tc.exit || errors.Is(err, ErrUnjoined) || string(raw) != "started") {
				t.Fatalf("guest deadline did not stop and join preparation: %s %v", raw, err)
			}
		})
	}
}

func TestNativeImageDigestReferencesRetainSelectedManifest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct{ name, reference, manifest, want string }{
		{"tagged source", "docker.io/library/nke-controller:latest", digest, "docker.io/library/nke-controller@" + digest},
		{"selected digest", "docker.io/nvkr-local/postgresql@" + digest, digest, "docker.io/nvkr-local/postgresql@" + digest},
		{"sibling digest", "docker.io/nvkr-local/postgresql@sha256:" + strings.Repeat("b", 64), digest, ""},
		{"implicit repository", "controller:latest", digest, ""},
		{"invalid manifest", "docker.io/library/nke-controller:latest", "latest", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nativeImageDigestReference(NativeImageAsset{Reference: tc.reference, ManifestDigest: tc.manifest})
			if got != tc.want || (err == nil) != (tc.want != "") {
				t.Fatalf("reference %q: %v", got, err)
			}
		})
	}
}

func TestNativeImageImportsKeepExactManifestAndDockerAlias(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		reference, repository string
		valid                 bool
	}{
		{"docker.io/nvkr-local/postgresql@" + digest, "docker.io/nvkr-local/postgresql", true},
		{workloadFixtureImage, "docker.io/library/alpine", true},
		{"localhost/nvke/ovn-kubernetes@" + digest, "localhost/nvke/ovn-kubernetes", true},
		{"index.docker.io/nvkr-local/postgresql@" + digest, "index.docker.io/nvkr-local/postgresql", true},
		{"docker.io/nvkr-local/postgresql:latest", "", false},
		{digest, "", false},
		{"postgresql@" + digest, "", false},
	} {
		t.Run(tc.reference, func(t *testing.T) {
			script, err := nativeImageImportScript("selected.tar", tc.reference)
			if (err == nil) != tc.valid {
				t.Fatalf("import reference: %v", err)
			}
			if !tc.valid {
				if script != "" {
					t.Fatal("invalid reference returned executable startup")
				}
				return
			}
			if strings.Contains(script, "io.cri-containerd.image") {
				t.Fatal("CRI must create the config-ID alias before marking the imported image managed")
			}
			if !strings.Contains(script, "images import --local --digests --base-name "+operatorQuote(tc.repository)) || !strings.Contains(script, "inspecti --quiet "+operatorQuote(tc.reference)) {
				t.Fatal("import must preserve the selected repository spelling and check its exact manifest through CRI")
			}
		})
	}
}

func TestNativeOVNProjectionUsesOnlyTheSelectedManifest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	reference := "localhost/nvke/ovn-kubernetes@" + digest
	for _, version := range []string{siteOVNVersion, upstreamSiteOVNVersion} {
		t.Run(version, func(t *testing.T) {
			raw, err := nativeOVNValues(version, reference)
			if err != nil {
				t.Fatal(err)
			}
			var values map[string]any
			if err := json.Unmarshal([]byte(raw), &values); err != nil {
				t.Fatal(err)
			}
			global := values["global"].(map[string]any)
			image := global["image"].(map[string]any)
			if version == siteOVNVersion {
				if global["useImageDigest"] != true || global["skipCallToK8s"] != false || global["enablePodHostPort"] != true || global["imagePullSecretName"] != "" || image["repository"] != "localhost/nvke/ovn-kubernetes" || image["tag"] != "nvkr" || image["sha256"] != strings.TrimPrefix(digest, "sha256:") {
					t.Fatal("NVIDIA native OVN projection lost exact image, Service lookup or hostPort support")
				}
			} else if global["enablePodHostPort"] != nil || global["enableOvnKubeIdentity"] != false || values["skipCallToK8s"] != true || image["repository"] != "localhost/nvke/ovn-kubernetes@sha256" || image["tag"] != strings.TrimPrefix(digest, "sha256:") || image["pullPolicy"] != "IfNotPresent" {
				t.Fatal("upstream image override used the NVIDIA schema")
			}
			script, err := nativeImageImportScript("ovn.tar", reference)
			if err != nil || !strings.Contains(script, "--digests --base-name "+operatorQuote("localhost/nvke/ovn-kubernetes")) || !strings.Contains(script, "inspecti --quiet "+operatorQuote(reference)) {
				t.Fatal("native digest import lost repository/manifest binding")
			}
		})
	}
	for _, tc := range []struct{ version, reference string }{
		{siteOVNVersion, "localhost/nvke/ovn-kubernetes:latest"},
		{siteOVNVersion, "localhost/nvke/sibling@" + digest},
		{"latest", reference}, {siteOVNVersion, "sha256:" + strings.Repeat("a", 64)},
	} {
		if raw, err := nativeOVNValues(tc.version, tc.reference); err == nil || raw != "" {
			t.Fatal("unsupported image/schema produced values")
		}
	}
}

func TestNativeOVNArchiveRetainsTheExactObservedManifest(t *testing.T) {
	for _, tc := range []struct {
		name, arch  string
		wrongDigest bool
		valid       bool
	}{
		{"selected", "arm64", false, true}, {"sibling observation", "arm64", true, false}, {"wrong platform", "amd64", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := t.TempDir()
			image := mutate.ConfigMediaType(mutate.MediaType(empty.Image, types.OCIManifestSchema1), types.OCIConfigJSON)
			var archive bytes.Buffer
			writer := tar.NewWriter(&archive)
			if err := writer.WriteHeader(&tar.Header{Name: "fixture", Mode: 0644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			image, err := mutate.AppendLayers(image, static.NewLayer(archive.Bytes(), types.OCIUncompressedLayer))
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := image.ConfigFile()
			if err != nil {
				t.Fatal(err)
			}
			cfg.OS, cfg.Architecture = "linux", tc.arch
			cfg.Config.Cmd = []string{"/bin/sh"}
			image, err = mutate.ConfigFile(image, cfg)
			if err != nil {
				t.Fatal(err)
			}
			digest, err := image.Digest()
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(run, "packages", "ovn-image-arm64")
			index, err := layout.Write(root, empty.Index)
			if err != nil {
				t.Fatal(err)
			}
			if err := index.AppendImage(image); err != nil {
				t.Fatal(err)
			}
			observed := digest.String()
			if tc.wrongDigest {
				observed = "sha256:" + strings.Repeat("b", 64)
			}
			work := t.TempDir()
			files := filepath.Join(work, "files")
			if err := os.Mkdir(files, 0700); err != nil {
				t.Fatal(err)
			}
			reference := "localhost/nvke/ovn-kubernetes@" + observed
			got, err := prepareNativeImage(t.Context(), nativeImageSelection{role: "ovn", root: run, output: "ovn-image-arm64", reference: reference, observed: lifecycle.Artifact{Digest: observed}}, work, files)
			if (err == nil) != tc.valid {
				t.Fatalf("image projection: %v", err)
			}
			if !tc.valid {
				return
			}
			if got.Reference != reference || got.ManifestDigest != observed {
				t.Fatal("image alias changed selected subject")
			}
			inspected, err := delivery.InspectContainerImage(t.Context(), filepath.Join(work, "images", "ovn"), "linux/arm64")
			if err != nil || inspected.ManifestDigest != observed {
				t.Fatalf("projected manifest: %v", err)
			}
			if _, err := DigestFile(t.Context(), got.File); err != nil {
				t.Fatal(err)
			}
		})
	}
}
