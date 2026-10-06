// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// MediaQCOW2 carries the unchanged envelope bytes, not a tar or decoded RAW disk.
const MediaQCOW2 = "application/vnd.qemu.qcow2"

// MediaDiskImage is the admitted v1 release wrapper for an exact QCOW2 payload.
// Generic diagnostic transports may use other bounded MIME types, but those
// declarations do not become production disk output types.
const MediaDiskImage = "application/vnd.nkd.disk-image.v1"

const (
	diskFormatAnnotation     = "dev.nkd.disk.format"
	diskEncryptionAnnotation = "dev.nkd.disk.encryption"
	// RFC 6838 section 4.2 limits each type/subtype name to 127 ASCII bytes.
	maxDiskMediaTypeNameBytes = 127
)

// DiskArtifactRequest describes a local transport operation. Digest and
// Size are expectations checked against the exact stream, not producer authority.
// Only a declared unencrypted QCOW2 on Linux amd64/arm64 is admitted. This does
// not inspect the filesystem, detect LUKS, reproduce RAW or authorize publication.
type DiskArtifactRequest struct {
	Path         string
	Digest       string
	Size         int64
	Platform     string
	Format       string
	Encryption   string
	ArtifactType string
}

// DiskArtifactInspection separates the exact OCI manifest identity from its raw
// payload identity. Platform and encryption remain declarations in the wrapper.
type DiskArtifactInspection struct {
	ManifestDigest string
	PayloadDigest  string
	Size           int64
	Platform       string
	ArtifactType   string
	Encryption     string
}

// DiskArtifact owns an opened payload inode. WriteToContext rechecks the complete
// stream each time; source mutation can cause a failure after partial writes, so
// callers must stream into private staging and publish only after success. Close
// serializes with active streams. This capability proves transport integrity,
// never disk composition, qualification or release authorization.
type DiskArtifact struct {
	owner      *diskArtifactOwner
	inspection DiskArtifactInspection
}

type diskArtifactOwner struct {
	permit chan struct{}
	file   *os.File
}

// BuildDiskArtifact creates a local, deterministic single-layer OCI transport.
// It holds the no-follow source descriptor through one hash-checked copy and
// checks the staged bytes before commit. Neither capture nor verification uses a
// whole-payload buffer or memfd; workspace capacity must be admitted by the caller.
func BuildDiskArtifact(ctx context.Context, out string, request DiskArtifactRequest) (result Result, err error) {
	defer func() { err = diskArtifactError(ctx, err, e.CodeInternal) }()
	if err := deliveryContext(ctx); err != nil {
		return Result{}, err
	}
	platform, err := diskPlatform(request.Platform)
	if err != nil {
		return Result{}, err
	}
	if request.Format != "qcow2" || request.Encryption != "none" {
		return Result{}, e.New(e.CodeEngineCapability, "disk transport requires declared unencrypted QCOW2")
	}
	if !validDiskArtifactType(request.ArtifactType) || !isDigest(request.Digest) || request.Size <= 0 || request.Size > maxDeliveryFileBytes {
		return Result{}, e.New(e.CodeVerifyFailed, "disk transport requires exact bounded payload and artifact type")
	}
	source, info, err := securepath.OpenRegular(request.Path)
	if err != nil {
		return Result{}, e.Wrap(e.CodeVerifyFailed, err, "opening disk transport source")
	}
	defer func() { _ = source.Close() }()
	if info.Size() != request.Size {
		return Result{}, e.New(e.CodeVerifyFailed, "disk source size differs from its expectation")
	}
	stage, commit, cleanup, err := stageLayout(out)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()
	writer := layoutWriter{root: stage}
	blobPath := filepath.Join(stage, "blobs", "sha256", strings.TrimPrefix(request.Digest, "sha256:"))
	if err := os.MkdirAll(filepath.Dir(blobPath), 0o755); err != nil {
		return Result{}, e.Wrap(e.CodeInternal, err, "creating disk blob directory")
	}
	destination, err := os.OpenFile(blobPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return Result{}, e.Wrap(e.CodeInternal, err, "creating staged disk blob")
	}
	_, copyErr := streamDiskPayload(ctx, source, destination, request.Digest, request.Size)
	if copyErr == nil {
		copyErr = destination.Sync()
	}
	if err := errors.Join(copyErr, destination.Close()); err != nil {
		return Result{}, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "staging disk payload"))
	}
	configRaw, err := json.Marshal(platform)
	if err != nil {
		return Result{}, e.Wrap(e.CodeInternal, err, "encoding disk platform")
	}
	config, err := writer.addBlobBytes(configRaw, MediaImageConfig)
	if err != nil {
		return Result{}, err
	}
	manifest := standardDiskManifest(config, request.Digest, request.Size, request.ArtifactType)
	descriptor, err := writer.addJSONBlob(manifest, MediaOCIManifest)
	if err != nil {
		return Result{}, err
	}
	descriptor.ArtifactType = request.ArtifactType
	root, err := writer.writeIndex(ociIndex{SchemaVersion: 2, MediaType: MediaOCIIndex, Manifests: []Descriptor{descriptor}})
	if err != nil {
		return Result{}, err
	}
	checked, err := OpenDiskArtifact(ctx, stage, descriptor.Digest, request.Platform, request.Size)
	if err != nil {
		return Result{}, err
	}
	if err := checked.Close(); err != nil {
		return Result{}, err
	}
	if err := deliveryContext(ctx); err != nil {
		return Result{}, err
	}
	if err := commit(); err != nil {
		return Result{}, err
	}
	return Result{LayoutPath: out, RootDigest: root, SemanticDigest: request.Digest}, nil
}

