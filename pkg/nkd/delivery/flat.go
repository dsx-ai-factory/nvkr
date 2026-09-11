package delivery

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

const MediaEmptyConfig = "application/vnd.oci.empty.v1+json"

// PayloadDescriptor identifies the single deliverable carried by a local OCI
// layout. Layout index files are transport scaffolding, not published wrappers.
func PayloadDescriptor(ctx context.Context, root string) (Descriptor, error) {
	index, _, err := readIndex(ctx, root)
	if err != nil {
		return Descriptor{}, err
	}
	var selected Descriptor
	visited := map[string]bool{}
	for _, descriptor := range index.Manifests {
		if err := verifyDescriptor(ctx, root, descriptor, visited); err != nil {
			return Descriptor{}, err
		}
		if descriptor.ArtifactType == MediaPublicationRoot {
			continue
		}
		if selected.Digest != "" || descriptor.MediaType != MediaOCIManifest {
			return Descriptor{}, e.New(e.CodeVerifyFailed, "payload layout must contain one deliverable manifest")
		}
		selected = descriptor
	}
	if selected.Digest == "" {
		return Descriptor{}, e.New(e.CodeVerifyFailed, "payload layout has no deliverable manifest")
	}
	if err := verifyBlobInventory(root, visited); err != nil {
		return Descriptor{}, err
	}
	return selected, nil
}

func (writer layoutWriter) importFlatLayout(ctx context.Context, input ObjectInput) (Descriptor, string, error) {
	_, indexRaw, err := readIndex(ctx, input.Path)
	if err != nil {
		return Descriptor{}, "", err
	}
	if input.BlobDigest != "" && digestBytes(indexRaw) != input.BlobDigest {
		return Descriptor{}, "", e.New(e.CodeVerifyFailed, "payload layout differs from its input pin")
	}
	selected, err := PayloadDescriptor(ctx, input.Path)
	if err != nil {
		return Descriptor{}, "", err
	}
	if err := writer.copyClosure(ctx, input.Path, selected); err != nil {
		return Descriptor{}, "", err
	}
	blob := selected.Digest
	manifest, err := flatManifest(ctx, input.Path, PublishedObject{DescriptorDigest: selected.Digest, Size: selected.Size})
	if err != nil {
		return Descriptor{}, "", err
	}
	if manifest.Config.MediaType == MediaEmptyConfig && len(manifest.Layers) == 1 {
		blob = manifest.Layers[0].Digest
	}
	return selected, blob, nil
}

func flatManifest(ctx context.Context, root string, object PublishedObject) (ociManifest, error) {
	raw, err := readBlob(ctx, root, Descriptor{MediaType: MediaOCIManifest, Digest: object.DescriptorDigest, Size: object.Size}, 8<<20)
	if err != nil {
		return ociManifest{}, err
	}
	var manifest ociManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return ociManifest{}, e.Wrap(e.CodeVerifyFailed, err, "decoding deliverable manifest")
	}
	return manifest, nil
}

// BuildArtifact builds one ordinary OCI artifact manifest and payload layer. The
// index.json is local OCI-layout scaffolding and is not nested in the release.
func BuildArtifact(ctx context.Context, out string, input ObjectInput) (Result, error) {
	if err := deliveryContext(ctx); err != nil {
		return Result{}, err
	}
	if input.Layout || !isDigest(input.SemanticDigest) || !validMediaType(input.ArtifactType) || !validMediaType(input.LayerMediaType) {
		return Result{}, e.New(e.CodeInvalidCatalog, "single artifact requires an exact typed payload")
	}
	stage, commit, cleanup, err := stageLayout(out)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()
	writer := layoutWriter{root: stage, plain: true}
	var descriptor Descriptor
	if input.Bytes != nil {
		descriptor, _, err = writer.addBytesObject(ctx, input)
	} else {
		descriptor, _, err = writer.addFileObject(ctx, input)
	}
	if err != nil {
		return Result{}, err
	}
	index := ociIndex{SchemaVersion: 2, MediaType: MediaOCIIndex, ArtifactType: input.ArtifactType, Manifests: []Descriptor{descriptor}}
	digest, err := writer.writeIndex(index)
	if err != nil {
		return Result{}, err
	}
	if _, err := PayloadDescriptor(ctx, stage); err != nil {
		return Result{}, err
	}
	if err := commit(); err != nil {
		return Result{}, err
	}
	return Result{LayoutPath: out, RootDigest: digest, SemanticDigest: input.SemanticDigest}, nil
}

// ReadArtifact returns a held, verified single-payload artifact for packaging
// inspection. Binary and native-image manifests require their own typed readers.
func ReadArtifact(ctx context.Context, root string) (Descriptor, []byte, error) {
	descriptor, err := PayloadDescriptor(ctx, root)
	if err != nil {
		return Descriptor{}, nil, err
	}
	manifest, err := flatManifest(ctx, root, PublishedObject{DescriptorDigest: descriptor.Digest, Size: descriptor.Size})
	if err != nil {
		return Descriptor{}, nil, err
	}
	if manifest.Config.MediaType != MediaEmptyConfig || len(manifest.Layers) != 1 {
		return Descriptor{}, nil, e.New(e.CodeVerifyFailed, "artifact has an unsupported config or payload count")
	}
	config, err := readBlob(ctx, root, manifest.Config, 8<<20)
	if err != nil || string(config) != "{}" {
		return Descriptor{}, nil, e.New(e.CodeVerifyFailed, "artifact config is not empty")
	}
	raw, err := readBlob(ctx, root, manifest.Layers[0], maxDeliveryFileBytes)
	return descriptor, raw, err
}

func (writer layoutWriter) copyClosure(ctx context.Context, source string, descriptor Descriptor) error {
	visited := map[string]bool{}
	if err := verifyDescriptor(ctx, source, descriptor, visited); err != nil {
		return err
	}
	for digest := range visited {
		held, err := heldfile.OpenContext(ctx, filepath.Join(source, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:")), heldfile.Options{Label: "release payload", MaxBytes: maxDeliveryFileBytes})
		if err != nil {
			return err
		}
		if held.Digest() != digest {
			_ = held.Close()
			return e.New(e.CodeVerifyFailed, "payload changed during import")
		}
		_, copyErr := writer.addBlobHeld(ctx, held, MediaOctetStream)
		closeErr := held.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}

	return nil
}
