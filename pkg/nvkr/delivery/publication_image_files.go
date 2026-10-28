// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/partial"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// ExtractImageFiles materializes bounded regular files under the selected roots
// of one captured native image. The held publication owns manifest/config/layer
// integrity and lifetime; go-containerregistry owns whiteout interpretation.
// Selected roots must exist and are committed without replacement through the
// existing transport owner. This grants no signer or qualification authority.
func (publication *Publication) ExtractImageFiles(ctx context.Context, objectName, platform string, roots []string, out string) (resultErr error) {
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	if objectName == "" || out == "" || len(roots) == 0 || len(roots) > 8 {
		return e.New(e.CodeUsage, "image files require a named output, selected roots and destination")
	}
	if platform != "linux/amd64" && platform != "linux/arm64" {
		return e.New(e.CodeEngineCapability, "image files require an explicit supported native platform")
	}
	seen := map[string]bool{}
	for _, root := range roots {
		if !filepath.IsLocal(root) || filepath.Clean(root) != root || root == "." || strings.ContainsAny(root, "\\\x00") || seen[root] {
			return e.New(e.CodeUsage, "image file roots must be unique canonical local paths")
		}
		for previous := range seen {
			if strings.HasPrefix(root, previous+"/") || strings.HasPrefix(previous, root+"/") {
				return e.New(e.CodeUsage, "image file roots must not overlap")
			}
		}
		seen[root] = true
	}
	if publication == nil || publication.publicationState == nil {
		return e.New(e.CodeGateUnmet, "publication is not open")
	}
	publication.mu.RLock()
	defer publication.mu.RUnlock()
	if publication.closed || len(publication.files) == 0 {
		return e.New(e.CodeGateUnmet, "publication is not open")
	}
	object, err := publishedImageOutput(publication.inspection, objectName)
	if err != nil {
		return err
	}
	native := object
	if object.Output.ImageIndex != nil {
		closure := &imageIndexClosure{blobs: map[string]*heldfile.File{}}
		children, err := populatePublishedImageIndex(ctx, object, closure, publication.heldBlob)
		if err != nil {
			return err
		}
		native.DescriptorDigest = ""
		for _, child := range children {
			if child.Platform == platform {
				native.DescriptorDigest, native.BlobDigest, native.Size = child.ManifestDigest, child.ManifestDigest, child.Size
				native.ArtifactType = MediaOCIManifest
				native.OS, native.Arch, _ = strings.Cut(platform, "/")
			}
		}
		if native.DescriptorDigest == "" {
			return e.New(e.CodeVerifyFailed, "image output does not contain selected platform")
		}
	}
	open := func(ctx context.Context, descriptor Descriptor, maximum int64) (*heldfile.File, func(), error) {
		if err := deliveryContext(ctx); err != nil {
			return nil, nil, err
		}
		file, err := publication.heldBlob(descriptor, maximum)
		return file, func() {}, err
	}
	inspection, err := inspectPublishedContainerImageFrom(ctx, Inspection{Root: PublicationRoot{Objects: []PublishedObject{native}}}, objectName, open)
	if err != nil {
		return err
	}
	if inspection.OS+"/"+inspection.Arch != platform {
		return e.New(e.CodeVerifyFailed, "image files platform differs from named output")
	}
	raw, err := publication.readHeldBlob(ctx, Descriptor{Digest: native.DescriptorDigest, Size: native.Size, MediaType: native.ArtifactType}, 8<<20)
	if err != nil {
		return err
	}
	var manifest ociManifest // Already validated by the native image owner above.
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "reading checked image manifest")
	}
	config, err := publication.readHeldBlob(ctx, manifest.Config, 8<<20)
	if err != nil {
		return err
	}
	extractCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	core := &publicationImageCore{ctx: extractCtx, manifest: raw, config: config, media: types.MediaType(native.ArtifactType), layers: map[string]publicationImageLayer{}}
	for _, layer := range manifest.Layers {
		file, err := publication.heldBlob(layer, maxDeliveryFileBytes)
		if err != nil {
			return err
		}
		core.layers[layer.Digest] = publicationImageLayer{ctx: extractCtx, file: file, descriptor: layer}
	}
	image, err := partial.CompressedToImage(core)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "adapting held image to upstream extractor")
	}
	stage, err := os.MkdirTemp("", "nvkr-image-files-")
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "creating private image projection")
	}
	defer func() { _ = os.RemoveAll(stage) }()
	archive, err := os.OpenFile(filepath.Join(stage, "files.tar"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "creating held image projection archive")
	}
	defer func() {
		if err := archive.Close(); err != nil {
			resultErr = errors.Join(resultErr, e.Wrap(e.CodeImmutableViolation, err, "closing private image projection"))
		}
	}()
	extracted := mutate.Extract(image)
	// On a projection failure, cancellation reaches every held layer read and
	// draining observes the upstream extractor's terminal pipe close before the
	// publication releases its borrowed files. No source layout path is reopened.
	projectionErr := projectImageFiles(extractCtx, extracted, archive, roots)
	if projectionErr != nil {
		cancel()
	}
	_, drainErr := io.Copy(io.Discard, extracted)
	closeErr := extracted.Close()
	if projectionErr != nil {
		return projectionErr
	}
	if err := errors.Join(drainErr, closeErr); err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "joining image filesystem extraction"))
	}
	if err := archive.Sync(); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "persisting image projection archive")
	}
	return transportarchive.UnpackOpen(ctx, archive, out, roots)
}

