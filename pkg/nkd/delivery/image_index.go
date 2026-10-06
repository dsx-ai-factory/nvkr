package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

// ImageIndexInput describes an exact single-platform image to capture. A path,
// platform or digest alone is not evidence; assembly verifies the held closure.
type ImageIndexInput struct {
	Platform       string
	LayoutPath     string
	ManifestDigest string
}

// ImageIndexChild describes one checked runnable image. It grants no execution,
// signer or release authority and cannot be submitted as an assembly receipt.
type ImageIndexChild struct {
	Platform       string
	ManifestDigest string
	Size           int64
	Entrypoint     []string
	Command        []string
}

// ImageIndexDescription describes exact OCI bytes, distinct from logical Build
// identity. Children are sorted by platform and returned as defensive copies.
type ImageIndexDescription struct {
	Digest   string
	Size     int64
	Children []ImageIndexChild
}

// ImageIndex owns an immutable, verified runnable OCI index and its complete
// child closure. Close releases its anonymous files and joins active operations.
// This capability proves integrity only; producers and release consumers must
// independently authenticate each child's build, output and evidence membership.
type ImageIndex struct {
	*imageIndexState
}

// Every shallow handle copy shares revocation and synchronization.
type imageIndexState struct {
	mu          sync.Mutex
	operation   chan struct{}
	closed      bool
	closure     *imageIndexClosure
	description ImageIndexDescription
}

type runnablePlatform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant,omitempty"`
}

type runnableDescriptor struct {
	Descriptor
	Platform *runnablePlatform `json:"platform,omitempty"`
}

type runnableIndex struct {
	SchemaVersion int                  `json:"schemaVersion"`
	MediaType     string               `json:"mediaType"`
	ArtifactType  string               `json:"artifactType,omitempty"`
	Manifests     []runnableDescriptor `json:"manifests"`
	Annotations   map[string]string    `json:"annotations,omitempty"`
}

// AssembleImageIndex captures exact single-manifest images and emits a canonical
// index over the requested finite platform set. Child manifests, configs and
// layers are retained unchanged. Supported platforms are linux/amd64 and
// linux/arm64 (baseline v8); no host inference or first-entry selection is used.
// The entire request is admitted before input filesystem access. No output path
// is created until the caller invokes WriteLayout on the returned capability.
func AssembleImageIndex(ctx context.Context, requiredPlatforms []string, inputs []ImageIndexInput) (*ImageIndex, error) {
	platforms, err := admitImagePlatforms(ctx, requiredPlatforms)
	if err != nil {
		return nil, err
	}
	selected := make(map[string]ImageIndexInput, len(inputs))
	seenDigests := make(map[string]bool, len(inputs))
	if len(inputs) != len(platforms) {
		return nil, e.New(e.CodeUsage, "image index inputs must exactly cover required platforms")
	}
	for _, input := range inputs {
		if !platforms[input.Platform] || selected[input.Platform].Platform != "" ||
			strings.TrimSpace(input.LayoutPath) == "" || !isDigest(input.ManifestDigest) || seenDigests[input.ManifestDigest] {
			return nil, e.New(e.CodeUsage, "image index input has an extra, duplicate or invalid platform, path or manifest identity")
		}
		selected[input.Platform] = input
		seenDigests[input.ManifestDigest] = true
	}
	closure := &imageIndexClosure{blobs: make(map[string]*heldfile.File)}
	complete := false
	defer func() {
		if !complete {
			closure.close()
		}
	}()
	index := runnableIndex{SchemaVersion: 2, MediaType: MediaOCIIndex}
	var children []ImageIndexChild
	for _, platform := range sortedPlatformNames(platforms) {
		input := selected[platform]
		child, err := captureImageClosure(ctx, input.LayoutPath)
		if err != nil {
			return nil, err
		}
		checked, err := child.verify(ctx, map[string]bool{platform: true}, false)
		if err != nil {
			child.close()
			return nil, err
		}
		if checked[0].ManifestDigest != input.ManifestDigest {
			child.close()
			return nil, e.New(e.CodeVerifyFailed, "image index child differs from its required manifest identity")
		}
		children = append(children, checked[0])
		parts := strings.Split(platform, "/")
		index.Manifests = append(index.Manifests, runnableDescriptor{
			Descriptor: Descriptor{MediaType: MediaOCIManifest, Digest: checked[0].ManifestDigest, Size: checked[0].Size},
			Platform:   &runnablePlatform{OS: parts[0], Architecture: parts[1]},
		})
		err = closure.merge(child)
		child.close()
		if err != nil {
			return nil, err
		}
	}
	raw, err := canonical.Marshal(index)
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "encoding runnable image index")
	}
	closure.index, err = heldfile.FromBytesContext(ctx, raw, heldfile.Options{Label: "runnable-image-index", MaxBytes: 8 << 20})
	if err != nil {
		return nil, classifyContext(ctx, e.Wrap(e.CodeInternal, err, "holding runnable image index"))
	}
	complete = true
	return newImageIndex(closure, children), nil
}

