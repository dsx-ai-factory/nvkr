package delivery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

const (
	maxDeliveryFileBytes int64 = 64 << 30
	maxDeliveryObjects         = 4096
)

type Descriptor struct {
	MediaType    string            `json:"mediaType"`
	Digest       string            `json:"digest"`
	Size         int64             `json:"size"`
	ArtifactType string            `json:"artifactType,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
}

type ociManifest struct {
	Annotations   map[string]string `json:"annotations,omitempty"`
	Subject       *Descriptor       `json:"subject,omitempty"`
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	ArtifactType  string            `json:"artifactType"`
	Config        Descriptor        `json:"config"`
	Layers        []Descriptor      `json:"layers"`
}

type ociIndex struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	ArtifactType  string            `json:"artifactType,omitempty"`
	Manifests     []Descriptor      `json:"manifests"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

type objectConfig struct {
	SchemaVersion  string `json:"schemaVersion"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	ArtifactType   string `json:"artifactType"`
	SemanticDigest string `json:"semanticDigest"`
	BlobDigest     string `json:"blobDigest"`
	Size           int64  `json:"size"`
}

// ObjectInput is either a regular blob or a complete OCI layout. Layout inputs
// are nested by descriptor and copied as an exact closure; regular files become
// one-layer OCI artifacts.
type ObjectInput struct {
	annotations    map[string]string
	subject        *Descriptor
	Output         *OutputBinding
	Evidence       *EvidenceBinding
	Name           string
	Kind           string
	Path           string
	ArtifactType   string
	LayerMediaType string
	SemanticDigest string
	// BlobDigest optionally pins the exact regular-file bytes or nested OCI root
	// descriptor. Qualified publication sets it for every authority-bearing input.
	BlobDigest string
	OS         string
	Arch       string
	Layout     bool
	Bytes      []byte
}

type PublicationRequest struct {
	Release          *ReleaseMetadata
	ReleaseSetDigest string
	InventoryDigest  string
	Objects          []ObjectInput
}

type Result struct {
	LayoutPath     string
	RootDigest     string
	SemanticDigest string
}

type Inspection struct {
	Root       PublicationRoot
	RootDigest string
	RootConfig Descriptor
}

type layoutWriter struct {
	root  string
	plain bool
}

// BuildPublicationRoot creates a diagnostic release-root layout. It intentionally
// cannot emit QualificationQualified: that constructor belongs to the authenticated
// release-publication capability and cannot be replaced by CLI strings.
func BuildPublicationRoot(ctx context.Context, out string, request PublicationRequest) (Result, error) {
	if err := deliveryContext(ctx); err != nil {
		return Result{}, err
	}
	if len(request.Objects) == 0 || len(request.Objects) > maxDeliveryObjects {
		return Result{}, e.New(e.CodeUsage, "publication root requires 1..%d objects", maxDeliveryObjects)
	}
	if (request.ReleaseSetDigest == "") != (request.InventoryDigest == "") ||
		(request.ReleaseSetDigest != "" && (!isDigest(request.ReleaseSetDigest) || !isDigest(request.InventoryDigest))) {
		return Result{}, e.New(e.CodeUnverifiedRef, "publication root input identities are incomplete or malformed")
	}
	root := PublicationRoot{SchemaVersion: PublicationRootSchema, Qualification: QualificationDiagnostic,
		Release:          request.Release,
		ReleaseSetDigest: request.ReleaseSetDigest, InventoryDigest: request.InventoryDigest}
	return buildPublicationRoot(ctx, out, root, request.Objects)
}

func buildPublicationRoot(ctx context.Context, out string, root PublicationRoot, requested []ObjectInput) (Result, error) {
	if err := deliveryContext(ctx); err != nil {
		return Result{}, err
	}
	if len(requested) == 0 || len(requested) > maxDeliveryObjects {
		return Result{}, e.New(e.CodeUsage, "publication root requires 1..%d objects", maxDeliveryObjects)
	}
	objects := append([]ObjectInput(nil), requested...)
	// Admit the disk profile before staging or copying a multi-GiB payload.
	// This validates descriptions only; factory still owns receipt admission.
	for _, input := range objects {
		if input.Kind != "disk-image" {
			continue
		}
		if root.Release == nil || !input.Layout || input.Output == nil || input.Output.Format != "disk-image" || input.Output.Disk == nil ||
			!validDiskArtifactType(input.ArtifactType) || !isDigest(input.Output.Disk.PayloadDigest) ||
			input.Output.Disk.Encryption != "none" || input.Output.Disk.Size > maxDeliveryFileBytes || input.Output.TransportPath != "" {
			return Result{}, e.New(e.CodeVerifyFailed, "disk release input requires exact bounded unencrypted wrapper and payload binding")
		}
		if err := (Output{Name: input.Name, Kind: "disk-image", Digest: input.SemanticDigest, BuildDigest: input.Output.BuildDigest,
			Components: input.Output.Components, Disk: input.Output.Disk}).Validate(); err != nil {
			return Result{}, err
		}
		if _, err := diskPlatform(input.OS + "/" + input.Arch); err != nil {
			return Result{}, err
		}
	}
	sort.Slice(objects, func(i, j int) bool {
		if (objects[i].Evidence == nil) != (objects[j].Evidence == nil) {
			return objects[i].Evidence == nil
		}
		if objects[i].Name != objects[j].Name {
			return objects[i].Name < objects[j].Name
		}
		return objects[i].Kind < objects[j].Kind
	})
	stage, commit, cleanupStage, err := stageLayout(out)
	if err != nil {
		return Result{}, err
	}
	defer cleanupStage()
	writer := layoutWriter{root: stage, plain: root.Release != nil}
	descriptors := make([]Descriptor, 0, len(objects)+1)
	root.Objects = make([]PublishedObject, 0, len(objects))
	previous := ""
	for _, input := range objects {
		key := input.Name + "\x00" + input.Kind
		if key == previous {
			return Result{}, e.New(e.CodeInvalidCatalog, "publication repeats object %s/%s", input.Name, input.Kind)
		}
		previous = key
		if input.Evidence != nil {
			binding := *input.Evidence
			input.Evidence = &binding
			owner, ownerErr := evidenceOwner(root.Objects, input.Evidence)
			if ownerErr != nil {
				return Result{}, ownerErr
			}
			input.subject = &Descriptor{MediaType: publishedManifestMediaType(owner), Digest: owner.DescriptorDigest, Size: owner.Size}
		}
		var descriptor Descriptor
		var blobDigest string
		if input.Layout && root.Release != nil {
			descriptor, blobDigest, err = writer.importFlatLayout(ctx, input)
		} else if input.Layout {
			descriptor, _, err = writer.importLayout(ctx, input.Path)
			blobDigest = descriptor.Digest
			if err == nil && input.BlobDigest != "" && descriptor.Digest != input.BlobDigest {
				err = e.New(e.CodeVerifyFailed, "publication object %q OCI root digest does not match its pin", input.Name)
			}
		} else if input.Bytes != nil {
			descriptor, blobDigest, err = writer.addBytesObject(ctx, input)
		} else {
			descriptor, blobDigest, err = writer.addFileObject(ctx, input)
		}
		if err != nil {
			return Result{}, err
		}
		if input.Evidence != nil && input.Evidence.DocumentDigest == "" && input.ArtifactType == "application/vnd.cyclonedx+json" {
			input.Evidence.DocumentDigest = blobDigest
		}
		if !isDigest(input.SemanticDigest) {
			return Result{}, e.New(e.CodeUnverifiedRef, "publication object %q requires an exact semantic digest", input.Name)
		}
		if input.ArtifactType == "" {
			input.ArtifactType = descriptor.ArtifactType
		}
		descriptor.ArtifactType = input.ArtifactType
		descriptor.Annotations = map[string]string{
			"org.opencontainers.image.title": input.Name,
			"dev.nkd.object.kind":            input.Kind,
			"dev.nkd.semantic.digest":        input.SemanticDigest,
		}
		if input.OS != "" {
			descriptor.Annotations["org.opencontainers.image.os"] = input.OS
		}
		if input.Arch != "" {
			descriptor.Annotations["org.opencontainers.image.architecture"] = input.Arch
		}
		if input.Output == nil && input.Evidence == nil {
			descriptors = append(descriptors, descriptor)
		}
		root.Objects = append(root.Objects, PublishedObject{Name: input.Name, Kind: input.Kind,
			Output: input.Output, Evidence: input.Evidence,
			ArtifactType: input.ArtifactType, DescriptorDigest: descriptor.Digest,
			SemanticDigest: input.SemanticDigest, BlobDigest: blobDigest,
			Size: descriptor.Size, OS: input.OS, Arch: input.Arch})
	}
	rootDigest, rootSemantic, err := writer.writePublicationMetadata(root, descriptors)
	if err != nil {
		return Result{}, err
	}
	if inspection, err := Inspect(ctx, stage); err != nil || inspection.RootDigest != rootDigest {
		if err != nil {
			return Result{}, err
		}
		return Result{}, e.New(e.CodeVerifyFailed, "staged publication-root layout changed before commit")
	}
	if err := commit(); err != nil {
		return Result{}, err
	}
	return Result{LayoutPath: out, RootDigest: rootDigest, SemanticDigest: rootSemantic}, nil
}

func (writer layoutWriter) writePublicationMetadata(root PublicationRoot, descriptors []Descriptor) (string, string, error) {
	sortObjects(root.Objects)
	if err := root.Validate(); err != nil {
		return "", "", err
	}
	rootSemantic, err := root.Digest()
	if err != nil {
		return "", "", err
	}
	rootBytes, err := canonical.Marshal(root)
	if err != nil {
		return "", "", err
	}
	config, err := writer.addBlobBytes(rootBytes, MediaPublicationRootConfig)
	if err != nil {
		return "", "", err
	}
	rootManifest := ociManifest{SchemaVersion: 2, MediaType: MediaOCIManifest,
		ArtifactType: MediaPublicationRoot, Config: config, Layers: []Descriptor{}}
	rootDescriptor, err := writer.addJSONBlob(rootManifest, MediaOCIManifest)
	if err != nil {
		return "", "", err
	}
	rootDescriptor.ArtifactType = MediaPublicationRoot
	rootDescriptor.Annotations = map[string]string{"org.opencontainers.image.title": "publication-root", "dev.nkd.semantic.digest": rootSemantic}
	descriptors = append(descriptors, rootDescriptor)
	sort.Slice(descriptors, func(i, j int) bool {
		return descriptors[i].Annotations["org.opencontainers.image.title"] < descriptors[j].Annotations["org.opencontainers.image.title"]
	})
	index := ociIndex{SchemaVersion: 2, MediaType: MediaOCIIndex, ArtifactType: MediaPublicationRoot,
		Manifests: descriptors, Annotations: map[string]string{
			"dev.nkd.qualification":   root.Qualification,
			"dev.nkd.semantic.digest": rootSemantic,
		}}
	rootDigest, err := writer.writeIndex(index)
	if err != nil {
		return "", "", err
	}
	return rootDigest, rootSemantic, nil
}

func (writer layoutWriter) addFileObject(ctx context.Context, input ObjectInput) (Descriptor, string, error) {
	if input.ArtifactType == "" {
		input.ArtifactType = MediaDeliveryObject
	}
	if input.LayerMediaType == "" {
		input.LayerMediaType = MediaOctetStream
	}
	held, err := heldfile.OpenContext(ctx, input.Path, heldfile.Options{Label: "delivery-object:" + input.Name, MaxBytes: maxDeliveryFileBytes})
	if err != nil {
		return Descriptor{}, "", classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "capturing delivery object %q", input.Name))
	}
	defer func() { _ = held.Close() }()
	return writer.addHeldObject(ctx, input, held)
}

func (writer layoutWriter) addBytesObject(ctx context.Context, input ObjectInput) (Descriptor, string, error) {
	if input.ArtifactType == "" {
		input.ArtifactType = MediaDeliveryObject
	}
	if input.LayerMediaType == "" {
		input.LayerMediaType = MediaOctetStream
	}
	held, err := heldfile.FromBytesContext(ctx, input.Bytes, heldfile.Options{Label: "delivery-object:" + input.Name, MaxBytes: maxDeliveryFileBytes})
	if err != nil {
		return Descriptor{}, "", classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "capturing delivery object %q", input.Name))
	}
	defer func() { _ = held.Close() }()
	return writer.addHeldObject(ctx, input, held)
}

func (writer layoutWriter) addHeldObject(ctx context.Context, input ObjectInput, held *heldfile.File) (Descriptor, string, error) {
	if input.BlobDigest != "" && held.Digest() != input.BlobDigest {
		return Descriptor{}, "", e.New(e.CodeVerifyFailed, "publication object %q byte digest %s does not match pinned %s", input.Name, held.Digest(), input.BlobDigest)
	}
	layer, err := writer.addBlobHeld(ctx, held, input.LayerMediaType)
	if err != nil {
		return Descriptor{}, "", err
	}
	configValue := objectConfig{SchemaVersion: ObjectConfigSchema, Name: input.Name, Kind: input.Kind,
		ArtifactType: input.ArtifactType, SemanticDigest: input.SemanticDigest,
		BlobDigest: held.Digest(), Size: held.Size()}
	configBytes, err := canonical.Marshal(configValue)
	if err != nil {
		return Descriptor{}, "", err
	}
	configType := MediaDeliveryObjectConfig
	if writer.plain {
		configBytes = []byte("{}")
		configType = MediaEmptyConfig
	}
	config, err := writer.addBlobBytes(configBytes, configType)
	if err != nil {
		return Descriptor{}, "", err
	}
	manifest := ociManifest{SchemaVersion: 2, MediaType: MediaOCIManifest, ArtifactType: input.ArtifactType,
		Config: config, Layers: []Descriptor{layer}, Subject: input.subject, Annotations: input.annotations}
	descriptor, err := writer.addJSONBlob(manifest, MediaOCIManifest)
	if err != nil {
		return Descriptor{}, "", err
	}
	descriptor.ArtifactType = input.ArtifactType
	return descriptor, held.Digest(), held.AssertUnchangedContext(ctx)
}

func (writer layoutWriter) writeIndex(index ociIndex) (string, error) {
	raw, err := json.Marshal(index)
	if err != nil {
		return "", e.Wrap(e.CodeInternal, err, "encoding OCI index")
	}
	if err := os.WriteFile(filepath.Join(writer.root, "index.json"), raw, 0o644); err != nil {
		return "", e.Wrap(e.CodeInternal, err, "writing OCI index")
	}
	if err := os.WriteFile(filepath.Join(writer.root, "oci-layout"), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0o644); err != nil {
		return "", e.Wrap(e.CodeInternal, err, "writing OCI layout marker")
	}
	return digestBytes(raw), nil
}

func (writer layoutWriter) addJSONBlob(value any, mediaType string) (Descriptor, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return Descriptor{}, e.Wrap(e.CodeInternal, err, "encoding OCI document")
	}
	return writer.addBlobBytes(raw, mediaType)
}

func (writer layoutWriter) addBlobBytes(raw []byte, mediaType string) (Descriptor, error) {
	digest := digestBytes(raw)
	path := filepath.Join(writer.root, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:"))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Descriptor{}, err
	}
	if existing, err := os.ReadFile(path); err == nil {
		if !bytes.Equal(existing, raw) {
			return Descriptor{}, e.New(e.CodeVerifyFailed, "OCI blob collision at %s", digest)
		}
		return Descriptor{MediaType: mediaType, Digest: digest, Size: int64(len(raw))}, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Descriptor{}, err
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return Descriptor{}, err
	}
	return Descriptor{MediaType: mediaType, Digest: digest, Size: int64(len(raw))}, nil
}

func (writer layoutWriter) addBlobHeld(ctx context.Context, source *heldfile.File, mediaType string) (Descriptor, error) {
	path := filepath.Join(writer.root, "blobs", "sha256", strings.TrimPrefix(source.Digest(), "sha256:"))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Descriptor{}, err
	}
	if _, err := os.Lstat(path); err == nil {
		existing, openErr := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "existing OCI blob", MaxBytes: maxDeliveryFileBytes})
		if openErr != nil {
			return Descriptor{}, openErr
		}
		defer func() { _ = existing.Close() }()
		if existing.Digest() != source.Digest() || existing.Size() != source.Size() {
			return Descriptor{}, e.New(e.CodeVerifyFailed, "OCI blob collision at %s", source.Digest())
		}
		return Descriptor{MediaType: mediaType, Digest: source.Digest(), Size: source.Size()}, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Descriptor{}, err
	}
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return Descriptor{}, err
	}
	_, copyErr := source.WriteToContext(ctx, out)
	if copyErr == nil {
		copyErr = out.Sync()
	}
	closeErr := out.Close()
	if copyErr != nil {
		return Descriptor{}, classifyContext(ctx, copyErr)
	}
	if closeErr != nil {
		return Descriptor{}, closeErr
	}
	return Descriptor{MediaType: mediaType, Digest: source.Digest(), Size: source.Size()}, nil
}

func (writer layoutWriter) importLayout(ctx context.Context, source string) (Descriptor, int64, error) {
	inspection, indexRaw, err := readIndex(ctx, source)
	if err != nil {
		return Descriptor{}, 0, err
	}
	if len(inspection.Manifests) == 0 {
		return Descriptor{}, 0, e.New(e.CodeVerifyFailed, "nested OCI layout has no manifests")
	}
	var total int64
	entries, err := os.ReadDir(filepath.Join(source, "blobs", "sha256"))
	if err != nil {
		return Descriptor{}, 0, e.Wrap(e.CodeVerifyFailed, err, "reading nested OCI blobs")
	}
	if len(entries) > maxDeliveryObjects*8 {
		return Descriptor{}, 0, e.New(e.CodeVerifyFailed, "nested OCI layout has too many blobs")
	}
	for _, entry := range entries {
		if err := deliveryContext(ctx); err != nil {
			return Descriptor{}, 0, err
		}
		if entry.IsDir() || len(entry.Name()) != 64 {
			return Descriptor{}, 0, e.New(e.CodeVerifyFailed, "nested OCI layout contains noncanonical blob entry %q", entry.Name())
		}
		held, err := heldfile.OpenContext(ctx, filepath.Join(source, "blobs", "sha256", entry.Name()), heldfile.Options{Label: "nested-oci-blob", MaxBytes: maxDeliveryFileBytes})
		if err != nil {
			return Descriptor{}, 0, e.Wrap(e.CodeVerifyFailed, err, "holding nested OCI blob")
		}
		if held.Digest() != "sha256:"+entry.Name() {
			_ = held.Close()
			return Descriptor{}, 0, e.New(e.CodeVerifyFailed, "nested OCI blob name does not match bytes")
		}
		_, err = writer.addBlobHeld(ctx, held, MediaOctetStream)
		total += held.Size()
		_ = held.Close()
		if err != nil {
			return Descriptor{}, 0, err
		}
	}
	indexDescriptor, err := writer.addBlobBytes(indexRaw, MediaOCIIndex)
	if err != nil {
		return Descriptor{}, 0, err
	}
	indexDescriptor.ArtifactType = inspection.ArtifactType
	total += int64(len(indexRaw))
	return indexDescriptor, total, nil
}

func readIndex(ctx context.Context, root string) (ociIndex, []byte, error) {
	held, err := heldfile.OpenContext(ctx, filepath.Join(root, "index.json"), heldfile.Options{Label: "OCI index", MaxBytes: 8 << 20})
	if err != nil {
		return ociIndex{}, nil, e.Wrap(e.CodeVerifyFailed, err, "opening OCI index")
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, 8<<20)
	if err != nil {
		return ociIndex{}, nil, err
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return ociIndex{}, nil, err
	}
	var index ociIndex
	if err := json.Unmarshal(raw, &index); err != nil {
		return ociIndex{}, nil, e.Wrap(e.CodeVerifyFailed, err, "decoding OCI index")
	}
	if index.SchemaVersion != 2 || index.MediaType != MediaOCIIndex {
		return ociIndex{}, nil, e.New(e.CodeVerifyFailed, "OCI index has unsupported schema/media type")
	}
	return index, raw, nil
}

func stageLayout(out string) (string, func() error, func(), error) {
	if strings.TrimSpace(out) == "" {
		return "", nil, nil, e.New(e.CodeUsage, "output OCI layout is required")
	}
	if _, err := os.Lstat(out); err == nil {
		return "", nil, nil, e.New(e.CodeImmutableViolation, "refusing to overwrite OCI layout %s", out)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", nil, nil, err
	}
	parent := filepath.Dir(out)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", nil, nil, err
	}
	stage, err := os.MkdirTemp(parent, ".nkd-delivery-*")
	if err != nil {
		return "", nil, nil, err
	}
	committed := false
	cleanup := func() {
		if !committed {
			_ = os.RemoveAll(stage)
		}
	}
	commit := func() error {
		if _, err := os.Lstat(out); err == nil {
			return e.New(e.CodeImmutableViolation, "refusing to overwrite OCI layout %s", out)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := os.Rename(stage, out); err != nil {
			return e.Wrap(e.CodeInternal, err, "publishing OCI layout %s", out)
		}
		committed = true
		return nil
	}
	return stage, commit, cleanup, nil
}

func digestBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func deliveryContext(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "delivery operation requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "delivery operation canceled")
	}
	return nil
}

func classifyContext(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "delivery operation canceled")
	}
	return err
}
