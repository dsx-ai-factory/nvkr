package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

// Inspect verifies every descriptor reachable from the top-level index and then
// rehydrates its one canonical publication-root config.
func Inspect(ctx context.Context, root string) (Inspection, error) {
	return inspectPublication(ctx, root, nil)
}

// fetchOutputs is used only by Pull to fill the private staging layout from the
// exact signed sibling repository/digest bindings, before full closure checks.
func inspectPublication(ctx context.Context, root string, fetchOutputs func(PublicationRoot) error) (Inspection, error) {
	if err := deliveryContext(ctx); err != nil {
		return Inspection{}, err
	}
	index, raw, err := readIndex(ctx, root)
	if err != nil {
		return Inspection{}, err
	}
	if index.ArtifactType != MediaPublicationRoot {
		return Inspection{}, e.New(e.CodeVerifyFailed, "OCI layout is %q, not an NKD publication root", index.ArtifactType)
	}
	rootDigest := digestBytes(raw)
	visited := map[string]bool{}
	var rootConfig Descriptor
	direct := map[string]bool{}
	for _, descriptor := range index.Manifests {
		if direct[descriptor.Digest] {
			return Inspection{}, e.New(e.CodeVerifyFailed, "publication index repeats a descriptor")
		}
		direct[descriptor.Digest] = true
		if err := verifyDescriptor(ctx, root, descriptor, visited); err != nil {
			return Inspection{}, err
		}
		if descriptor.ArtifactType == MediaPublicationRoot {
			if rootConfig.Digest != "" {
				return Inspection{}, e.New(e.CodeVerifyFailed, "publication index contains multiple root configs")
			}
			manifestRaw, err := readBlob(ctx, root, descriptor, 8<<20)
			if err != nil {
				return Inspection{}, err
			}
			var manifest ociManifest
			if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
				return Inspection{}, e.Wrap(e.CodeVerifyFailed, err, "decoding publication-root manifest")
			}
			if manifest.ArtifactType != MediaPublicationRoot || len(manifest.Layers) != 0 || manifest.Config.MediaType != MediaPublicationRootConfig {
				return Inspection{}, e.New(e.CodeVerifyFailed, "publication-root manifest has an invalid config/layer contract")
			}
			rootConfig = manifest.Config
		}
	}
	if rootConfig.Digest == "" {
		return Inspection{}, e.New(e.CodeVerifyFailed, "publication index has no root config")
	}
	configRaw, err := readBlob(ctx, root, rootConfig, 8<<20)
	if err != nil {
		return Inspection{}, err
	}
	var publication PublicationRoot
	if err := canonical.UnmarshalExact(configRaw, &publication); err != nil {
		return Inspection{}, e.Wrap(e.CodeVerifyFailed, err, "decoding canonical publication root")
	}
	if err := publication.Validate(); err != nil {
		return Inspection{}, err
	}
	descriptors := map[string]Descriptor{}
	for _, descriptor := range index.Manifests {
		if descriptor.ArtifactType != MediaPublicationRoot {
			descriptors[descriptor.Digest] = descriptor
		}
	}
	if fetchOutputs != nil {
		if err := fetchOutputs(publication); err != nil {
			return Inspection{}, err
		}
	}
	for _, object := range publication.Objects {
		if object.Output == nil && object.Evidence == nil {
			continue
		}
		if _, exists := descriptors[object.DescriptorDigest]; exists {
			return Inspection{}, e.New(e.CodeVerifyFailed, "deliverable is duplicated in release repository")
		}
		descriptor := Descriptor{MediaType: MediaOCIManifest, Digest: object.DescriptorDigest, Size: object.Size, ArtifactType: object.ArtifactType}
		if err := verifyDescriptor(ctx, root, descriptor, visited); err != nil {
			return Inspection{}, err
		}
		descriptors[descriptor.Digest] = descriptor
	}
	if err := verifyBlobInventory(root, visited); err != nil {
		return Inspection{}, err
	}
	if len(descriptors) != len(publication.Objects) {
		return Inspection{}, e.New(e.CodeVerifyFailed, "publication-root object count does not match OCI descriptors")
	}
	for _, object := range publication.Objects {
		descriptor, ok := descriptors[object.DescriptorDigest]
		if !ok || descriptor.ArtifactType != object.ArtifactType || descriptor.Size != object.Size {
			return Inspection{}, e.New(e.CodeVerifyFailed, "publication object %q does not match its OCI descriptor", object.Name)
		}
		if object.Output == nil && object.Evidence == nil && (descriptor.Annotations["dev.nkd.semantic.digest"] != object.SemanticDigest ||
			descriptor.Annotations["dev.nkd.object.kind"] != object.Kind ||
			descriptor.Annotations["org.opencontainers.image.title"] != object.Name) {
			return Inspection{}, e.New(e.CodeVerifyFailed, "publication object %q descriptor annotations do not match root semantics", object.Name)
		}
		if publication.Release != nil && descriptor.MediaType != MediaOCIManifest {
			return Inspection{}, e.New(e.CodeVerifyFailed, "release objects must be direct manifests")
		}
		if err := verifyPublishedObjectBinding(ctx, root, object, descriptor); err != nil {
			return Inspection{}, err
		}
	}
	if err := verifyReleaseEvidence(ctx, root, publication); err != nil {
		return Inspection{}, err
	}
	semantic, err := publication.Digest()
	if err != nil {
		return Inspection{}, err
	}
	if index.Annotations["dev.nkd.semantic.digest"] != semantic {
		return Inspection{}, e.New(e.CodeVerifyFailed, "publication index semantic digest does not match root config")
	}
	return Inspection{Root: publication, RootDigest: rootDigest, RootConfig: rootConfig}, nil
}

