package delivery

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	"github.com/NVIDIA/nke-recipes/internal/transportarchive"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

// Publication owns an exact, independently checked publication closure. Its
// anonymous file capabilities survive source-path replacement. Only defensive
// output descriptions leave this owner; Close releases all held bytes.
// It establishes membership and integrity, not signer or release authorization.
type Publication struct {
	mu           sync.RWMutex
	closed       bool
	files        []*heldfile.File
	diskFiles    []*os.File
	diskPayloads map[string]*os.File
	inspection   Inspection
	outputs      []Output
	platforms    map[string]publicationPlatform
}

type publicationPlatform struct{ OS, Arch string }

// OpenPublication captures bounded held-byte snapshots, validates the exact root and
// every reachable descriptor/evidence binding, then checks output payload members.
// The caller's root digest is an expectation; only the checked bytes establish it.
func OpenPublication(ctx context.Context, root, expectedRoot string) (*Publication, error) {
	return openPublication(ctx, root, expectedRoot, admitPublicationDiskScratch)
}

func openPublication(ctx context.Context, root, expectedRoot string,
	admitDiskScratch func(context.Context, *os.File, uint64) error,
) (*Publication, error) {
	if err := deliveryContext(ctx); err != nil {
		return nil, err
	}
	if !isDigest(expectedRoot) {
		return nil, e.New(e.CodeVerifyFailed, "publication requires an exact expected root digest")
	}
	stage, err := os.MkdirTemp("", "nkd-publication-*")
	if err != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, err, "creating publication inspection workspace")
	}
	defer func() { _ = os.RemoveAll(stage) }()
	publication := &Publication{diskPayloads: map[string]*os.File{}}
	failed := true
	defer func() {
		if failed {
			_ = publication.Close()
		}
	}()
	layout := filepath.Join(stage, "layout")
	if err := os.MkdirAll(filepath.Join(layout, "blobs", "sha256"), 0o700); err != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, err, "creating held publication layout")
	}
	capture := func(relative, digest string, max int64) error {
		held, err := heldfile.OpenContext(ctx, filepath.Join(root, relative), heldfile.Options{Label: "publication closure", ExpectedDigest: digest, MaxBytes: max})
		if err != nil {
			return classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "capturing publication closure member"))
		}
		publication.files = append(publication.files, held)
		out, err := os.OpenFile(filepath.Join(layout, relative), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return e.Wrap(e.CodeImmutableViolation, err, "materializing held publication member")
		}
		_, writeErr := held.WriteToContext(ctx, out)
		if err := errors.Join(writeErr, out.Close()); err != nil {
			return classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "materializing held publication bytes"))
		}
		return nil
	}
	if err := capture("index.json", expectedRoot, 8<<20); err != nil {
		return nil, err
	}
	disks, err := publicationDiskPayloads(ctx, root, expectedRoot)
	if err != nil {
		return nil, err
	}
	defer func() {
		for _, disk := range disks {
			_ = disk.Close()
		}
	}()
	if len(disks) != 0 {
		var required uint64
		for _, disk := range disks {
			size := uint64(disk.Inspection().Size)
			if size > ^uint64(0)-required {
				return nil, e.New(e.CodeEngineCapability, "publication disk snapshot capacity overflows")
			}
			required += size
		}
		directory, _, err := securepath.OpenDirectory(stage)
		if err != nil {
			return nil, e.Wrap(e.CodeEngineCapability, err, "holding publication scratch filesystem")
		}
		admitErr := admitDiskScratch(ctx, directory, required)
		if err := errors.Join(admitErr, directory.Close()); err != nil {
			return nil, classifyContext(ctx, e.Wrap(e.CodeEngineCapability, err, "admitting publication disk snapshots"))
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "blobs", "sha256"))
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "opening publication blob inventory")
	}
	for _, entry := range entries {
		digest := "sha256:" + entry.Name()
		if !isDigest(digest) || !entry.Type().IsRegular() {
			return nil, e.New(e.CodeVerifyFailed, "publication contains an invalid blob inventory member")
		}
		if disk := disks[digest]; disk != nil {
			if err := (layoutWriter{root: layout}).copyDiskPayload(ctx, disk); err != nil {
				return nil, err
			}
			// Hold the private disk-backed snapshot, not another complete memfd.
			// Deferred workspace cleanup unlinks it before the owner is returned.
			file, err := os.Open(filepath.Join(layout, "blobs", "sha256", entry.Name()))
			if err != nil {
				return nil, e.Wrap(e.CodeVerifyFailed, err, "holding private disk snapshot")
			}
			publication.diskFiles = append(publication.diskFiles, file)
			publication.diskPayloads[digest] = file
			continue
		}
		if err := capture(filepath.Join("blobs", "sha256", entry.Name()), digest, maxDeliveryFileBytes); err != nil {
			return nil, err
		}
	}
	inspection, err := Inspect(ctx, layout)
	if err != nil {
		return nil, err
	}
	if inspection.RootDigest != expectedRoot || inspection.Root.Release == nil {
		return nil, e.New(e.CodeVerifyFailed, "publication has no exact release metadata")
	}
	platforms := make(map[string]publicationPlatform)
	outputs, err := inspectOutputs(ctx, layout, inspection, stage, platforms)
	if err != nil {
		return nil, err
	}
	content, err := ContentDigest(outputs)
	if err != nil {
		return nil, err
	}
	if content != inspection.Root.Release.ContentSubject {
		return nil, e.New(e.CodeVerifyFailed, "release content subject differs from exact output payloads")
	}
	publication.inspection, publication.outputs = inspection, outputs
	publication.platforms = platforms
	failed = false
	return publication, nil
}