// OpenSingleDiskArtifact selects and verifies the sole disk in a producer's OCI
// layout in one payload pass. The index only selects an expected manifest;
// OpenDiskArtifact authenticates that manifest and the complete bounded closure.
// The returned owner proves transport integrity, not producer or release authority.
func OpenSingleDiskArtifact(ctx context.Context, root, expectedPlatform string, maxPayloadBytes int64) (*DiskArtifact, error) {
	if err := deliveryContext(ctx); err != nil {
		return nil, err
	}
	if _, err := diskPlatform(expectedPlatform); err != nil {
		return nil, err
	}
	if maxPayloadBytes <= 0 || maxPayloadBytes > maxDeliveryFileBytes {
		return nil, e.New(e.CodeVerifyFailed, "disk reader requires a positive bounded payload budget")
	}
	index, _, err := readIndex(ctx, root)
	if err != nil {
		return nil, err
	}
	if len(index.Manifests) != 1 {
		return nil, e.New(e.CodeVerifyFailed, "disk layout must select exactly one manifest")
	}
	return OpenDiskArtifact(ctx, root, index.Manifests[0].Digest, expectedPlatform, maxPayloadBytes)
}

// OpenDiskArtifact checks one exact manifest/config/QCOW2-layer closure and owns
// the no-follow payload descriptor. The platform config uses image-spec's
// Platform, the same representation ORAS --artifact-platform writes. No bytes
// are decoded, and an absent encryption declaration remains unknown.
func OpenDiskArtifact(ctx context.Context, root, expectedManifest, expectedPlatform string, maxPayloadBytes int64) (artifact *DiskArtifact, err error) {
	defer func() { err = diskArtifactError(ctx, err, e.CodeVerifyFailed) }()
	if err := deliveryContext(ctx); err != nil {
		return nil, err
	}
	if _, err := diskPlatform(expectedPlatform); err != nil {
		return nil, err
	}
	if !isDigest(expectedManifest) || maxPayloadBytes <= 0 || maxPayloadBytes > maxDeliveryFileBytes {
		return nil, e.New(e.CodeVerifyFailed, "disk reader requires an exact manifest pin and positive bounded payload budget")
	}
	if err := verifyImageLayoutMarker(ctx, root); err != nil {
		return nil, err
	}
	index, indexRaw, err := readIndex(ctx, root)
	if err != nil {
		return nil, err
	}
	if err := canonical.ValidateJSONDocument(indexRaw); err != nil {
		return nil, err
	}
	if index.ArtifactType != "" || len(index.Manifests) != 1 || index.Manifests[0].MediaType != MediaOCIManifest ||
		index.Manifests[0].Digest != expectedManifest {
		return nil, e.New(e.CodeVerifyFailed, "disk layout must contain exactly the pinned OCI manifest")
	}
	descriptor := index.Manifests[0]
	artifact, err = openDiskManifest(ctx, root, descriptor, expectedPlatform, maxPayloadBytes)
	if err != nil {
		return nil, err
	}
	manifest, err := flatManifest(ctx, root, PublishedObject{DescriptorDigest: descriptor.Digest, Size: descriptor.Size})
	if err == nil {
		err = verifyBlobInventory(root, map[string]bool{descriptor.Digest: true, manifest.Config.Digest: true, artifact.inspection.PayloadDigest: true})
	}
	if err != nil {
		_ = artifact.Close()
		return nil, err
	}
	return artifact, nil
}