func verifyPublishedObjectBinding(ctx context.Context, root string, object PublishedObject, descriptor Descriptor) error {
	if descriptor.MediaType == MediaOCIIndex {
		if object.BlobDigest != descriptor.Digest {
			return e.New(e.CodeVerifyFailed, "nested publication object %q root digest does not match its blob binding", object.Name)
		}
		return nil
	}
	if descriptor.MediaType != MediaOCIManifest {
		return e.New(e.CodeVerifyFailed, "publication object %q has unsupported descriptor media type", object.Name)
	}
	manifestRaw, err := readBlob(ctx, root, descriptor, 8<<20)
	if err != nil {
		return err
	}
	var manifest ociManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "decoding publication manifest")
	}
	if manifest.Config.MediaType == MediaImageConfig {
		if object.ArtifactType != MediaOCIManifest || object.BlobDigest != descriptor.Digest {
			return e.New(e.CodeVerifyFailed, "native image descriptor differs from release binding")
		}
		config, err := verifyImageManifest(ctx, root, manifest, map[string]bool{})
		if err != nil {
			return err
		}
		if config.OS != object.OS || config.Architecture != object.Arch {
			return e.New(e.CodeVerifyFailed, "native image platform differs from release binding")
		}
		return nil
	}
	if manifest.Config.MediaType == MediaBinaryBundleConfig {
		configRaw, err := readBlob(ctx, root, manifest.Config, 8<<20)
		if err != nil {
			return err
		}
		var bundle BinaryBundle
		if err := canonical.UnmarshalExact(configRaw, &bundle); err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "decoding binary config")
		}
		semantic, err := bundle.Digest()
		if err != nil || semantic != object.SemanticDigest || manifest.ArtifactType != MediaBinaryBundle || len(manifest.Layers) != 1 || manifest.Layers[0].MediaType != MediaBinaryBundleLayer || object.BlobDigest != descriptor.Digest {
			return e.New(e.CodeVerifyFailed, "binary manifest differs from release binding")
		}
		return nil
	}
	if manifest.Config.MediaType == MediaEmptyConfig {
		raw, err := readBlob(ctx, root, manifest.Config, 8<<20)
		if err != nil || string(raw) != "{}" || len(manifest.Layers) != 1 || manifest.Layers[0].Digest != object.BlobDigest || manifest.ArtifactType != object.ArtifactType {
			return e.New(e.CodeVerifyFailed, "artifact differs from release binding")
		}
		return nil
	}
	if len(manifest.Layers) != 1 || manifest.Config.MediaType != MediaDeliveryObjectConfig {
		return e.New(e.CodeVerifyFailed, "publication object %q has an invalid delivery manifest", object.Name)
	}
	configRaw, err := readBlob(ctx, root, manifest.Config, 8<<20)
	if err != nil {
		return err
	}
	var config objectConfig
	if err := canonical.UnmarshalExact(configRaw, &config); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "decoding publication object config")
	}
	if config.SchemaVersion != ObjectConfigSchema || config.Name != object.Name || config.Kind != object.Kind ||
		config.ArtifactType != object.ArtifactType || config.SemanticDigest != object.SemanticDigest ||
		config.BlobDigest != object.BlobDigest || config.BlobDigest != manifest.Layers[0].Digest ||
		config.Size != manifest.Layers[0].Size {
		return e.New(e.CodeVerifyFailed, "publication object %q config does not match its root binding", object.Name)
	}
	return nil
}