func admitPublicationDiskScratch(ctx context.Context, directory *os.File, required uint64) error {
	available, err := securepath.AvailableDiskBackedBytes(ctx, directory)
	if err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "publication requires observed disk-backed scratch")
	}
	if available < required {
		return e.New(e.CodeEngineCapability, "publication scratch cannot hold its aggregate unique disk snapshots")
	}
	return nil
}

// PullPublication owns registry materialization and its cleanup, then returns the
// same held-byte capability used for already materialized local publications.
func PullPublication(ctx context.Context, reference, workspace string, options RegistryOptions) (*Publication, error) {
	if err := deliveryContext(ctx); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, err, "creating release verification root")
	}
	stage, err := os.MkdirTemp(workspace, ".verify-*")
	if err != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, err, "creating release verification workspace")
	}
	defer func() { _ = os.RemoveAll(stage) }()
	root := filepath.Join(stage, "layout")
	inspection, err := Pull(ctx, reference, root, options)
	if err != nil {
		return nil, err
	}
	return OpenPublication(ctx, root, inspection.RootDigest)
}

func (publication *Publication) Close() error {
	if publication == nil {
		return nil
	}
	publication.mu.Lock()
	defer publication.mu.Unlock()
	if publication.closed {
		return nil
	}
	publication.closed = true
	var result error
	for _, held := range publication.files {
		result = errors.Join(result, held.Close())
	}
	publication.files = nil
	for _, file := range publication.diskFiles {
		result = errors.Join(result, file.Close())
	}
	publication.diskFiles = nil
	publication.diskPayloads = nil
	if result != nil {
		return e.Wrap(e.CodeVerifyFailed, result, "closing publication snapshots")
	}
	return nil
}

// Artifacts projects exact named outputs, binding their transport references to
// this root. The content subject never includes those repository locators.
func (publication *Publication) Artifacts(reference string) ([]Output, string, error) {
	if publication == nil {
		return nil, "", e.New(e.CodeGateUnmet, "publication is not open")
	}
	publication.mu.RLock()
	defer publication.mu.RUnlock()
	if publication.closed || publication.inspection.Root.Release == nil {
		return nil, "", e.New(e.CodeGateUnmet, "publication is not open")
	}
	if _, digest, ok := strings.Cut(reference, "@"); ok && isDigest(digest) && digest != publication.inspection.RootDigest {
		return nil, "", e.New(e.CodeVerifyFailed, "checked publication root differs from requested subject")
	}
	references, err := OutputReferences(reference, publication.inspection)
	if err != nil {
		return nil, "", err
	}
	outputs := cloneOutputs(publication.outputs)
	for i := range outputs {
		ref, ok := references[outputs[i].Name]
		if !ok {
			return nil, "", e.New(e.CodeVerifyFailed, "publication output has no bound repository")
		}
		outputs[i].Reference = ref
	}
	return outputs, publication.inspection.Root.Release.ContentSubject, nil
}