// openDiskManifest shares the exact disk profile across standalone diagnostics,
// release import and consumer readback. It does not require an isolated layout.
// An empty expected platform means validate the declared supported platform.
func openDiskManifest(ctx context.Context, root string, descriptor Descriptor, expectedPlatform string, maxPayloadBytes int64) (*DiskArtifact, error) {
	manifest, observed, err := inspectDiskMetadata(ctx, root, descriptor, expectedPlatform, maxPayloadBytes)
	if err != nil {
		return nil, err
	}
	observedPlatform := observed.OS + "/" + observed.Architecture
	encryption := manifest.Annotations[diskEncryptionAnnotation]
	layer := manifest.Layers[0]
	payloadPin := layer.Digest.String()
	source, info, err := securepath.OpenRegular(filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(payloadPin, "sha256:")))
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "opening disk artifact payload")
	}
	if info.Size() != layer.Size {
		_ = source.Close()
		return nil, e.New(e.CodeVerifyFailed, "disk payload differs from its descriptor size")
	}
	if _, err := streamDiskPayload(ctx, source, io.Discard, payloadPin, layer.Size); err != nil {
		_ = source.Close()
		return nil, err
	}
	permit := make(chan struct{}, 1)
	permit <- struct{}{}
	return &DiskArtifact{owner: &diskArtifactOwner{permit: permit, file: source}, inspection: DiskArtifactInspection{
		ManifestDigest: descriptor.Digest, PayloadDigest: payloadPin, Size: layer.Size,
		Platform: observedPlatform, ArtifactType: manifest.ArtifactType, Encryption: encryption}}, nil
}

