// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// Use the existing upstream image model to create a schema-2 fixture. The test
// does not implement a second manifest grammar or rewrite production images.
func dockerImageFixture(t *testing.T) string {
	t.Helper()
	root := imageFixture(t)
	index, err := layout.Path(root).ImageIndex()
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := index.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}
	image, err := index.Image(manifest.Manifests[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	image = mutate.MediaType(image, types.DockerManifestSchema2)
	image = mutate.ConfigMediaType(image, types.DockerConfigJSON)
	out := filepath.Join(t.TempDir(), "docker")
	writer, err := layout.Write(out, empty.Index)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.AppendImage(image); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestImportAcquiredContainerImagePreservesExactManifest(t *testing.T) {
	t.Parallel()
	for _, testcase := range []struct {
		name    string
		fixture func(*testing.T) string
		media   string
	}{
		{name: "OCI", fixture: imageFixture, media: MediaOCIManifest},
		{name: "Docker schema 2", fixture: dockerImageFixture, media: MediaDockerManifest},
	} {
		t.Run(testcase.name, func(t *testing.T) {
			source := testcase.fixture(t)
			expected, err := InspectContainerImage(context.Background(), source, "linux/amd64")
			if err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(t.TempDir(), "imported")
			actual, err := ImportAcquiredContainerImage(context.Background(), source, expected.ManifestDigest, "linux/amd64", out)
			if err != nil {
				t.Fatal(err)
			}
			if actual.ManifestDigest != expected.ManifestDigest || actual.MediaType != testcase.media {
				t.Fatalf("changed acquired image: got %#v, want %#v", actual, expected)
			}
			publication := filepath.Join(t.TempDir(), "publication")
			_, err = BuildPublicationRoot(context.Background(), publication, PublicationRequest{Objects: []ObjectInput{{
				Name: "third-party", Kind: "oci-image", Path: out, Layout: true, ArtifactType: actual.MediaType,
				SemanticDigest: actual.ManifestDigest, BlobDigest: actual.RootDigest, OS: "linux", Arch: "amd64",
			}}})
			if err != nil {
				t.Fatal(err)
			}
			inspection, err := Inspect(context.Background(), publication)
			if err != nil || len(inspection.Root.Objects) != 1 || inspection.Root.Objects[0].SemanticDigest != expected.ManifestDigest {
				t.Fatalf("published upstream manifest changed: %#v, %v", inspection, err)
			}
		})
	}
}

func TestImportAcquiredContainerImageRejectsInvalidClosureWithoutOutput(t *testing.T) {
	t.Parallel()
	for _, testcase := range []struct {
		name   string
		change func(*testing.T, string)
		want   string
		code   e.Code
	}{
		{name: "missing digest", code: e.CodeUnverifiedRef},
		{name: "wrong pinned manifest", want: "sha256:" + strings.Repeat("a", 64), code: e.CodeVerifyFailed},
		{name: "unreachable bytes", code: e.CodeVerifyFailed, change: func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "blobs", "sha256", strings.Repeat("a", 64)), []byte("extra"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "changed layer", code: e.CodeVerifyFailed, change: func(t *testing.T, root string) {
			_, manifest := fixtureManifest(t, root)
			if err := os.WriteFile(filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(manifest.Layers[0].Digest, "sha256:")), []byte("wrong bytes"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(testcase.name, func(t *testing.T) {
			source := imageFixture(t)
			image, err := InspectContainerImage(context.Background(), source, "linux/amd64")
			if err != nil {
				t.Fatal(err)
			}
			want := testcase.want
			if testcase.change != nil {
				want = image.ManifestDigest
				testcase.change(t, source)
			}
			out := filepath.Join(t.TempDir(), "imported")
			_, err = ImportAcquiredContainerImage(context.Background(), source, want, "linux/amd64", out)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != testcase.code {
				t.Fatalf("error = %v, want %s", err, testcase.code)
			}
			if _, err := os.Lstat(out); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("failed import exposed output: %v", err)
			}
		})
	}
}
