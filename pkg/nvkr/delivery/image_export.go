// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/google/go-containerregistry/pkg/name"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sys/unix"
)

// PublishedImageExport describes unchanged image bytes and their archive transport.
// The reference annotation is an import locator, not signature or release authority.
type PublishedImageExport struct {
	Reference      string `json:"reference"`
	Repository     string `json:"repository"`
	ManifestDigest string `json:"manifestDigest"`
	Platform       string `json:"platform"`
	ArchiveDigest  string `json:"archiveDigest"`
	Size           int64  `json:"size"`
}

// ExportPublishedImage selects an exact named output from a checked publication.
// Aggregate outputs require one explicit native platform. The reference must bind
// the selected native digest. No manifest, config or layer bytes are converted.
// Callers must authenticate the publication independently before consuming it.
func ExportPublishedImage(ctx context.Context, root, objectName, reference, platform, out string) (PublishedImageExport, error) {
	ref, err := admitImageExport(ctx, objectName, reference, platform, out)
	if err != nil {
		return PublishedImageExport{}, err
	}
	inspection, err := Inspect(ctx, root)
	if err != nil {
		return PublishedImageExport{}, err
	}
	object, err := publishedImageOutput(inspection, objectName)
	if err != nil {
		return PublishedImageExport{}, err
	}
	descriptor := Descriptor{Digest: object.DescriptorDigest, Size: object.Size, MediaType: object.ArtifactType}
	var heldIndex *ImageIndex
	if object.Output.ImageIndex != nil {
		if platform == "" {
			return PublishedImageExport{}, e.New(e.CodeUsage, "aggregate image export requires an explicit platform")
		}
		index, err := openPublishedImageIndex(ctx, root, object)
		if err != nil {
			return PublishedImageExport{}, err
		}
		defer func() { _ = index.Close() }()
		heldIndex = index
		description, err := index.Description()
		if err != nil {
			return PublishedImageExport{}, err
		}
		descriptor = Descriptor{}
		for _, child := range description.Children {
			if child.Platform == platform {
				descriptor = Descriptor{Digest: child.ManifestDigest, Size: child.Size, MediaType: MediaOCIManifest}
			}
		}
		if descriptor.Digest == "" {
			return PublishedImageExport{}, e.New(e.CodeVerifyFailed, "image output does not contain selected platform")
		}
	} else {
		image, err := inspectPublishedContainerImage(ctx, root, inspection, objectName)
		if err != nil {
			return PublishedImageExport{}, err
		}
		actual := image.OS + "/" + image.Arch
		if platform != "" && platform != actual {
			return PublishedImageExport{}, e.New(e.CodeVerifyFailed, "image export platform differs from named output")
		}
		platform = actual
	}
	if ref.DigestStr() != descriptor.Digest {
		return PublishedImageExport{}, e.New(e.CodeVerifyFailed, "image export reference substitutes the selected native digest")
	}
	visited := map[string]bool{}
	if err := verifyDescriptor(ctx, root, descriptor, visited); err != nil {
		return PublishedImageExport{}, err
	}
	blobs := make(map[string]*heldfile.File, len(visited))
	defer func() {
		if heldIndex != nil {
			return // The complete index owner retains and closes borrowed blobs.
		}
		for _, file := range blobs {
			_ = file.Close()
		}
	}()
	var total int64
	for digest := range visited {
		var file *heldfile.File
		if heldIndex != nil {
			file = heldIndex.closure.blobs[digest]
			if file == nil {
				return PublishedImageExport{}, e.New(e.CodeVerifyFailed, "selected blob is absent from the held complete image index")
			}
		} else {
			file, err = heldfile.OpenContext(ctx, filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:")), heldfile.Options{Label: "selected image blob", ExpectedDigest: digest, MaxBytes: maxDeliveryFileBytes - total})
			if err != nil {
				return PublishedImageExport{}, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "capturing selected image closure"))
			}
		}
		blobs[digest] = file
		total += file.Size()
	}
	return exportHeldPublishedImage(ctx, ref, platform, out, descriptor, blobs)
}

func admitImageExport(ctx context.Context, objectName, reference, platform, out string) (name.Digest, error) {
	if err := deliveryContext(ctx); err != nil {
		return name.Digest{}, err
	}
	ref, err := name.NewDigest(reference, name.StrictValidation)
	if err != nil || ref.Name() != reference || objectName == "" || strings.TrimSpace(out) == "" || strings.HasSuffix(out, string(filepath.Separator)) {
		return name.Digest{}, e.New(e.CodeUsage, "image export requires named output, canonical digest reference and new destination")
	}
	if platform != "" && platform != "linux/amd64" && platform != "linux/arm64" {
		return name.Digest{}, e.New(e.CodeEngineCapability, "image export platform %q is unsupported", platform)
	}
	return ref, nil
}

