package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/assemble"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func imageFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "worker")
	if err := os.WriteFile(binary, []byte("fixture worker"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "image")
	if _, err := assemble.OCIImage(binary, "worker", 1, "amd64", root); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestContainerImagePublicationPreservesRunnableManifest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	image := imageFixture(t)
	inspected, err := InspectContainerImage(ctx, image, "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	if len(inspected.Entrypoint) != 1 || inspected.Entrypoint[0] != "/usr/local/bin/worker" {
		t.Fatalf("entrypoint: %#v", inspected)
	}
	root := filepath.Join(t.TempDir(), "publication")
	_, err = BuildPublicationRoot(ctx, root, PublicationRequest{Objects: []ObjectInput{{
		Name: "cwe", Kind: "oci-image", Path: image, Layout: true, ArtifactType: MediaOCIManifest,
		SemanticDigest: inspected.ManifestDigest, BlobDigest: inspected.RootDigest, OS: "linux", Arch: "amd64",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	publication, err := Inspect(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(publication.Root.Objects) != 1 || publication.Root.Objects[0].SemanticDigest != inspected.ManifestDigest {
		t.Fatalf("image identity changed: %#v", publication.Root)
	}
}

func TestContainerImageRejectsMalformedClosure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		platform string
		mutate   func(*testing.T, string)
		code     e.Code
	}{
		{name: "wrong platform", platform: "linux/arm64", code: e.CodeVerifyFailed},
		{name: "unsupported platform", platform: "windows/amd64", code: e.CodeEngineCapability},
		{name: "unreachable blob", platform: "linux/amd64", code: e.CodeVerifyFailed, mutate: func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "blobs", "sha256", strings.Repeat("a", 64)), []byte("extra"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "corrupt layer", platform: "linux/amd64", code: e.CodeVerifyFailed, mutate: func(t *testing.T, root string) {
			_, manifest := fixtureManifest(t, root)
			if err := os.WriteFile(filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(manifest.Layers[0].Digest, "sha256:")), []byte("corrupt"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "wrong diff ID", platform: "linux/amd64", code: e.CodeVerifyFailed, mutate: func(t *testing.T, root string) {
			index, manifest := fixtureManifest(t, root)
			oldConfig, oldManifest := manifest.Config.Digest, index.Manifests[0].Digest
			configRaw, err := readBlob(context.Background(), root, manifest.Config, 8<<20)
			if err != nil {
				t.Fatal(err)
			}
			var config imageConfig
			if err := json.Unmarshal(configRaw, &config); err != nil {
				t.Fatal(err)
			}
			config.RootFS.DiffIDs[0] = "sha256:" + strings.Repeat("a", 64)
			writer := layoutWriter{root: root}
			manifest.Config, err = writer.addJSONBlob(config, MediaImageConfig)
			if err != nil {
				t.Fatal(err)
			}
			index.Manifests[0], err = writer.addJSONBlob(manifest, MediaOCIManifest)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(index)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "index.json"), raw, 0o644); err != nil {
				t.Fatal(err)
			}
			// Keep the closure otherwise valid so this negative cannot pass merely
			// because the old config/manifest became unreachable.
			for _, digest := range []string{oldConfig, oldManifest} {
				if err := os.Remove(filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:"))); err != nil {
					t.Fatal(err)
				}
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := imageFixture(t)
			if tc.mutate != nil {
				tc.mutate(t, root)
			}
			_, err := InspectContainerImage(context.Background(), root, tc.platform)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != tc.code {
				t.Fatalf("error = %v, want %s", err, tc.code)
			}
		})
	}
}

func fixtureManifest(t *testing.T, root string) (ociIndex, ociManifest) {
	t.Helper()
	index, _, err := readIndex(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := readBlob(context.Background(), root, index.Manifests[0], 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	var manifest ociManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	return index, manifest
}