func (publication *Publication) RequireAttestedEvidence() error {
	if publication == nil {
		return e.New(e.CodeGateUnmet, "publication is not open")
	}
	publication.mu.RLock()
	defer publication.mu.RUnlock()
	if publication.closed {
		return e.New(e.CodeGateUnmet, "publication is not open")
	}
	return RequireAttestedEvidence(publication.inspection)
}

// WriteDiskPayload streams one exact disk member from the private anonymous
// snapshot. The owner establishes byte integrity and membership only; signature,
// policy and release authorization remain independent consumer checks. A failed
// write can leave partial bytes; callers must discard their private destination.
// This streaming API is contract-tested; no production export caller uses it yet.
func (publication *Publication) WriteDiskPayload(ctx context.Context, outputName string, destination io.Writer) (int64, error) {
	if err := deliveryContext(ctx); err != nil {
		return 0, err
	}
	if publication == nil {
		return 0, e.New(e.CodeGateUnmet, "publication is not open")
	}
	if destination == nil {
		return 0, e.New(e.CodeUsage, "disk payload destination is required")
	}
	publication.mu.RLock()
	defer publication.mu.RUnlock()
	if publication.closed {
		return 0, e.New(e.CodeGateUnmet, "publication is not open")
	}
	for _, output := range publication.outputs {
		if output.Name != outputName {
			continue
		}
		if output.Kind != "disk-image" || output.Disk == nil || !isDigest(output.Disk.PayloadDigest) {
			return 0, e.New(e.CodeVerifyFailed, "requested output is not an exact disk member")
		}
		file := publication.diskPayloads[output.Disk.PayloadDigest]
		if file == nil {
			return 0, e.New(e.CodeVerifyFailed, "disk member has no held payload snapshot")
		}
		return streamDiskPayload(ctx, file, destination, output.Disk.PayloadDigest, output.Disk.Size)
	}
	return 0, e.New(e.CodeVerifyFailed, "publication has no requested disk output")
}

