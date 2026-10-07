// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// This serial fixture uses the existing library transport override, not a new
// acquisition API. The owner still enforces HTTPS, authenticates actual registry
// bytes and hands its original selected image to the production pipeline consumer.
// All acquisition callbacks/remote reads finish before Cleanup restores the global.
func TestBoundImageBuiltInAcquisitionTLS(t *testing.T) {
	server := httptest.NewTLSServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(server.Close)
	address, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := releaseExecutableRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme != "https" || (request.URL.Host != "example.invalid" && request.URL.Host != address.Host) {
			t.Errorf("unexpected registry fixture request: %s", request.URL)
		}
		copy := request.Clone(request.Context())
		copy.URL.Host = address.Host
		return server.Client().Transport.RoundTrip(copy)
	})
	original := remote.DefaultTransport
	remote.DefaultTransport = transport
	t.Cleanup(func() { remote.DefaultTransport = original })
	for _, media := range []types.MediaType{types.OCIManifestSchema1, types.DockerManifestSchema2} {
		for _, arch := range []string{"amd64", "arm64"} {
			for _, authored := range []string{"tag", "digest"} {
				t.Run(string(media)+"/"+arch+"/"+authored, func(t *testing.T) {
					image, err := random.Image(128, 1)
					if err != nil {
						t.Fatal(err)
					}
					config, err := image.ConfigFile()
					if err != nil {
						t.Fatal(err)
					}
					config.OS, config.Architecture = "linux", arch
					image, err = mutate.ConfigFile(image, config)
					if err != nil {
						t.Fatal(err)
					}
					image = mutate.MediaType(image, media)
					indexMedia := types.OCIImageIndex
					if media == types.DockerManifestSchema2 {
						indexMedia = types.DockerManifestList
					}
					index := mutate.IndexMediaType(mutate.AppendManifests(empty.Index, mutate.IndexAddendum{
						Add: image, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: arch}},
					}), indexMedia)
					ref, err := name.NewTag("example.invalid/fixture:v1")
					if err != nil {
						t.Fatal(err)
					}
					if err := remote.WriteIndex(ref, index, remote.WithContext(t.Context()), remote.WithAuth(authn.Anonymous), remote.WithTransport(transport)); err != nil {
						t.Fatal(err)
					}
					parent, err := index.Digest()
					if err != nil {
						t.Fatal(err)
					}
					child, err := image.Digest()
					if err != nil {
						t.Fatal(err)
					}
					artifact := ref.Name()
					if authored == "digest" {
						artifact = ref.Context().Digest(parent.String()).Name()
					}
					node := ir.Node{ID: "pull-image", Kind: ir.KindPull, Component: "image", OutputType: "oci-image",
						Artifact: artifact, Digest: child.String(), ImageParentDigest: parent.String(),
						Platform: "linux/" + arch, Network: ir.NetworkDefault, Version: "1.0.0"}
					opts := RunOptions{Ctx: t.Context(), SourceRoot: filepath.Join(t.TempDir(), "src"), OCI: registryOCITransport{}, Stdout: io.Discard}
					result, err := (Local{}).pull(node, opts)
					if err != nil {
						t.Fatal(err)
					}
					wantRef, err := ir.NativeImageReference(node)
					if err != nil {
						t.Fatal(err)
					}
					measured, err := verifyOCILayoutAndSize(t.Context(), result.TransportPath, child.String())
					if err != nil || measured <= 1 || result.Size != measured || result.Ref != wantRef || result.SHA256 != child.String() {
						t.Fatalf("acquired closure = %+v, measured=%d: %v", result, measured, err)
					}
					record := filepath.Join(t.TempDir(), "result.json")
					writeObservedResult(t, record, *result)
					observed, err := ObserveResult(t.Context(), node, result.TransportPath, record)
					if err != nil || observed.Ref != wantRef || observed.Size != measured {
						t.Fatalf("actual acquisition observation = %+v: %v", observed, err)
					}
					reused, err := (Local{}).pull(node, opts)
					if err != nil || reused.Size != measured || reused.TransportPath != result.TransportPath || reused.Ref != wantRef {
						t.Fatalf("cached acquisition = %+v: %v", reused, err)
					}
					configDigest, err := image.ConfigName()
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(result.TransportPath, "blobs", "sha256", configDigest.Hex), []byte("tampered existing config"), 0o600); err != nil {
						t.Fatal(err)
					}
					if cached, err := (Local{}).pull(node, opts); code(err) != e.CodeVerifyFailed || cached != nil {
						t.Fatalf("tampered existing acquisition = %+v: %v", cached, err)
					}
					missing := node
					missing.Digest = "sha256:" + strings.Repeat("a", 64)
					root := filepath.Join(t.TempDir(), "absent")
					opts.SourceRoot = filepath.Join(root, "src")
					if result, err := (Local{}).pull(missing, opts); code(err) != e.CodeVerifyFailed || result != nil {
						t.Fatalf("unbound child acquisition = %+v: %v", result, err)
					}
					if _, err := os.Lstat(root); !os.IsNotExist(err) {
						t.Fatalf("missing membership created output: %v", err)
					}
				})
			}
		}
	}
}