// inspectDiskMetadata shares the disk profile with metadata-only browsing.
// It checks bounded manifest/config bytes and never opens the QCOW2 layer.
func inspectDiskMetadata(ctx context.Context, root string, descriptor Descriptor, expectedPlatform string, maxPayloadBytes int64) (ocispec.Manifest, ocispec.Platform, error) {
	if descriptor.MediaType != MediaOCIManifest || !isDigest(descriptor.Digest) || maxPayloadBytes <= 0 || maxPayloadBytes > maxDeliveryFileBytes {
		return ocispec.Manifest{}, ocispec.Platform{}, e.New(e.CodeVerifyFailed, "disk manifest requires exact bounded descriptors")
	}
	raw, err := readBlob(ctx, root, descriptor, 8<<20)
	if err != nil {
		return ocispec.Manifest{}, ocispec.Platform{}, err
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return ocispec.Manifest{}, ocispec.Platform{}, err
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return ocispec.Manifest{}, ocispec.Platform{}, e.Wrap(e.CodeVerifyFailed, err, "decoding disk artifact manifest")
	}
	if manifest.SchemaVersion != 2 || manifest.MediaType != MediaOCIManifest || !validDiskArtifactType(manifest.ArtifactType) ||
		(descriptor.ArtifactType != "" && descriptor.ArtifactType != manifest.ArtifactType) || manifest.Subject != nil ||
		manifest.Config.MediaType != MediaImageConfig || len(manifest.Config.URLs) != 0 || len(manifest.Config.Data) != 0 ||
		len(manifest.Layers) != 1 || manifest.Layers[0].MediaType != MediaQCOW2 || len(manifest.Layers[0].URLs) != 0 || len(manifest.Layers[0].Data) != 0 ||
		manifest.Layers[0].Size <= 0 || manifest.Layers[0].Size > maxPayloadBytes || !isDigest(manifest.Layers[0].Digest.String()) {
		return ocispec.Manifest{}, ocispec.Platform{}, e.New(e.CodeVerifyFailed, "disk artifact has an unsupported manifest, config or payload contract")
	}
	if format := manifest.Annotations[diskFormatAnnotation]; format != "" && format != "qcow2" {
		return ocispec.Manifest{}, ocispec.Platform{}, e.New(e.CodeEngineCapability, "disk transport format %q is unsupported", format)
	}
	encryption := manifest.Annotations[diskEncryptionAnnotation]
	if encryption != "" && encryption != "none" {
		return ocispec.Manifest{}, ocispec.Platform{}, e.New(e.CodeEngineCapability, "disk transport encryption %q is unsupported", encryption)
	}
	config := Descriptor{MediaType: manifest.Config.MediaType, Digest: manifest.Config.Digest.String(), Size: manifest.Config.Size}
	configRaw, err := readBlob(ctx, root, config, 8<<20)
	if err != nil {
		return ocispec.Manifest{}, ocispec.Platform{}, err
	}
	if err := canonical.ValidateJSONDocument(configRaw); err != nil {
		return ocispec.Manifest{}, ocispec.Platform{}, err
	}
	var observed ocispec.Platform
	decoder := json.NewDecoder(bytes.NewReader(configRaw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&observed); err != nil {
		return ocispec.Manifest{}, ocispec.Platform{}, e.Wrap(e.CodeVerifyFailed, err, "decoding disk platform config")
	}
	observedPlatform := observed.OS + "/" + observed.Architecture
	if _, err := diskPlatform(observedPlatform); err != nil {
		return ocispec.Manifest{}, ocispec.Platform{}, err
	}
	if (expectedPlatform != "" && observedPlatform != expectedPlatform) || observed.Variant != "" || observed.OSVersion != "" || len(observed.OSFeatures) != 0 {
		return ocispec.Manifest{}, ocispec.Platform{}, e.New(e.CodeVerifyFailed, "disk platform differs from the requested platform")
	}
	return manifest, observed, nil
}

// Inspection returns immutable descriptive identities, not authority to publish.
func (artifact *DiskArtifact) Inspection() DiskArtifactInspection {
	if artifact == nil {
		return DiskArtifactInspection{}
	}
	return artifact.inspection
}

// RequireStandardProfile checks the exact wrapper bytes emitted by
// BuildDiskArtifact. Generic OCI diagnostics may accept other annotations or
// encoding, but production cannot sign a caller-modified, self-pinned wrapper.
// This checks transport metadata only, not inventory or publication authority.
func (artifact *DiskArtifact) RequireStandardProfile(ctx context.Context) error {
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	if artifact == nil || artifact.owner == nil {
		return e.New(e.CodeGateUnmet, "disk artifact is not open")
	}
	select {
	case <-ctx.Done():
		return classifyContext(ctx, ctx.Err())
	case <-artifact.owner.permit:
	}
	defer func() { artifact.owner.permit <- struct{}{} }()
	if artifact.owner.file == nil {
		return e.New(e.CodeGateUnmet, "disk artifact is closed")
	}
	observed := artifact.inspection
	platform, err := diskPlatform(observed.Platform)
	if err != nil {
		return err
	}
	configRaw, err := json.Marshal(platform)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "encoding standard disk platform")
	}
	config := Descriptor{MediaType: MediaImageConfig, Digest: digestBytes(configRaw), Size: int64(len(configRaw))}
	raw, err := json.Marshal(standardDiskManifest(config, observed.PayloadDigest, observed.Size, observed.ArtifactType))
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "encoding standard disk manifest")
	}
	if observed.Encryption != "none" || digestBytes(raw) != observed.ManifestDigest {
		return e.New(e.CodeVerifyFailed, "disk wrapper differs from the exact standard transport profile")
	}
	return nil
}

func standardDiskManifest(config Descriptor, payload string, size int64, artifactType string) ociManifest {
	return ociManifest{SchemaVersion: 2, MediaType: MediaOCIManifest, ArtifactType: artifactType,
		Config: config, Layers: []Descriptor{{MediaType: MediaQCOW2, Digest: payload, Size: size,
			Annotations: map[string]string{ocispec.AnnotationTitle: "disk.qcow2"}}},
		Annotations: map[string]string{diskFormatAnnotation: "qcow2", diskEncryptionAnnotation: "none"}}
}

// WriteToContext emits only the held inode and checks its exact bytes and size.
// A failed stream must be discarded by the caller, including any partial bytes.
func (artifact *DiskArtifact) WriteToContext(ctx context.Context, destination io.Writer) (int64, error) {
	if err := deliveryContext(ctx); err != nil {
		return 0, err
	}
	if artifact == nil || artifact.owner == nil {
		return 0, e.New(e.CodeGateUnmet, "disk artifact is not open")
	}
	if destination == nil {
		return 0, e.New(e.CodeUsage, "disk artifact destination is required")
	}
	select {
	case <-ctx.Done():
		return 0, e.Wrap(e.CodeTimeout, ctx.Err(), "disk artifact stream canceled")
	case <-artifact.owner.permit:
	}
	defer func() { artifact.owner.permit <- struct{}{} }()
	if artifact.owner.file == nil {
		return 0, e.New(e.CodeGateUnmet, "disk artifact is closed")
	}
	return streamDiskPayload(ctx, artifact.owner.file, destination, artifact.inspection.PayloadDigest, artifact.inspection.Size)
}

