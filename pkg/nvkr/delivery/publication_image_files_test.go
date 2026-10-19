// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

func TestPublicationImageFilesUseHeldBytesAndSharedLifetime(t *testing.T) {
	root, pin, _ := heldNativeImageFixture(t, types.OCIManifestSchema1)
	publication, err := OpenPublication(t.Context(), root, pin)
	if err != nil {
		t.Fatal(err)
	}
	defer publication.Close()
	copyOf := *publication
	if err := os.Rename(root, root+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.json"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "files")
	if err := copyOf.ExtractImageFiles(t.Context(), "worker", "linux/amd64", []string{"usr"}, out); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(out, "usr/local/bin/worker"))
	if err != nil || string(raw) != "fixture worker" {
		t.Fatal(string(raw), err)
	}
	if err := copyOf.Close(); err != nil {
		t.Fatal(err)
	}
	refused := filepath.Join(t.TempDir(), "closed")
	assertRegistryCode(t, publication.ExtractImageFiles(t.Context(), "worker", "linux/amd64", []string{"usr"}, refused), e.CodeGateUnmet)
	if _, err := os.Stat(refused); !os.IsNotExist(err) {
		t.Fatal("closed owner produced output", err)
	}
}

func TestPublicationImageFilesRejectBeforeOutput(t *testing.T) {
	root, pin, _ := heldNativeImageFixture(t, types.OCIManifestSchema1)
	publication, err := OpenPublication(t.Context(), root, pin)
	if err != nil {
		t.Fatal(err)
	}
	defer publication.Close()
	for _, test := range []struct {
		name, output, platform string
		roots                  []string
		code                   e.Code
	}{
		{"missing output", "missing", "linux/amd64", []string{"usr"}, e.CodeVerifyFailed},
		{"wrong platform", "worker", "linux/arm64", []string{"usr"}, e.CodeVerifyFailed},
		{"missing platform", "worker", "", []string{"usr"}, e.CodeEngineCapability},
		{"unsafe root", "worker", "linux/amd64", []string{"../usr"}, e.CodeUsage},
		{"duplicate root", "worker", "linux/amd64", []string{"usr", "usr"}, e.CodeUsage},
		{"overlapping roots", "worker", "linux/amd64", []string{"usr", "usr/local"}, e.CodeUsage},
	} {
		t.Run(test.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "refused")
			assertRegistryCode(t, publication.ExtractImageFiles(t.Context(), test.output, test.platform, test.roots, out), test.code)
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatal("rejected selection produced output", err)
			}
		})
	}
	for _, handle := range []*Publication{nil, {}} {
		assertRegistryCode(t, handle.ExtractImageFiles(t.Context(), "worker", "linux/amd64", []string{"usr"}, filepath.Join(t.TempDir(), "nil")), e.CodeGateUnmet)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assertRegistryCode(t, publication.ExtractImageFiles(ctx, "worker", "linux/amd64", []string{"usr"}, filepath.Join(t.TempDir(), "cancel")), e.CodeTimeout)
}