func publishedImageOutput(inspection Inspection, objectName string) (PublishedObject, error) {
	var object PublishedObject
	for _, candidate := range inspection.Root.Objects {
		if candidate.Name == objectName {
			if object.Name != "" {
				return PublishedObject{}, e.New(e.CodeVerifyFailed, "ambiguous published image %q", objectName)
			}
			object = candidate
		}
	}
	if object.Output == nil || object.Output.Format != "oci" {
		return PublishedObject{}, e.New(e.CodeVerifyFailed, "publication has no named OCI image output %q", objectName)
	}
	return object, nil
}

func exportHeldPublishedImage(ctx context.Context, ref name.Digest, platform, out string,
	descriptor Descriptor, blobs map[string]*heldfile.File,
) (PublishedImageExport, error) {
	parent, _, err := securepath.OpenDirectory(filepath.Dir(out))
	if err != nil {
		return PublishedImageExport{}, e.Wrap(e.CodeVerifyFailed, err, "opening image export destination parent")
	}
	defer parent.Close()
	descriptor.Annotations = map[string]string{ocispec.AnnotationRefName: ref.Name()}
	archiveDigest, size, err := writePublishedImageArchive(ctx, parent, filepath.Base(out), descriptor, blobs)
	if err != nil {
		return PublishedImageExport{}, err
	}
	return PublishedImageExport{Reference: ref.Name(), Repository: ref.Context().Name(), ManifestDigest: descriptor.Digest, Platform: platform, ArchiveDigest: archiveDigest, Size: size}, nil
}

// The archive transaction uses only the held parent and anonymous blob snapshots.
// Standard-library tar encodes the OCI layout; native image bytes stay unchanged.
func writePublishedImageArchive(ctx context.Context, parent *os.File, name string, descriptor Descriptor, blobs map[string]*heldfile.File) (string, int64, error) {
	if err := deliveryContext(ctx); err != nil {
		return "", 0, err
	}
	if parent == nil || name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return "", 0, e.New(e.CodeUsage, "image archive requires a held parent and new filename")
	}
	index, err := json.Marshal(ociIndex{SchemaVersion: 2, MediaType: MediaOCIIndex, Manifests: []Descriptor{descriptor}})
	if err != nil {
		return "", 0, e.Wrap(e.CodeInternal, err, "encoding image archive index")
	}
	temporary, temporaryName, err := securepath.CreateTemporarySibling(parent)
	if err != nil {
		return "", 0, e.Wrap(e.CodeInternal, err, "creating private image archive")
	}
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = unix.Unlinkat(int(parent.Fd()), temporaryName, 0)
		}
	}()
	hash := sha256.New()
	writer := tar.NewWriter(io.MultiWriter(temporary, hash))
	write := func(name string, size int64, copy func(io.Writer) error) error {
		if err := deliveryContext(ctx); err != nil {
			return err
		}
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: size, Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
			return e.Wrap(e.CodeInternal, err, "writing image archive header")
		}
		if err := copy(writer); err != nil {
			return classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "writing image archive member"))
		}
		return nil
	}
	for _, member := range []struct {
		name string
		raw  []byte
	}{{"index.json", index}, {"oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`)}} {
		if err := write(member.name, int64(len(member.raw)), func(destination io.Writer) error {
			_, err := io.Copy(destination, bytes.NewReader(member.raw))
			return err
		}); err != nil {
			return "", 0, err
		}
	}
	digests := make([]string, 0, len(blobs))
	for digest := range blobs {
		digests = append(digests, digest)
	}
	sort.Strings(digests)
	for _, digest := range digests {
		file := blobs[digest]
		if err := write("blobs/sha256/"+strings.TrimPrefix(digest, "sha256:"), file.Size(), func(destination io.Writer) error { _, err := file.WriteToContext(ctx, destination); return err }); err != nil {
			return "", 0, err
		}
	}
	if err := writer.Close(); err != nil {
		return "", 0, e.Wrap(e.CodeInternal, err, "closing image archive")
	}
	if err := temporary.Chmod(0644); err != nil {
		return "", 0, e.Wrap(e.CodeInternal, err, "setting image archive mode")
	}
	if err := temporary.Sync(); err != nil {
		return "", 0, e.Wrap(e.CodeInternal, err, "persisting image archive")
	}
	info, err := temporary.Stat()
	if err != nil {
		return "", 0, e.Wrap(e.CodeInternal, err, "observing image archive size")
	}
	if info.Size() > maxDeliveryFileBytes {
		return "", 0, e.New(e.CodeVerifyFailed, "image archive exceeds its bounded import size")
	}
	if err := deliveryContext(ctx); err != nil {
		return "", 0, err
	}
	if err := transportarchive.PublishAtNoReplace(int(parent.Fd()), temporaryName, int(parent.Fd()), name); err != nil {
		return "", 0, e.Wrap(e.CodeImmutableViolation, err, "committing image archive without replacement")
	}
	committed = true
	if err := parent.Sync(); err != nil {
		return "", 0, e.Wrap(e.CodeInternal, err, "persisting image archive commit")
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), info.Size(), nil
}
