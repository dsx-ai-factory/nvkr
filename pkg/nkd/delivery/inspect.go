package delivery

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
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
		if object.Output == nil {
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
		if object.Output == nil && (descriptor.Annotations["dev.nkd.semantic.digest"] != object.SemanticDigest ||
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
	raw, err := readBlob(ctx, root, descriptor, maxDeliveryFileBytes)
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
			if _, err := readBlob(ctx, root, layer, maxDeliveryFileBytes); err != nil {
				return err
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
	if !isDigest(descriptor.Digest) || descriptor.Size < 0 || descriptor.Size > max {
		return nil, e.New(e.CodeVerifyFailed, "OCI descriptor is malformed or exceeds its byte bound")
	}
	path := filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(descriptor.Digest, "sha256:"))
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "OCI blob", MaxBytes: max})
	if err != nil {
		return nil, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "opening OCI blob %s", descriptor.Digest))
	}
	defer func() { _ = held.Close() }()
	if held.Digest() != descriptor.Digest || held.Size() != descriptor.Size {
		return nil, e.New(e.CodeVerifyFailed, "OCI blob %s bytes do not match descriptor", descriptor.Digest)
	}
	return held.ReadAllContext(ctx, max)
}

// ExtractBinaryBundle verifies a publication root, selects one binary-bundle
// child by name, validates its canonical config against its tar layer, and
// atomically materializes only regular files under out.
func ExtractBinaryBundle(ctx context.Context, layoutRoot, name, out string) (BinaryBundle, error) {
	inspection, err := Inspect(ctx, layoutRoot)
	if err != nil {
		return BinaryBundle{}, err
	}
	var object PublishedObject
	for _, candidate := range inspection.Root.Objects {
		if candidate.Name == name && candidate.ArtifactType == MediaBinaryBundle {
			object = candidate
			break
		}
	}
	if object.Name == "" {
		return BinaryBundle{}, e.New(e.CodeUnverifiedRef, "publication root has no binary bundle named %q", name)
	}
	var manifestRaw []byte
	if inspection.Root.Release != nil {
		manifestRaw, err = readBlob(ctx, layoutRoot, Descriptor{MediaType: MediaOCIManifest, Digest: object.DescriptorDigest, Size: object.Size}, 8<<20)
	} else {
		indexRaw, readErr := readBlob(ctx, layoutRoot, Descriptor{MediaType: MediaOCIIndex, Digest: object.DescriptorDigest, Size: object.Size}, 8<<20)
		if readErr != nil {
			return BinaryBundle{}, readErr
		}
		var index ociIndex
		if err := json.Unmarshal(indexRaw, &index); err != nil || len(index.Manifests) != 1 {
			return BinaryBundle{}, e.New(e.CodeVerifyFailed, "binary layout must contain one manifest")
		}
		manifestRaw, err = readBlob(ctx, layoutRoot, index.Manifests[0], 8<<20)
	}
	if err != nil {
		return BinaryBundle{}, err
	}
	var manifest ociManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil || manifest.ArtifactType != MediaBinaryBundle || len(manifest.Layers) != 1 {
		return BinaryBundle{}, e.New(e.CodeVerifyFailed, "binary-bundle manifest has an invalid shape")
	}
	configRaw, err := readBlob(ctx, layoutRoot, manifest.Config, 8<<20)
	if err != nil {
		return BinaryBundle{}, err
	}
	var bundle BinaryBundle
	if err := canonical.UnmarshalExact(configRaw, &bundle); err != nil {
		return BinaryBundle{}, err
	}
	if err := bundle.Validate(); err != nil {
		return BinaryBundle{}, err
	}
	semantic, _ := bundle.Digest()
	if semantic != object.SemanticDigest {
		return BinaryBundle{}, e.New(e.CodeVerifyFailed, "binary-bundle semantic identity does not match publication root")
	}
	layerPath := filepath.Join(layoutRoot, "blobs", "sha256", strings.TrimPrefix(manifest.Layers[0].Digest, "sha256:"))
	held, err := heldfile.OpenContext(ctx, layerPath, heldfile.Options{Label: "binary-bundle layer", MaxBytes: maxDeliveryFileBytes})
	if err != nil {
		return BinaryBundle{}, err
	}
	defer func() { _ = held.Close() }()
	if held.Digest() != manifest.Layers[0].Digest || held.Size() != manifest.Layers[0].Size {
		return BinaryBundle{}, e.New(e.CodeVerifyFailed, "binary-bundle layer does not match its descriptor")
	}
	stage, commit, cleanup, err := stageLayout(out)
	if err != nil {
		return BinaryBundle{}, err
	}
	defer cleanup()
	if err := extractBundleLayer(ctx, held, stage, bundle); err != nil {
		return BinaryBundle{}, err
	}
	if err := commit(); err != nil {
		return BinaryBundle{}, err
	}
	return bundle, nil
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

func extractBundleLayer(ctx context.Context, held *heldfile.File, out string, bundle BinaryBundle) error {
	gzipReader, err := gzip.NewReader(io.NewSectionReader(held, 0, held.Size()))
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "opening binary-bundle gzip layer")
	}
	defer func() { _ = gzipReader.Close() }()
	tarReader := tar.NewReader(gzipReader)
	want := make(map[string]BinaryEntry, len(bundle.Entries))
	for _, entry := range bundle.Entries {
		want["files/"+entry.Name] = entry
	}
	seen := map[string]bool{}
	for {
		if err := deliveryContext(ctx); err != nil {
			return err
		}
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "reading binary-bundle tar layer")
		}
		entry, ok := want[header.Name]
		if !ok || seen[header.Name] || header.Typeflag != tar.TypeReg || header.Size != entry.Size || uint32(header.Mode) != entry.Mode {
			return e.New(e.CodeVerifyFailed, "binary-bundle tar contains an undeclared or mismatched entry %q", header.Name)
		}
		seen[header.Name] = true
		destination := filepath.Join(out, entry.Name)
		file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fs.FileMode(entry.Mode))
		if err != nil {
			return e.Wrap(e.CodeInternal, err, "creating extracted binary %q", entry.Name)
		}
		hash := sha256.New()
		written, copyErr := io.CopyN(io.MultiWriter(file, hash), contextReader{ctx: ctx, reader: tarReader}, entry.Size)
		if copyErr == nil {
			copyErr = file.Sync()
		}
		closeErr := file.Close()
		got := "sha256:" + hex.EncodeToString(hash.Sum(nil))
		if copyErr != nil || closeErr != nil || written != entry.Size || got != entry.SHA256 {
			return e.New(e.CodeVerifyFailed, "extracted binary %q does not match its config", entry.Name)
		}
	}
	if len(seen) != len(want) {
		missing := make([]string, 0)
		for name := range want {
			if !seen[name] {
				missing = append(missing, name)
			}
		}
		sort.Strings(missing)
		return e.New(e.CodeReleaseIncomplete, "binary-bundle layer omits entries: %s", strings.Join(missing, ", "))
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

// InspectBinaryBundle verifies a standalone binary-bundle layout and returns its
// canonical config plus both the OCI bootstrap and semantic digests.
func InspectBinaryBundle(ctx context.Context, root string) (BinaryBundle, Result, error) {
	index, raw, err := readIndex(ctx, root)
	if err != nil {
		return BinaryBundle{}, Result{}, err
	}
	if index.ArtifactType != MediaBinaryBundle || len(index.Manifests) != 1 {
		return BinaryBundle{}, Result{}, e.New(e.CodeVerifyFailed, "OCI layout is not a single NKD binary bundle")
	}
	visited := map[string]bool{}
	if err := verifyDescriptor(ctx, root, index.Manifests[0], visited); err != nil {
		return BinaryBundle{}, Result{}, err
	}
	if err := verifyBlobInventory(root, visited); err != nil {
		return BinaryBundle{}, Result{}, err
	}
	manifestRaw, err := readBlob(ctx, root, index.Manifests[0], 8<<20)
	if err != nil {
		return BinaryBundle{}, Result{}, err
	}
	var manifest ociManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil || manifest.ArtifactType != MediaBinaryBundle || len(manifest.Layers) != 1 {
		return BinaryBundle{}, Result{}, e.New(e.CodeVerifyFailed, "binary-bundle manifest has an invalid shape")
	}
	configRaw, err := readBlob(ctx, root, manifest.Config, 8<<20)
	if err != nil {
		return BinaryBundle{}, Result{}, err
	}
	var bundle BinaryBundle
	if err := canonical.UnmarshalExact(configRaw, &bundle); err != nil {
		return BinaryBundle{}, Result{}, err
	}
	semantic, err := bundle.Digest()
	if err != nil {
		return BinaryBundle{}, Result{}, err
	}
	if index.Annotations["dev.nkd.semantic.digest"] != semantic || index.Manifests[0].Annotations["dev.nkd.semantic.digest"] != semantic {
		return BinaryBundle{}, Result{}, e.New(e.CodeVerifyFailed, "binary-bundle OCI descriptors do not bind its semantic identity")
	}
	return bundle, Result{LayoutPath: root, RootDigest: digestBytes(raw), SemanticDigest: semantic}, nil
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	if err := deliveryContext(reader.ctx); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}