// These are adapters to the pinned upstream Image interface, not an OCI codec.
// The existing image owner checked all descriptors and compressed/diff identities.
type publicationImageCore struct {
	ctx              context.Context
	manifest, config []byte
	media            types.MediaType
	layers           map[string]publicationImageLayer
}

func (image *publicationImageCore) RawManifest() ([]byte, error) {
	return image.manifest, image.ctx.Err()
}
func (image *publicationImageCore) RawConfigFile() ([]byte, error) {
	return image.config, image.ctx.Err()
}
func (image *publicationImageCore) MediaType() (types.MediaType, error) {
	return image.media, image.ctx.Err()
}
func (image *publicationImageCore) LayerByDigest(digest v1.Hash) (partial.CompressedLayer, error) {
	if err := image.ctx.Err(); err != nil {
		return nil, err
	}
	layer, exists := image.layers[digest.String()]
	if !exists {
		return nil, e.New(e.CodeVerifyFailed, "image layer is absent from held native closure")
	}
	return &layer, nil
}

type publicationImageLayer struct {
	ctx        context.Context
	file       *heldfile.File
	descriptor Descriptor
}

func (layer *publicationImageLayer) Digest() (v1.Hash, error) {
	return v1.NewHash(layer.descriptor.Digest)
}
func (layer *publicationImageLayer) Size() (int64, error) { return layer.file.Size(), layer.ctx.Err() }
func (layer *publicationImageLayer) MediaType() (types.MediaType, error) {
	return types.MediaType(layer.descriptor.MediaType), layer.ctx.Err()
}
func (layer *publicationImageLayer) Compressed() (io.ReadCloser, error) {
	if err := layer.ctx.Err(); err != nil {
		return nil, err
	}
	return io.NopCloser(publicationImageReader{ctx: layer.ctx, reader: io.NewSectionReader(layer.file, 0, layer.file.Size())}), nil
}

type publicationImageReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader publicationImageReader) Read(bytes []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(bytes)
}

// The existing bounded standard-library projection now belongs to the held
// image owner. Upstream handles layer merging; this admits only regular data.
func projectImageFiles(ctx context.Context, input io.Reader, output io.Writer, roots []string) (resultErr error) {
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	defer func() {
		if resultErr != nil {
			resultErr = classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, resultErr, "projecting bounded image files"))
		}
	}()
	limited := &io.LimitedReader{R: input, N: 128<<20 + 1}
	reader := tar.NewReader(limited)
	writer := tar.NewWriter(output)
	defer func() { resultErr = errors.Join(resultErr, writer.Close()) }()
	var total int64
	seen := map[string]bool{}
	for count := 0; ; count++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := reader.Next()
		if err == io.EOF {
			if limited.N <= 0 {
				return errors.New("image filesystem exceeds byte bound")
			}
			return nil
		}
		if err != nil {
			return err
		}
		member := strings.TrimSuffix(header.Name, "/")
		if count >= 4096 || !filepath.IsLocal(member) || filepath.Clean(member) != member {
			return errors.New("image contains an unsafe or excessive member list")
		}
		selected := false
		for _, root := range roots {
			selected = selected || member == root || strings.HasPrefix(member, root+"/")
		}
		if !selected {
			continue
		}
		if header.Typeflag != tar.TypeDir && header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > (16<<20)-total || header.Mode&0o7000 != 0 || seen[member] || header.Typeflag == tar.TypeDir && header.Size != 0 {
			return errors.New("image selection is not bounded regular data")
		}
		seen[member] = true
		total += header.Size
		if err = writer.WriteHeader(header); err != nil {
			return err
		}
		if _, err = transportarchive.CopyExact(ctx, writer, reader, header.Size); err != nil {
			return err
		}
	}
}