// OpenImageIndex captures a standard runnable OCI index at an exact byte digest
// and verifies exactly the caller's required platforms and all reachable blobs.
// Unlike the single-image input to assembly, every child descriptor must carry
// its platform. This operation neither resolves tags nor authenticates signers.
func OpenImageIndex(ctx context.Context, root, expectedIndexDigest string, requiredPlatforms []string) (*ImageIndex, error) {
	platforms, err := admitImagePlatforms(ctx, requiredPlatforms)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(root) == "" || !isDigest(expectedIndexDigest) {
		return nil, e.New(e.CodeUsage, "image index opening requires a path and exact index digest")
	}
	closure, err := captureImageClosure(ctx, root)
	if err != nil {
		return nil, err
	}
	if closure.index.Digest() != expectedIndexDigest {
		closure.close()
		return nil, e.New(e.CodeVerifyFailed, "runnable image index differs from its required digest")
	}
	children, err := closure.verify(ctx, platforms, true)
	if err != nil {
		closure.close()
		return nil, err
	}
	return newImageIndex(closure, children), nil
}

func newImageIndex(closure *imageIndexClosure, children []ImageIndexChild) *ImageIndex {
	return &ImageIndex{imageIndexState: &imageIndexState{operation: make(chan struct{}, 1), closure: closure, description: ImageIndexDescription{
		Digest: closure.index.Digest(), Size: closure.index.Size(), Children: children,
	}}}
}

// Description returns only defensive metadata from the verified held closure.
func (index *ImageIndex) Description() (ImageIndexDescription, error) {
	if index == nil {
		return ImageIndexDescription{}, e.New(e.CodeUsage, "image index capability is nil")
	}
	if index.imageIndexState == nil {
		return ImageIndexDescription{}, e.New(e.CodeVerifyFailed, "image index capability was not issued by delivery")
	}
	index.mu.Lock()
	defer index.mu.Unlock()
	if err := index.usable(); err != nil {
		return ImageIndexDescription{}, err
	}
	result := index.description
	result.Children = append([]ImageIndexChild(nil), result.Children...)
	for i := range result.Children {
		result.Children[i].Entrypoint = append([]string(nil), result.Children[i].Entrypoint...)
		result.Children[i].Command = append([]string(nil), result.Children[i].Command...)
	}
	return result, nil
}

// WriteLayout durably writes held bytes and atomically installs a new OCI layout.
// The destination parent must already exist as a real directory. An existing
// destination is never replaced, including under competing writers.
// Cancellation before commit leaves no partial destination. A failed directory
// sync after commit reports failure even though the complete layout may exist.
func (index *ImageIndex) WriteLayout(ctx context.Context, out string) error {
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	if index == nil || strings.TrimSpace(out) == "" {
		return e.New(e.CodeUsage, "image index writing requires a capability and output path")
	}
	if index.imageIndexState == nil || index.operation == nil {
		return e.New(e.CodeVerifyFailed, "image index capability was not issued by delivery")
	}
	select {
	case <-ctx.Done():
		return e.Wrap(e.CodeTimeout, ctx.Err(), "image index write canceled while waiting for an active operation")
	case index.operation <- struct{}{}:
	}
	defer func() { <-index.operation }()
	// A completed operation and cancellation may become ready together. Recheck
	// after acquiring the gate, before observing or writing any held bytes.
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	index.mu.Lock()
	err := index.usable()
	closure := index.closure
	index.mu.Unlock()
	if err != nil {
		return err
	}
	return writeImageIndexLayout(ctx, out, closure)
}

func (index *ImageIndex) usable() error {
	if index.closed || index.closure == nil || index.closure.index == nil {
		return e.New(e.CodeVerifyFailed, "image index capability is closed or was not issued by delivery")
	}
	return nil
}

// Close joins active operations and releases the held closure. It is idempotent.
func (index *ImageIndex) Close() error {
	if index == nil || index.imageIndexState == nil {
		return nil
	}
	if index.operation != nil {
		index.operation <- struct{}{}
		defer func() { <-index.operation }()
	}
	index.mu.Lock()
	defer index.mu.Unlock()
	if !index.closed && index.closure != nil {
		index.closure.close()
	}
	index.closed = true
	return nil
}

func admitImagePlatforms(ctx context.Context, required []string) (map[string]bool, error) {
	if err := deliveryContext(ctx); err != nil {
		return nil, err
	}
	if len(required) == 0 || len(required) > 2 {
		return nil, e.New(e.CodeUsage, "image index requires a nonempty unique supported platform set")
	}
	result := make(map[string]bool, len(required))
	for _, platform := range required {
		if platform != "linux/amd64" && platform != "linux/arm64" {
			return nil, e.New(e.CodeEngineCapability, "image index platform %q is unsupported", platform)
		}
		if result[platform] {
			return nil, e.New(e.CodeUsage, "image index repeats required platform %q", platform)
		}
		result[platform] = true
	}
	return result, nil
}

func sortedPlatformNames(platforms map[string]bool) []string {
	result := make([]string, 0, len(platforms))
	for platform := range platforms {
		result = append(result, platform)
	}
	sort.Strings(result)
	return result
}

// External OCI JSON need not use NKD's canonical encoding. Reject ambiguous
// documents and unknown structural fields without rewriting their exact bytes.
func decodeImageJSON(raw []byte, target any) error {
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "validating runnable image JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "decoding runnable image JSON")
	}
	return nil
}
