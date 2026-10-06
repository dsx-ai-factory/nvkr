package sbom

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/NVIDIA/nke-recipes/internal/diskdiagnostic"
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
	ordinal := 0
	return inspectNestedOCILayoutScan(ctx, subject, archive, platform, scanner, budget, func(ctx context.Context, _ string, image v1.Image) (DependencyInventory, error) {
		ordinal++
		depth, _ := ctx.Value(diskScanDepthKey{}).(int)
		diskdiagnostic.Emit(ctx, "scanner-transport", "start", depth, ordinal)
		held, err := createGeneratedInspectionFile(ctx, maxBytes, func(destination io.Writer) error {
			return tarball.Write(name.MustParseReference("nkd.invalid/material:scan"), image, destination)
		})
		if err != nil {
			return DependencyInventory{}, nestedOCIValidationError(ctx, err, "sealing nested OCI scan transport")
		}
		diskdiagnostic.Emit(ctx, "scanner-transport", "complete", depth, ordinal)
		inventory, scanErr := inspectGeneratedDependencies(ctx, held, scanner)
		return inventory, errors.Join(scanErr, held.Close())
	})
}

// Production supplies the concrete scanner owner. Tests can model a stopped
// scan/mutation, but cannot bypass pre/post closure validation or issue receipts.
func inspectNestedOCILayoutScan(ctx context.Context, subject inspectionArchive, archive *generatedInspectionFile, platform string, scanner Scanner, budget *materialInspectionBudget,
	scan func(context.Context, string, v1.Image) (DependencyInventory, error),
) (*FilesystemInspection, error) {
	workspace, err := os.MkdirTemp("/tmp", "nkd-material-oci-*")
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "creating private OCI material workspace")
	}
	defer func() { _ = os.RemoveAll(workspace) }()
	depth, _ := ctx.Value(diskScanDepthKey{}).(int)
	diskdiagnostic.Emit(ctx, "filesystem-extraction", "start", depth, 0)
	if err := transportarchive.UnpackOpen(ctx, archive.file, workspace, []string{"oci-layout", "index.json", "blobs"}); err != nil {
		return nil, nestedOCIValidationError(ctx, err, "extracting nested OCI layout")
	}
	diskdiagnostic.Emit(ctx, "filesystem-extraction", "complete", depth, 0)
	diskdiagnostic.Emit(ctx, "subject-revalidation", "start", depth, 0)
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
		return nil, nestedOCIValidationError(ctx, err, "validating nested OCI closure")
	}
	images, err := nestedOCIImages(ctx, index, platform, budget, 0)
	if err != nil {
		return nil, err
	}
	if len(images) == 0 {
		return nil, e.New(e.CodeEngineCapability, "nested OCI layout has no inspectable image")
	}
	diskdiagnostic.Emit(ctx, "subject-revalidation", "complete", depth, 0)
	result := &FilesystemInspection{subject: subject.Digest(), size: subject.Size(), scanner: scanner.Digest}
	for _, image := range images {
		inventory, err := scan(ctx, workspace, image)
		if err != nil {
			var coded *e.Error
			if ctx.Err() != nil && (!errors.As(err, &coded) || coded.Code != e.CodeTimeout) {
				return nil, nestedOCIValidationError(ctx, err, "scanning nested OCI image")
			}
			return nil, err
		}
		result.components = append(result.components, withDiskContentClass(inventory.Components, diskClassContainer)...)
	}
	diskdiagnostic.Emit(ctx, "subject-revalidation", "start", depth, 0)
	if err := pipeline.VerifyOfflineOCILayout(ctx, workspace, want); err != nil {
		return nil, nestedOCIValidationError(ctx, err, "revalidating scanned OCI closure")
	}
	if err := archive.AssertUnchangedContext(ctx); err != nil {
		return nil, err
	}
	if err := subject.AssertUnchangedContext(ctx); err != nil {
		return nil, err
	}
	diskdiagnostic.Emit(ctx, "subject-revalidation", "complete", depth, 0)
	return result, nil
}

// Extraction is transport and may use another owner's failure code. Translate
// malformed inspected material at this boundary; cancellation remains timeout.
func nestedOCIValidationError(ctx context.Context, err error, operation string) error {
	var coded *e.Error
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "%s canceled", operation)
	}
	if errors.As(err, &coded) && coded.Code == e.CodeTimeout {
		return err
	}
	return e.Wrap(e.CodeVerifyFailed, err, "%s", operation)
}

// Enumerate every descriptor before scanning. No platform selection silently
// drops an image: an unsupported or non-consumer image rejects the whole input.
func nestedOCIImages(ctx context.Context, index v1.ImageIndex, platform string, budget *materialInspectionBudget, depth int) ([]v1.Image, error) {
	return nestedOCIImagesSeen(ctx, index, platform, budget, depth, make(map[v1.Hash]bool))
}

func nestedOCIImagesSeen(ctx context.Context, index v1.ImageIndex, platform string, budget *materialInspectionBudget, depth int, seen map[v1.Hash]bool) ([]v1.Image, error) {
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
			children, err := nestedOCIImagesSeen(ctx, child, platform, budget, depth+1, seen)
			if err != nil {
				return nil, err
			}
			images = append(images, children...)
		case descriptor.MediaType.IsImage():
			if seen[descriptor.Digest] {
				continue // Descriptor membership/platform above is still checked.
			}
			seen[descriptor.Digest] = true
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