func inspectOutputs(ctx context.Context, layout string, inspection Inspection, stage string, platforms map[string]publicationPlatform) ([]Output, error) {
	var outputs []Output
	for _, object := range inspection.Root.Objects {
		binding := object.Output
		if binding == nil {
			continue
		} // Inspect already requires exact evidence membership for every other object.
		output := Output{Name: object.Name, Kind: binding.Format, Digest: object.DescriptorDigest, BuildDigest: binding.BuildDigest, Components: append([]string(nil), binding.Components...), Disk: cloneDiskOutput(binding.Disk)}
		if binding.Format == "apt-repository" {
			output.Digest = object.SemanticDigest
		}
		if err := output.Validate(); err != nil {
			return nil, err
		}
		members := append([]string(nil), output.Components...)
		switch output.Kind {
		case "disk-image":
			artifact, err := openDiskManifest(ctx, layout, Descriptor{MediaType: MediaOCIManifest, Digest: object.DescriptorDigest, Size: object.Size, ArtifactType: object.ArtifactType}, object.OS+"/"+object.Arch, maxDeliveryFileBytes)
			if err != nil {
				return nil, err
			}
			observed := artifact.Inspection()
			if err := artifact.Close(); err != nil {
				return nil, err
			}
			if output.Disk == nil || observed.PayloadDigest != output.Disk.PayloadDigest || observed.Size != output.Disk.Size || observed.Encryption != "none" {
				return nil, e.New(e.CodeVerifyFailed, "disk output differs from its wrapper binding")
			}
			platforms[output.Name] = publicationPlatform{OS: "linux", Arch: strings.TrimPrefix(observed.Platform, "linux/")}
		case "oci":
			if len(members) != 1 || binding.TransportPath != "" {
				return nil, e.New(e.CodeVerifyFailed, "OCI output has invalid members or transport binding")
			}
			image, err := inspectPublishedContainerImage(ctx, layout, inspection, output.Name)
			if err != nil {
				return nil, err
			}
			if image.RootDigest != output.Digest {
				return nil, e.New(e.CodeVerifyFailed, "OCI output differs from its image binding")
			}
			platforms[output.Name] = publicationPlatform{OS: image.OS, Arch: image.Arch}
		case "executable":
			bundle, err := extractBinaryBundle(ctx, layout, inspection, output.Name, filepath.Join(stage, output.Name+"-bundle"))
			if err != nil {
				return nil, err
			}
			if object.OS != "" && (object.OS != bundle.OS || object.Arch != bundle.Arch) {
				return nil, e.New(e.CodeVerifyFailed, "executable output platform differs from its exact bundle config")
			}
			platforms[output.Name] = publicationPlatform{OS: bundle.OS, Arch: bundle.Arch}
			members = nil
			for _, entry := range bundle.Entries {
				members = append(members, entry.Name)
			}
		case "apt-repository":
			expected := filepath.ToSlash(filepath.Join(".nkd", "factory", "packages", output.Name))
			if binding.TransportPath != expected {
				return nil, e.New(e.CodeVerifyFailed, "APT output has an unsafe transport binding")
			}
			archive := filepath.Join(stage, output.Name+".tar")
			if err := materializePayload(ctx, layout, object, archive); err != nil {
				return nil, err
			}
			unpacked := filepath.Join(stage, output.Name+"-repository")
			if err := transportarchive.Unpack(ctx, archive, unpacked, []string{binding.TransportPath}); err != nil {
				return nil, err
			}
			repository, err := InspectAPTRepository(ctx, filepath.Join(unpacked, filepath.FromSlash(binding.TransportPath)))
			if err != nil {
				return nil, err
			}
			if repository.Digest != output.Digest {
				return nil, e.New(e.CodeVerifyFailed, "APT output differs from its binding")
			}
			members = nil
			for _, pkg := range repository.Repository.Packages {
				members = append(members, pkg.Name)
			}
			sort.Strings(members)
		case outputFormatHelm:
			if binding.TransportPath != "" {
				return nil, e.New(e.CodeVerifyFailed, "Helm output has an unexpected transport path")
			}
		default:
			return nil, e.New(e.CodeVerifyFailed, "unsupported publication output format")
		}
		if !slices.Equal(members, output.Components) {
			return nil, e.New(e.CodeVerifyFailed, "publication output %q payload members differ from its binding", output.Name)
		}
		outputs = append(outputs, output)
	}
	sort.Slice(outputs, func(i, j int) bool { return outputs[i].Name < outputs[j].Name })
	return outputs, nil
}

// The destination is private to OpenPublication; no caller-owned path becomes
// visible. Stream large APT carriers from verified anonymous bytes.
func materializePayload(ctx context.Context, root string, object PublishedObject, destination string) error {
	manifest, err := flatManifest(ctx, root, object)
	if err != nil {
		return err
	}
	if manifest.ArtifactType != object.ArtifactType || len(manifest.Layers) != 1 || manifest.Layers[0].Digest != object.BlobDigest {
		return e.New(e.CodeVerifyFailed, "publication object has no exact single payload")
	}
	held, err := holdBlob(ctx, root, manifest.Layers[0], maxDeliveryFileBytes)
	if err != nil {
		return err
	}
	defer func() { _ = held.Close() }()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "creating private payload inspection carrier")
	}
	_, writeErr := held.WriteToContext(ctx, out)
	if err := errors.Join(writeErr, out.Close()); err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "materializing exact payload"))
	}
	return nil
}