func verifyDescriptor(ctx context.Context, root string, descriptor Descriptor, visited map[string]bool) error {
	if visited[descriptor.Digest] {
		return nil
	}
	visited[descriptor.Digest] = true
	raw, err := readBlob(ctx, root, descriptor, 8<<20)
	if err != nil {
		return err
	}
	switch descriptor.MediaType {
	case MediaOCIIndex:
		var index ociIndex
		if err := json.Unmarshal(raw, &index); err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "decoding nested OCI index")
		}
		if index.SchemaVersion != 2 || index.MediaType != MediaOCIIndex {
			return e.New(e.CodeVerifyFailed, "nested OCI index has an invalid schema")
		}
		for _, child := range index.Manifests {
			if err := verifyDescriptor(ctx, root, child, visited); err != nil {
				return err
			}
		}
	case MediaOCIManifest:
		var manifest ociManifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "decoding OCI artifact manifest")
		}
		if manifest.ArtifactType == "" && manifest.Config.MediaType == MediaImageConfig {
			_, err := verifyImageManifest(ctx, root, manifest, visited)
			return err
		}
		if manifest.SchemaVersion != 2 || manifest.MediaType != MediaOCIManifest || manifest.ArtifactType == "" {
			return e.New(e.CodeVerifyFailed, "OCI artifact manifest has an invalid schema")
		}
		if _, err := readBlob(ctx, root, manifest.Config, 8<<20); err != nil {
			return err
		}
		visited[manifest.Config.Digest] = true
		for _, layer := range manifest.Layers {
			held, err := holdBlob(ctx, root, layer, maxDeliveryFileBytes)
			if err != nil {
				return err
			}
			if err := held.Close(); err != nil {
				return e.Wrap(e.CodeVerifyFailed, err, "closing verified OCI blob")
			}
			visited[layer.Digest] = true
		}
	default:
		return e.New(e.CodeVerifyFailed, "publication descriptor has unsupported media type %q", descriptor.MediaType)
	}
	return nil
}

func verifyBlobInventory(root string, reachable map[string]bool) error {
	entries, err := os.ReadDir(filepath.Join(root, "blobs", "sha256"))
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "reading OCI blob inventory")
	}
	if len(entries) != len(reachable) {
		return e.New(e.CodeVerifyFailed, "OCI layout has %d blobs but exactly %d are reachable", len(entries), len(reachable))
	}
	for _, entry := range entries {
		digest := "sha256:" + entry.Name()
		if entry.IsDir() || !reachable[digest] {
			return e.New(e.CodeVerifyFailed, "OCI layout contains unreachable or non-regular blob %q", entry.Name())
		}
	}
	return nil
}

func readBlob(ctx context.Context, root string, descriptor Descriptor, max int64) ([]byte, error) {
	held, err := holdBlob(ctx, root, descriptor, max)
	if err != nil {
		return nil, err
	}
	defer func() { _ = held.Close() }()
	return held.ReadAllContext(ctx, max)
}