// Close waits for an active stream and releases the owned payload descriptor.
func (artifact *DiskArtifact) Close() error {
	if artifact == nil || artifact.owner == nil {
		return nil
	}
	<-artifact.owner.permit
	defer func() { artifact.owner.permit <- struct{}{} }()
	if artifact.owner.file == nil {
		return nil
	}
	err := artifact.owner.file.Close()
	artifact.owner.file = nil
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "closing disk artifact")
	}
	return nil
}

func diskPlatform(value string) (ocispec.Platform, error) {
	if value != "linux/amd64" && value != "linux/arm64" {
		return ocispec.Platform{}, e.New(e.CodeEngineCapability, "disk platform %q is unsupported", value)
	}
	return ocispec.Platform{OS: "linux", Architecture: strings.TrimPrefix(value, "linux/")}, nil
}

func validDiskArtifactType(value string) bool {
	// Bound untrusted metadata before MIME parsing or any payload/output I/O.
	if len(value) > 2*maxDiskMediaTypeNameBytes+1 {
		return false
	}
	topLevel, subtype, found := strings.Cut(value, "/")
	if !found || len(topLevel) > maxDiskMediaTypeNameBytes || len(subtype) > maxDiskMediaTypeNameBytes {
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(value)
	return err == nil && mediaType == value && strings.Count(value, "/") == 1 && len(parameters) == 0
}

func diskArtifactError(ctx context.Context, err error, code e.Code) error {
	if err == nil {
		return nil
	}
	var coded *e.Error
	if !errors.As(err, &coded) {
		err = e.Wrap(code, err, "disk artifact transport")
	}
	return classifyContext(ctx, err)
}

func streamDiskPayload(ctx context.Context, source *os.File, destination io.Writer, digest string, size int64) (int64, error) {
	info, err := source.Stat()
	if err != nil || info.Size() != size || !info.Mode().IsRegular() {
		return 0, e.New(e.CodeVerifyFailed, "held disk payload size or mode changed")
	}
	hash := sha256.New()
	reader := contextReader{ctx: ctx, reader: io.NewSectionReader(source, 0, size)}
	written, err := io.CopyBuffer(io.MultiWriter(destination, hash), reader, make([]byte, 128<<10))
	if err != nil {
		return written, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "streaming held disk payload"))
	}
	if err := deliveryContext(ctx); err != nil {
		return written, err
	}
	info, err = source.Stat()
	if err != nil || info.Size() != size || written != size || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != digest {
		return written, e.New(e.CodeVerifyFailed, "disk payload stream differs from its exact digest or size")
	}
	return written, nil
}

// copyDiskPayload uses the already-open, checked descriptor rather than sealing
// a second multi-GiB copy in RAM. Its destination remains private to the caller's
// staging layout until the complete operation succeeds.
func (writer layoutWriter) copyDiskPayload(ctx context.Context, disk *DiskArtifact) error {
	observed := disk.Inspection()
	path := filepath.Join(writer.root, "blobs", "sha256", strings.TrimPrefix(observed.PayloadDigest, "sha256:"))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return e.Wrap(e.CodeInternal, err, "creating disk payload staging directory")
	}
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		existing, _, err := securepath.OpenRegular(path)
		if err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "checking existing disk payload")
		}
		defer func() { _ = existing.Close() }()
		_, err = streamDiskPayload(ctx, existing, io.Discard, observed.PayloadDigest, observed.Size)
		return err
	}
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "creating private disk payload")
	}
	_, copyErr := disk.WriteToContext(ctx, out)
	if copyErr == nil {
		copyErr = out.Sync()
	}
	if err := errors.Join(copyErr, out.Close()); err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "copying exact disk payload"))
	}
	return nil
}

