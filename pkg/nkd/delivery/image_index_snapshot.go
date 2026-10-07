package delivery

import (
	"context"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

// imageIndexClosure owns all file lifetimes. Opening a blob lends the immutable
// capability only while the owning ImageIndex operation holds its operation gate.
type imageIndexClosure struct {
	index *heldfile.File
	blobs map[string]*heldfile.File
	bytes int64
}

func (closure *imageIndexClosure) close() {
	if closure.index != nil {
		_ = closure.index.Close()
		closure.index = nil
	}
	for digest, file := range closure.blobs {
		_ = file.Close()
		delete(closure.blobs, digest)
	}
}

func (closure *imageIndexClosure) merge(child *imageIndexClosure) error {
	for digest, file := range child.blobs {
		if existing := closure.blobs[digest]; existing != nil {
			if existing.Size() != file.Size() {
				return e.New(e.CodeVerifyFailed, "image index has conflicting blob sizes")
			}
			continue
		}
		if len(closure.blobs) >= maxDeliveryObjects*8 || file.Size() > maxDeliveryFileBytes-closure.bytes {
			return e.New(e.CodeVerifyFailed, "image index closure exceeds object or byte bounds")
		}
		closure.blobs[digest] = file
		closure.bytes += file.Size()
		delete(child.blobs, digest) // Transfer ownership; child's remaining files close independently.
	}
	return nil
}

func (closure *imageIndexClosure) open(ctx context.Context, descriptor Descriptor, maximum int64) (*heldfile.File, func(), error) {
	if err := deliveryContext(ctx); err != nil {
		return nil, nil, err
	}
	file := closure.blobs[descriptor.Digest]
	if !isDigest(descriptor.Digest) || descriptor.Size < 0 || descriptor.Size > maximum || file == nil ||
		file.Size() != descriptor.Size || file.Digest() != descriptor.Digest {
		return nil, nil, e.New(e.CodeVerifyFailed, "image index blob differs from its descriptor or is absent")
	}
	return file, func() {}, nil
}

func (closure *imageIndexClosure) verify(ctx context.Context, platforms map[string]bool, requirePlatform bool) ([]ImageIndexChild, error) {
	raw, err := closure.index.ReadAllContext(ctx, 8<<20)
	if err != nil {
		return nil, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "reading held image index"))
	}
	var index runnableIndex
	if err := decodeImageJSON(raw, &index); err != nil {
		return nil, err
	}
	if index.SchemaVersion != 2 || index.MediaType != MediaOCIIndex || index.ArtifactType != "" || len(index.Manifests) != len(platforms) {
		return nil, e.New(e.CodeVerifyFailed, "runnable image index must exactly cover required platforms")
	}
	seenPlatforms, seenManifests, visited := map[string]bool{}, map[string]bool{}, map[string]bool{}
	children := make([]ImageIndexChild, 0, len(platforms))
	for _, descriptor := range index.Manifests {
		if descriptor.MediaType != MediaOCIManifest || descriptor.ArtifactType != "" || seenManifests[descriptor.Digest] ||
			(requirePlatform && descriptor.Platform == nil) {
			return nil, e.New(e.CodeVerifyFailed, "runnable image index has a duplicate or non-image descriptor or missing platform")
		}
		seenManifests[descriptor.Digest] = true
		file, _, err := closure.open(ctx, descriptor.Descriptor, 8<<20)
		if err != nil {
			return nil, err
		}
		manifestRaw, err := file.ReadAllContext(ctx, 8<<20)
		if err != nil {
			return nil, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "reading held image manifest"))
		}
		var manifest ociManifest
		if err := decodeImageJSON(manifestRaw, &manifest); err != nil {
			return nil, err
		}
		if manifest.MediaType != descriptor.MediaType {
			return nil, e.New(e.CodeVerifyFailed, "runnable image manifest media type differs from its descriptor")
		}
		if manifest.Subject != nil || manifest.Config.ArtifactType != "" {
			return nil, e.New(e.CodeVerifyFailed, "runnable image cannot be a subject-bearing artifact")
		}
		for _, layer := range manifest.Layers {
			if layer.ArtifactType != "" {
				return nil, e.New(e.CodeVerifyFailed, "runnable image layer cannot declare an artifact type")
			}
		}
		config, err := verifyImageManifestFrom(ctx, manifest, visited, closure.open)
		if err != nil {
			return nil, err
		}
		platform := config.OS + "/" + config.Architecture
		if !platforms[platform] || seenPlatforms[platform] {
			return nil, e.New(e.CodeVerifyFailed, "runnable image config has an extra or duplicate platform")
		}
		if declared := descriptor.Platform; declared != nil &&
			(declared.OS != config.OS || declared.Architecture != config.Architecture ||
				(declared.Variant != "" && (declared.Architecture != "arm64" || declared.Variant != "v8"))) {
			return nil, e.New(e.CodeVerifyFailed, "runnable image descriptor platform differs from its config")
		}
		seenPlatforms[platform], visited[descriptor.Digest] = true, true
		children = append(children, ImageIndexChild{Platform: platform, ManifestDigest: descriptor.Digest,
			Size: descriptor.Size, Entrypoint: config.Config.Entrypoint, Command: config.Config.Cmd})
	}
	if len(visited) != len(closure.blobs) {
		return nil, e.New(e.CodeVerifyFailed, "runnable image closure contains unreachable blobs")
	}
	sort.Slice(children, func(i, j int) bool { return children[i].Platform < children[j].Platform })
	return children, nil
}