func imageFilesTar(t *testing.T, headers []*tar.Header) []byte {
	t.Helper()
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	for _, header := range headers {
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := io.CopyN(writer, imageFilesZeroReader{}, header.Size); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

type imageFilesZeroReader struct{}

func (imageFilesZeroReader) Read(b []byte) (int, error) { clear(b); return len(b), nil }

func imageFilesPublication(t *testing.T, layers ...[]byte) (string, string) {
	t.Helper()
	image := mutate.ConfigMediaType(mutate.MediaType(empty.Image, types.OCIManifestSchema1), types.OCIConfigJSON)
	var err error
	for _, layer := range layers {
		image, err = mutate.AppendLayers(image, static.NewLayer(layer, types.OCIUncompressedLayer))
		if err != nil {
			t.Fatal(err)
		}
	}
	config, err := image.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	config.OS, config.Architecture = "linux", "amd64"
	config.Config.Entrypoint = []string{"/unused"}
	image, err = mutate.ConfigFile(image, config)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := image.RawManifest()
	if err != nil {
		t.Fatal(err)
	}
	var desired ociManifest
	if err := json.Unmarshal(raw, &desired); err != nil {
		t.Fatal(err)
	}
	configRaw, err := image.RawConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	root, pin := metadataImageProfileFixture(t, types.OCIManifestSchema1, func(_ *testing.T, manifest *ociManifest, config *[]byte) { *manifest = desired; *config = configRaw })
	for i, descriptor := range desired.Layers {
		if _, err := (layoutWriter{root: root}).addBlobBytes(layers[i], descriptor.MediaType); err != nil {
			t.Fatal(err)
		}
	}
	return root, pin
}

func TestPublicationImageFilesUseUpstreamWhiteoutsAndRejectUnsafeProjection(t *testing.T) {
	for _, test := range []struct {
		name    string
		upper   []*tar.Header
		invalid bool
	}{
		{"whiteout", []*tar.Header{{Name: "profiles/.wh.gone", Typeflag: tar.TypeReg, Mode: 0600}, {Name: "profiles/current", Typeflag: tar.TypeReg, Mode: 0600, Size: 1}}, false},
		{"traversal", []*tar.Header{{Name: "../profiles/escape", Typeflag: tar.TypeReg, Mode: 0600}}, true},
		{"selected symlink", []*tar.Header{{Name: "profiles/link", Typeflag: tar.TypeSymlink, Linkname: "current", Mode: 0600}}, true},
		{"selected bound", []*tar.Header{{Name: "profiles/large", Typeflag: tar.TypeReg, Mode: 0600, Size: 16<<20 + 1}}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			lower := imageFilesTar(t, []*tar.Header{{Name: "profiles/gone", Typeflag: tar.TypeReg, Mode: 0600, Size: 1}})
			upper := imageFilesTar(t, test.upper)
			root, pin := imageFilesPublication(t, lower, upper)
			publication, err := OpenPublication(t.Context(), root, pin)
			if err != nil {
				t.Fatal(err)
			}
			defer publication.Close()
			out := filepath.Join(t.TempDir(), "files")
			err = publication.ExtractImageFiles(t.Context(), "worker", "linux/amd64", []string{"profiles"}, out)
			if test.invalid {
				assertRegistryCode(t, err, e.CodeVerifyFailed)
				if _, err := os.Stat(out); !os.IsNotExist(err) {
					t.Fatal("unsafe image produced output", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(out, "profiles/gone")); !os.IsNotExist(err) {
				t.Fatal("whiteout did not remove lower file", err)
			}
			if _, err := os.Stat(filepath.Join(out, "profiles/current")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestImageFilesProjectionRetainsBoundsForIgnoredAndSelectedData(t *testing.T) {
	valid := []*tar.Header{{Name: "profiles", Typeflag: tar.TypeDir, Mode: 0700}, {Name: "profiles/osProfiles/metadata.yaml", Typeflag: tar.TypeReg, Mode: 0600, Size: 1}, {Name: "runbooks/setup.yaml", Typeflag: tar.TypeReg, Mode: 0600, Size: 1}, {Name: "unrelated/private", Typeflag: tar.TypeReg, Mode: 0600, Size: 1}}
	var projected bytes.Buffer
	if err := projectImageFiles(t.Context(), bytes.NewReader(imageFilesTar(t, valid)), &projected, []string{"profiles", "runbooks"}); err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(&projected)
	var names []string
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, header.Name)
	}
	if strings.Join(names, ",") != "profiles,profiles/osProfiles/metadata.yaml,runbooks/setup.yaml" {
		t.Fatal(names)
	}
	for _, test := range []struct {
		name    string
		headers []*tar.Header
	}{
		{"traversal", []*tar.Header{{Name: "../profiles/x", Typeflag: tar.TypeReg}}},
		{"hardlink", []*tar.Header{{Name: "runbooks/link", Typeflag: tar.TypeLink, Linkname: "outside"}}},
		{"privileged mode", []*tar.Header{{Name: "profiles/x", Typeflag: tar.TypeReg, Mode: 04700}}},
		{"duplicate", []*tar.Header{{Name: "profiles/x", Typeflag: tar.TypeReg}, {Name: "profiles/x", Typeflag: tar.TypeReg}}},
		{"selected size", []*tar.Header{{Name: "profiles/large", Typeflag: tar.TypeReg, Size: 16<<20 + 1}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertRegistryCode(t, projectImageFiles(t.Context(), bytes.NewReader(imageFilesTar(t, test.headers)), io.Discard, []string{"profiles", "runbooks"}), e.CodeVerifyFailed)
		})
	}
	headers := make([]*tar.Header, 4097)
	for i := range headers {
		headers[i] = &tar.Header{Name: "ignored", Typeflag: tar.TypeReg}
	}
	assertRegistryCode(t, projectImageFiles(t.Context(), bytes.NewReader(imageFilesTar(t, headers)), io.Discard, []string{"profiles"}), e.CodeVerifyFailed)
	var header bytes.Buffer
	writer := tar.NewWriter(&header)
	if err := writer.WriteHeader(&tar.Header{Name: "ignored", Typeflag: tar.TypeReg, Size: 129 << 20}); err != nil {
		t.Fatal(err)
	}
	stream := io.MultiReader(bytes.NewReader(header.Bytes()), io.LimitReader(imageFilesZeroReader{}, 129<<20))
	assertRegistryCode(t, projectImageFiles(t.Context(), stream, io.Discard, []string{"profiles"}), e.CodeVerifyFailed)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assertRegistryCode(t, projectImageFiles(ctx, bytes.NewReader(nil), io.Discard, []string{"profiles"}), e.CodeTimeout)
}

func TestPublicationImageFilesAggregateValidatesUnselectedSibling(t *testing.T) {
	root, result, index := registryImageIndexFixture(t)
	publication, err := OpenPublication(t.Context(), root, result.RootDigest)
	if err != nil {
		t.Fatal(err)
	}
	defer publication.Close()
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		out := filepath.Join(t.TempDir(), "files")
		if err := publication.ExtractImageFiles(t.Context(), "cwe", platform, []string{"usr"}, out); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(out, "usr/local/bin/worker"))
		if err != nil || string(raw) != "shared fixture worker" {
			t.Fatal(string(raw), err)
		}
	}
	sibling := index.Children[1]
	held, err := publication.heldBlob(Descriptor{Digest: sibling.ManifestDigest, Size: sibling.Size}, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "refused")
	assertRegistryCode(t, publication.ExtractImageFiles(t.Context(), "cwe", index.Children[0].Platform, []string{"usr"}, out), e.CodeVerifyFailed)
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("unselected sibling fault exposed output", err)
	}
}

type imageFilesCancelWriter struct{ cancel context.CancelFunc }

func (writer imageFilesCancelWriter) Write(raw []byte) (int, error) {
	writer.cancel()
	return len(raw), nil
}

func TestImageFilesProjectionCancellationDuringCopy(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	raw := imageFilesTar(t, []*tar.Header{{Name: "profiles/data", Typeflag: tar.TypeReg, Mode: 0600, Size: 2 << 20}})
	assertRegistryCode(t, projectImageFiles(ctx, bytes.NewReader(raw), imageFilesCancelWriter{cancel}, []string{"profiles"}), e.CodeTimeout)
	if ctx.Err() != context.Canceled {
		t.Fatal("copy did not observe cancellation")
	}
}