// publicationDiskPayloads selects storage, not authority: the later complete
// Inspect validates release membership and evidence. All returned source FDs
// must be closed by the caller. Metadata stays bounded; payloads stay streamed.
func publicationDiskPayloads(ctx context.Context, root, expectedRoot string) (map[string]*DiskArtifact, error) {
	index, indexRaw, err := readIndex(ctx, root)
	if err != nil {
		return nil, err
	}
	if digestBytes(indexRaw) != expectedRoot {
		return nil, e.New(e.CodeVerifyFailed, "publication changed before disk storage selection")
	}
	disks := map[string]*DiskArtifact{}
	reachable := map[string]bool{}
	failed := true
	defer func() {
		if failed {
			for _, disk := range disks {
				_ = disk.Close()
			}
		}
	}()
	for _, descriptor := range index.Manifests {
		if descriptor.ArtifactType != MediaPublicationRoot {
			continue
		}
		manifest, err := flatManifest(ctx, root, PublishedObject{DescriptorDigest: descriptor.Digest, Size: descriptor.Size})
		if err != nil {
			return nil, err
		}
		raw, err := readBlob(ctx, root, manifest.Config, 8<<20)
		if err != nil {
			return nil, err
		}
		var publication PublicationRoot
		if err := canonical.UnmarshalExact(raw, &publication); err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding publication disk storage selection")
		}
		if len(publication.Objects) > maxDeliveryObjects {
			return nil, e.New(e.CodeVerifyFailed, "publication disk storage selection exceeds object bound")
		}
		if err := publication.Validate(); err != nil {
			return nil, err
		}
		if publication.Release == nil {
			return nil, e.New(e.CodeVerifyFailed, "publication has no exact release metadata")
		}
		reachable[descriptor.Digest], reachable[manifest.Config.Digest] = true, true
		objects := append([]PublishedObject(nil), publication.Objects...)
		for i := 0; i < len(objects); i++ {
			object := objects[i]
			if publishedManifestMediaType(object) == MediaOCIIndex {
				raw, err := readBlob(ctx, root, Descriptor{MediaType: MediaOCIIndex, Digest: object.DescriptorDigest, Size: object.Size}, 8<<20)
				if err != nil {
					return nil, err
				}
				var children runnableIndex
				if err := decodeImageJSON(raw, &children); err != nil {
					return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding image index storage selection")
				}
				if children.SchemaVersion != 2 || children.MediaType != MediaOCIIndex || len(children.Manifests) == 0 || len(children.Manifests)+len(objects) > maxDeliveryObjects {
					return nil, e.New(e.CodeVerifyFailed, "image index storage selection exceeds supported bounds")
				}
				reachable[object.DescriptorDigest] = true
				for _, child := range children.Manifests {
					if child.MediaType != MediaOCIManifest && child.MediaType != MediaDockerManifest {
						return nil, e.New(e.CodeVerifyFailed, "image index storage selection requires native manifests")
					}
					objects = append(objects, PublishedObject{DescriptorDigest: child.Digest, Size: child.Size, ArtifactType: child.MediaType})
				}
				continue
			}
			// Select storage from the byte profile, not untrusted output labels:
			// a substituted kind must not force a raw disk into a whole-file memfd
			// before the later authoritative binding check rejects that label.
			manifest, err := flatManifest(ctx, root, object)
			if err != nil {
				return nil, err
			}
			reachable[object.DescriptorDigest], reachable[manifest.Config.Digest] = true, true
			// A malformed disk wrapper must not fall back to whole-file capture.
			// Both release declarations and byte-profile markers select the owning
			// disk validator; no caller-controlled kind/config can bypass it.
			_, hasFormat := manifest.Annotations[diskFormatAnnotation]
			_, hasEncryption := manifest.Annotations[diskEncryptionAnnotation]
			diskCandidate := object.Kind == "disk-image" || hasFormat || hasEncryption ||
				(object.Output != nil && (object.Output.Format == "disk-image" || object.Output.Disk != nil))
			for _, layer := range manifest.Layers {
				reachable[layer.Digest] = true
				diskCandidate = diskCandidate || layer.MediaType == MediaQCOW2
			}
			if !diskCandidate {
				continue
			}
			disk, err := openDiskManifest(ctx, root, Descriptor{MediaType: MediaOCIManifest, Digest: object.DescriptorDigest, Size: object.Size, ArtifactType: object.ArtifactType}, "", maxDeliveryFileBytes)
			if err != nil {
				return nil, err
			}
			pin := disk.Inspection().PayloadDigest
			if prior := disks[pin]; prior != nil {
				_ = disk.Close()
			} else {
				disks[pin] = disk
			}
		}
	}
	// Membership is known from bounded metadata before any payload capture.
	// In particular, an orphan large disk cannot consume a whole-file memfd.
	if err := verifyBlobInventory(root, reachable); err != nil {
		return nil, err
	}
	failed = false
	return disks, nil
}
