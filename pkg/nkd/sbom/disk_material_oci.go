package sbom

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/NVIDIA/nke-recipes/internal/transportarchive"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// This adapter reuses the transport extraction and offline OCI closure owners.
// The scanner's Docker archive is only a private projection; the observation
// remains bound to the original held material, not a rewritten image identity.
func inspectNestedOCILayout(ctx context.Context, subject inspectionArchive, archive *generatedInspectionFile, platform string, scanner Scanner, maxBytes int64, budget *materialInspectionBudget) (*FilesystemInspection, error) {
	workspace, err := os.MkdirTemp("/tmp", "nkd-material-oci-*")
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "creating private OCI material workspace")
	}
	defer func() { _ = os.RemoveAll(workspace) }()
	if err := transportarchive.UnpackOpen(ctx, archive.file, workspace, []string{"oci-layout", "index.json", "blobs"}); err != nil {
		return nil, err
	}
	index, err := layout.ImageIndexFromPath(workspace)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "opening nested OCI index")
	}
	manifest, err := index.IndexManifest()
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding nested OCI index")
	}
	digest, err := index.Digest()
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "observing nested OCI root")
	}
	want := digest.String()
	if !manifest.MediaType.IsIndex() {
		if len(manifest.Manifests) != 1 {
			return nil, e.New(e.CodeVerifyFailed, "OCI layout without index media type requires one exact image root")
		}
		want = manifest.Manifests[0].Digest.String()
	}
	if err := pipeline.VerifyOfflineOCILayout(ctx, workspace, want); err != nil {
		return nil, err
	}
	images, err := nestedOCIImages(ctx, index, platform, budget, 0)
	if err != nil {
		return nil, err
	}
	if len(images) == 0 {
		return nil, e.New(e.CodeEngineCapability, "nested OCI layout has no inspectable image")
	}
	result := &FilesystemInspection{subject: subject.Digest(), size: subject.Size(), scanner: scanner.Digest}
	for _, image := range images {
		held, err := createGeneratedInspectionFile(ctx, maxBytes, func(destination io.Writer) error {
			return tarball.Write(name.MustParseReference("nkd.invalid/material:scan"), image, destination)
		})
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "sealing nested OCI scan transport")
		}
		inventory, scanErr := inspectGeneratedDependencies(ctx, held, scanner)
		closeErr := held.Close()
		if err := errors.Join(scanErr, closeErr); err != nil {
			return nil, err
		}
		result.components = append(result.components, withDiskContentClass(inventory.Components, diskClassContainer)...)
	}
	if err := pipeline.VerifyOfflineOCILayout(ctx, workspace, want); err != nil {
		return nil, err
	}
	if err := archive.AssertUnchangedContext(ctx); err != nil {
		return nil, err
	}
	if err := subject.AssertUnchangedContext(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

// Enumerate every descriptor before scanning. No platform selection silently
// drops an image: an unsupported or non-consumer image rejects the whole input.
func nestedOCIImages(ctx context.Context, index v1.ImageIndex, platform string, budget *materialInspectionBudget, depth int) ([]v1.Image, error) {
	if err := validateSBOMContext(ctx); err != nil {
		return nil, err
	}
	if depth > 8 {
		return nil, e.New(e.CodeEngineCapability, "nested OCI index exceeds bounded depth")
	}
	manifest, err := index.IndexManifest()
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding nested OCI membership")
	}
	var images []v1.Image
	for _, descriptor := range manifest.Manifests {
		if err := validateSBOMContext(ctx); err != nil {
			return nil, err
		}
		if err := budget.member(); err != nil {
			return nil, err
		}
		if descriptor.Platform != nil && (descriptor.Platform.OS != "linux" || descriptor.Platform.Architecture != strings.TrimPrefix(platform, "linux/")) {
			return nil, e.New(e.CodeVerifyFailed, "nested OCI descriptor platform differs from consumer")
		}
		switch {
		case descriptor.MediaType.IsIndex():
			child, err := index.ImageIndex(descriptor.Digest)
			if err != nil {
				return nil, e.Wrap(e.CodeVerifyFailed, err, "opening nested OCI child index")
			}
			children, err := nestedOCIImages(ctx, child, platform, budget, depth+1)
			if err != nil {
				return nil, err
			}
			images = append(images, children...)
		case descriptor.MediaType.IsImage():
			budget.images++
			if budget.images > 256 {
				return nil, e.New(e.CodeEngineCapability, "nested material exceeds its cumulative image budget")
			}
			image, err := index.Image(descriptor.Digest)
			if err != nil {
				return nil, e.Wrap(e.CodeVerifyFailed, err, "opening nested OCI image")
			}
			config, err := image.ConfigFile()
			if err != nil {
				return nil, e.Wrap(e.CodeVerifyFailed, err, "reading nested OCI image platform")
			}
			if config.OS != "linux" || config.Architecture != strings.TrimPrefix(platform, "linux/") {
				return nil, e.New(e.CodeVerifyFailed, "nested OCI image platform differs from consumer")
			}
			images = append(images, image)
		default:
			return nil, e.New(e.CodeEngineCapability, "nested OCI descriptor is not an image or index")
		}
	}
	return images, nil
}