func holdBlob(ctx context.Context, root string, descriptor Descriptor, max int64) (*heldfile.File, error) {
	if !isDigest(descriptor.Digest) || descriptor.Size < 0 || descriptor.Size > max {
		return nil, e.New(e.CodeVerifyFailed, "OCI descriptor is malformed or exceeds its byte bound")
	}
	path := filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(descriptor.Digest, "sha256:"))
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "OCI blob", ExpectedDigest: descriptor.Digest, MaxBytes: max})
	if err != nil {
		return nil, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "opening OCI blob %s", descriptor.Digest))
	}
	if held.Size() != descriptor.Size {
		_ = held.Close()
		return nil, e.New(e.CodeVerifyFailed, "OCI blob %s bytes do not match descriptor", descriptor.Digest)
	}
	return held, nil
}

// ExtractObject verifies the entire publication root and materializes the exact
// payload of one non-layout delivery object. The object config is rejoined to the
// root's name, kind, media type, semantic digest, byte digest, and size before the
// destination becomes visible.
func ExtractObject(ctx context.Context, layoutRoot, name, out string) (PublishedObject, error) {
	object, payload, err := ReadObject(ctx, layoutRoot, name)
	if err != nil {
		return PublishedObject{}, err
	}
	if err := writeExtractedObject(ctx, out, payload); err != nil {
		return PublishedObject{}, err
	}
	return object, nil
}

// ReadObject verifies a publication root and returns the exact payload of one
// non-layout object. The returned slice is detached from the layout.
func ReadObject(ctx context.Context, layoutRoot, name string) (PublishedObject, []byte, error) {
	inspection, err := Inspect(ctx, layoutRoot)
	if err != nil {
		return PublishedObject{}, nil, err
	}
	var object PublishedObject
	for _, candidate := range inspection.Root.Objects {
		if candidate.Name == name {
			if object.Name != "" {
				return PublishedObject{}, nil, e.New(e.CodeVerifyFailed, "publication root repeats object name %q", name)
			}
			object = candidate
		}
	}
	if object.Name == "" {
		return PublishedObject{}, nil, e.New(e.CodeUnverifiedRef, "publication root has no object named %q", name)
	}
	manifestRaw, err := readBlob(ctx, layoutRoot, Descriptor{MediaType: MediaOCIManifest,
		Digest: object.DescriptorDigest, Size: object.Size}, 8<<20)
	if err != nil {
		return PublishedObject{}, nil, err
	}
	var manifest ociManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil || manifest.ArtifactType != object.ArtifactType || len(manifest.Layers) != 1 {
		return PublishedObject{}, nil, e.New(e.CodeVerifyFailed, "publication object %q is not a single-payload delivery object", name)
	}
	payload, err := readBlob(ctx, layoutRoot, manifest.Layers[0], maxDeliveryFileBytes)
	if err != nil {
		return PublishedObject{}, nil, err
	}
	if digestBytes(payload) != object.BlobDigest || int64(len(payload)) != manifest.Layers[0].Size {
		return PublishedObject{}, nil, e.New(e.CodeVerifyFailed, "publication object %q payload differs from its root binding", name)
	}
	return object, append([]byte(nil), payload...), nil
}

func writeExtractedObject(ctx context.Context, destination string, payload []byte) error {
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return e.Wrap(e.CodeInternal, err, "creating extracted-object parent")
	}
	tmp, err := os.CreateTemp(parent, ".nkd-extracted-object-*.tmp")
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "staging extracted object")
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o644); err != nil {
		return err
	}
	if _, err := io.Copy(tmp, contextReader{ctx: ctx, reader: bytes.NewReader(payload)}); err != nil {
		return classifyContext(ctx, err)
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	if err := os.Link(tmpPath, destination); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return e.New(e.CodeImmutableViolation, "extracted object destination already exists: %s", destination)
		}
		return e.Wrap(e.CodeInternal, err, "committing extracted object")
	}
	committed = true
	if err := os.Remove(tmpPath); err != nil {
		return e.Wrap(e.CodeInternal, err, "removing extracted-object stage")
	}
	directory, err := os.Open(parent)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	if err := deliveryContext(reader.ctx); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}
