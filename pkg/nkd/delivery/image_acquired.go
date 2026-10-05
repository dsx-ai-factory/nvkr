package delivery

import (
	"context"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// ImportAcquiredContainerImage copies one complete, digest-pinned native image
// closure without rewriting its manifest or layers. The source path and expected
// digest are descriptions until inspected. No acquisition or release authority
// is conferred; the factory independently authenticates the producer record.
// The local layout index is normalized transport scaffolding, not image identity.
func ImportAcquiredContainerImage(ctx context.Context, source, manifestDigest, platform, out string) (ImageInspection, error) {
	if err := deliveryContext(ctx); err != nil {
		return ImageInspection{}, err
	}
	if !isDigest(manifestDigest) {
		return ImageInspection{}, e.New(e.CodeUnverifiedRef, "acquired image requires an exact manifest digest")
	}
	image, err := InspectContainerImage(ctx, source, platform)
	if err != nil {
		return ImageInspection{}, err
	}
	if image.ManifestDigest != manifestDigest {
		return ImageInspection{}, e.New(e.CodeVerifyFailed, "acquired image differs from its pinned native manifest")
	}
	index, raw, err := readIndex(ctx, source)
	if err != nil {
		return ImageInspection{}, err
	}
	if digestBytes(raw) != image.RootDigest {
		return ImageInspection{}, e.New(e.CodeVerifyFailed, "acquired image index changed after inspection")
	}
	// The index is local handoff scaffolding, not upstream image identity. Keep
	// only the verified descriptor and discard acquisition-specific annotations.
	descriptor := Descriptor{MediaType: image.MediaType, Digest: image.ManifestDigest, Size: index.Manifests[0].Size}
	index = ociIndex{SchemaVersion: 2, MediaType: MediaOCIIndex, Manifests: []Descriptor{descriptor}}
	stage, commit, cleanup, err := stageLayout(out)
	if err != nil {
		return ImageInspection{}, err
	}
	defer cleanup()
	writer := layoutWriter{root: stage}
	if err := writer.copyClosure(ctx, source, descriptor); err != nil {
		return ImageInspection{}, err
	}
	if _, err := writer.writeIndex(index); err != nil {
		return ImageInspection{}, err
	}
	verified, err := InspectContainerImage(ctx, stage, platform)
	if err != nil {
		return ImageInspection{}, err
	}
	if verified.ManifestDigest != manifestDigest || verified.MediaType != image.MediaType {
		return ImageInspection{}, e.New(e.CodeVerifyFailed, "acquired image changed during import")
	}
	if err := deliveryContext(ctx); err != nil {
		return ImageInspection{}, err
	}
	if err := commit(); err != nil {
		return ImageInspection{}, err
	}
	return verified, nil
}