func captureImageClosure(ctx context.Context, root string) (*imageIndexClosure, error) {
	if err := deliveryContext(ctx); err != nil {
		return nil, err
	}
	directory, _, err := securepath.OpenDirectory(root)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "opening runnable image layout")
	}
	defer directory.Close()
	if err := exactImageDirectory(directory, []string{"blobs", "index.json", "oci-layout"}); err != nil {
		return nil, err
	}
	marker, err := captureImageFile(ctx, directory, "oci-layout", "", 4096)
	if err != nil {
		return nil, err
	}
	markerRaw, err := marker.ReadAllContext(ctx, 4096)
	_ = marker.Close()
	if err != nil {
		return nil, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "reading OCI layout marker"))
	}
	var version struct {
		Version string `json:"imageLayoutVersion"`
	}
	if err := decodeImageJSON(markerRaw, &version); err != nil {
		return nil, err
	}
	if version.Version != "1.0.0" {
		return nil, e.New(e.CodeVerifyFailed, "unsupported runnable image layout version")
	}
	closure := &imageIndexClosure{blobs: make(map[string]*heldfile.File)}
	complete := false
	defer func() {
		if !complete {
			closure.close()
		}
	}()
	closure.index, err = captureImageFile(ctx, directory, "index.json", "", 8<<20)
	if err != nil {
		return nil, err
	}
	blobs, _, err := securepath.OpenChild(directory, "blobs", true)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "opening image blob directory")
	}
	defer blobs.Close()
	if err := exactImageDirectory(blobs, []string{"sha256"}); err != nil {
		return nil, err
	}
	shaDirectory, _, err := securepath.OpenChild(blobs, "sha256", true)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "opening image SHA-256 directory")
	}
	defer shaDirectory.Close()
	entries, err := shaDirectory.ReadDir(maxDeliveryObjects*8 + 1)
	if err != nil && err != io.EOF {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "listing image blobs")
	}
	if len(entries) == 0 || len(entries) > maxDeliveryObjects*8 {
		return nil, e.New(e.CodeVerifyFailed, "image closure has an invalid object count")
	}
	for _, entry := range entries {
		digest := "sha256:" + entry.Name()
		if !isDigest(digest) || closure.blobs[digest] != nil || closure.bytes >= maxDeliveryFileBytes {
			return nil, e.New(e.CodeVerifyFailed, "image closure has an invalid or duplicate blob or exceeds byte bounds")
		}
		file, err := captureImageFile(ctx, shaDirectory, entry.Name(), digest, maxDeliveryFileBytes-closure.bytes)
		if err != nil {
			return nil, err
		}
		closure.blobs[digest] = file
		closure.bytes += file.Size()
	}
	complete = true
	return closure, nil
}

func captureImageFile(ctx context.Context, directory *os.File, name, digest string, maximum int64) (*heldfile.File, error) {
	if err := deliveryContext(ctx); err != nil {
		return nil, err
	}
	file, info, err := securepath.OpenChild(directory, name, false)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "opening image file %q", name)
	}
	defer file.Close()
	if info.Size() < 0 || info.Size() > maximum {
		return nil, e.New(e.CodeVerifyFailed, "image file %q exceeds byte bounds", name)
	}
	held, err := heldfile.FromReaderContext(ctx, file, heldfile.Options{Label: "image-index-" + name, MaxBytes: maximum, ExpectedDigest: digest})
	if err != nil {
		return nil, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "capturing image file %q", name))
	}
	return held, nil
}

func exactImageDirectory(directory *os.File, expected []string) error {
	entries, err := directory.ReadDir(len(expected) + 1)
	if err != nil && err != io.EOF {
		return e.Wrap(e.CodeVerifyFailed, err, "listing image layout directory")
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	sort.Strings(names)
	if strings.Join(names, "\x00") != strings.Join(expected, "\x00") {
		return e.New(e.CodeVerifyFailed, "image layout directory has missing or extra entries")
	}
	return nil
}
