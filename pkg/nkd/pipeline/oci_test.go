package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	nkderr "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

func TestVerifyOCILayoutRequiresExactCompleteClosure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "image.oci")
	want := writeMinimalOCILayout(t, root)
	if err := verifyOCILayout(context.Background(), root, want); err != nil {
		t.Fatalf("valid complete OCI closure rejected: %v", err)
	}

	configPath := filepath.Join(root, "blobs", "sha256", minimalOCIConfigDigest()[len("sha256:"):])
	if err := os.WriteFile(configPath, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyOCILayout(context.Background(), root, want); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("tampered reachable blob must fail closed: %v", err)
	}

	root = filepath.Join(t.TempDir(), "image.oci")
	want = writeMinimalOCILayout(t, root)
	extra := []byte("unreferenced")
	extraDigest := digestBytes(extra)
	if err := os.WriteFile(filepath.Join(root, "blobs", "sha256", extraDigest[len("sha256:"):]), extra, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyOCILayout(context.Background(), root, want); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("unreferenced blob smuggling must fail closed: %v", err)
	}
}

func TestVerifyOCILayoutHonorsCancellation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "image.oci")
	want := writeMinimalOCILayout(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := verifyOCILayout(ctx, root, want); code(err) != nkderr.CodeTimeout {
		t.Fatalf("canceled verification must return timeout code: %v", err)
	}
}

func writeMinimalOCILayout(t *testing.T, root string) string {
	t.Helper()
	config := []byte(`{"architecture":"amd64","os":"linux"}`)
	layer := []byte("layer-bytes")
	configDigest := digestBytes(config)
	layerDigest := digestBytes(layer)
	manifest, err := json.Marshal(ociManifestDocument{
		SchemaVersion: 2,
		MediaType:     string("application/vnd.oci.image.manifest.v1+json"),
		Config: ociDescriptor{MediaType: "application/vnd.oci.image.config.v1+json",
			Digest: configDigest, Size: int64(len(config))},
		Layers: []ociDescriptor{{MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
			Digest: layerDigest, Size: int64(len(layer))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest := digestBytes(manifest)
	index, err := json.Marshal(ociIndexDocument{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.index.v1+json",
		Manifests: []ociDescriptor{{MediaType: "application/vnd.oci.image.manifest.v1+json",
			Digest: manifestDigest, Size: int64(len(manifest))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "blobs", "sha256"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, raw := range map[string][]byte{
		filepath.Join(root, "oci-layout"):                                       []byte(`{"imageLayoutVersion":"1.0.0"}`),
		filepath.Join(root, "index.json"):                                       index,
		filepath.Join(root, "blobs", "sha256", configDigest[len("sha256:"):]):   config,
		filepath.Join(root, "blobs", "sha256", layerDigest[len("sha256:"):]):    layer,
		filepath.Join(root, "blobs", "sha256", manifestDigest[len("sha256:"):]): manifest,
	} {
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return manifestDigest
}

func minimalOCIConfigDigest() string {
	return digestBytes([]byte(`{"architecture":"amd64","os":"linux"}`))
}
