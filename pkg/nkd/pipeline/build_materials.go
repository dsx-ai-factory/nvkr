package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	"github.com/NVIDIA/nke-recipes/internal/transportarchive"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/safetext"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/material"
	"golang.org/x/sys/unix"
)

// BuildMaterialInput binds one manifest-relative destination to exact held
// artifact bytes. Factory lowering must derive Content from an authenticated
// producer observation; a pathname or ArtifactResult is not accepted here.
type BuildMaterialInput struct {
	Path    string
	SHA256  string
	Content *heldfile.File
}

type privateFileSnapshot struct {
	Path   string
	SHA256 string
	Mode   os.FileMode
}

// stagedBuildMaterialInputs is an internal capability for the exact manifest
// and material bytes made visible to a producer. Callers cannot construct it
// from paths or digest strings.
type stagedBuildMaterialInputs struct {
	root  string
	files []privateFileSnapshot
}

func (inputs *stagedBuildMaterialInputs) verify(ctx context.Context) error {
	if inputs == nil || inputs.root == "" {
		return e.New(e.CodeEngineCapability, "authenticated staged build-material inputs are required")
	}
	return verifyPrivateFileSnapshot(ctx, inputs.root, inputs.files, "build-material")
}

const (
	buildInputsContainerRoot = "/nkd/inputs"

	// MaxBuildMaterialBytes bounds one authenticated producer payload and the
	// complete ordinary-producer material closure. Packer transport applies its
	// own additional carrier bound independently.
	MaxBuildMaterialBytes         int64 = transportarchive.MaxArchiveBytes
	maxBuildMaterialManifestBytes int64 = 64 << 20
	maxBuildMaterialTotalBytes          = MaxBuildMaterialBytes
	maxBuildMaterialFiles               = 256
	maxBuildMaterialPathBytes           = 4096
)

// BuildMaterials is a process-local capability over one authenticated native
// material manifest and its exact held producer bytes. It borrows the held files;
// the factory observation that issued them owns and closes their lifetime.
type BuildMaterials struct {
	manifest *heldfile.File
	inputs   []BuildMaterialInput
}

// NewBuildMaterials admits held bytes into the ordinary-build boundary. It does
// not accept caller paths, and it snapshots the slice so later caller mutation
// cannot relabel an input.
func NewBuildMaterials(manifest *heldfile.File, inputs []BuildMaterialInput) (*BuildMaterials, error) {
	return newBuildMaterials(manifest, inputs, MaxBuildMaterialBytes, maxBuildMaterialTotalBytes)
}

func newBuildMaterials(
	manifest *heldfile.File,
	inputs []BuildMaterialInput,
	maxFileBytes int64,
	maxTotalBytes int64,
) (*BuildMaterials, error) {
	if manifest == nil || !canonical.IsDigest(manifest.Digest()) {
		return nil, e.New(e.CodeUnverifiedRef,
			"build materials require an authenticated manifest capability")
	}
	if len(inputs) == 0 || len(inputs) > maxBuildMaterialFiles {
		return nil, e.New(e.CodeEngineCapability,
			"build materials require between 1 and %d inputs", maxBuildMaterialFiles)
	}
	owned := append([]BuildMaterialInput(nil), inputs...)
	paths := make(map[string]struct{}, len(owned))
	var total int64
	for _, input := range owned {
		if !validBuildMaterialRelativePath(input.Path) || input.Path == "material-manifest.json" {
			return nil, e.New(e.CodeInvalidCatalog,
				"build material path %q is not bounded and relative", input.Path)
		}
		if _, duplicate := paths[input.Path]; duplicate {
			return nil, e.New(e.CodeInvalidCatalog,
				"build material path %q is duplicated", input.Path)
		}
		if input.Content == nil || !canonical.IsDigest(input.SHA256) ||
			input.Content.Digest() != input.SHA256 {
			return nil, e.New(e.CodeUnverifiedRef,
				"build material %q lacks its exact authenticated bytes", input.Path)
		}
		next, ok := addBoundedSize(total, input.Content.Size(), maxFileBytes, maxTotalBytes)
		if !ok {
			return nil, e.New(e.CodeEngineCapability,
				"build material %q exceeds the authenticated closure size bound", input.Path)
		}
		total = next
		paths[input.Path] = struct{}{}
	}
	return &BuildMaterials{manifest: manifest, inputs: owned}, nil
}

func addBoundedSize(total, size, maxFile, maxTotal int64) (int64, bool) {
	if total < 0 || size < 0 || maxFile < 0 || maxTotal < 0 || size > maxFile || size > maxTotal ||
		total > maxTotal-size {
		return 0, false
	}
	return total + size, true
}

func validBuildMaterialRelativePath(path string) bool {
	return validCleanRelativePath(path, maxBuildMaterialPathBytes)
}

func validCleanRelativePath(path string, maxBytes int) bool {
	if !safetext.Valid(path, maxBytes, false) || strings.ContainsRune(path, '\\') ||
		filepath.IsAbs(path) || filepath.Clean(path) != path || path == "." {
		return false
	}
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func verifyPrivateFileSnapshot(ctx context.Context, root string, files []privateFileSnapshot, label string) error {
	expected := make(map[string]privateFileSnapshot, len(files))
	for _, file := range files {
		expected[filepath.Clean(file.Path)] = file
	}
	seen := make(map[string]bool, len(expected))
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode().Perm()&0o222 != 0 {
				return fmt.Errorf("%s directory %q is writable", label, path)
			}
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		pin, ok := expected[relative]
		if !ok || entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s closure contains undeclared file %q", label, relative)
		}
		file, before, err := openRegularBeneath(root, relative)
		if err != nil {
			return err
		}
		digest, _, after, hashErr := hashOpenRegularContext(ctx, file, before)
		closeErr := file.Close()
		if err := errors.Join(hashErr, closeErr); err != nil {
			return err
		}
		if digest != pin.SHA256 || after.Mode().Perm() != pin.Mode {
			return fmt.Errorf("%s file %q changed", label, relative)
		}
		seen[relative] = true
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return e.Wrap(e.CodeTimeout, ctx.Err(), "verifying private %s closure", label)
		}
		return e.Wrap(e.CodeUnverifiedRef, err, "verifying private %s closure", label)
	}
	if len(seen) != len(expected) {
		return e.New(e.CodeUnverifiedRef,
			"private %s closure has %d files, want %d", label, len(seen), len(expected))
	}
	return nil
}

func sealPrivateInputDirectories(root string) error {
	directories := make([]string, 0)
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			directories = append(directories, path)
		}
		return nil
	}); err != nil {
		return err
	}
	sort.Slice(directories, func(i, j int) bool { return len(directories[i]) > len(directories[j]) })
	for _, directory := range directories {
		if err := os.Chmod(directory, 0o555); err != nil {
			return err
		}
	}
	return nil
}

func removePrivateTree(root string) error {
	if root == "" {
		return nil
	}
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, _ error) error {
		if entry != nil && entry.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
	return os.RemoveAll(root)
}

func materializeBuildMaterialManifest(ctx context.Context, manifest *heldfile.File, destination string) error {
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "creating private build-material manifest")
	}
	written, copyErr := manifest.WriteToContext(ctx, file)
	chmodErr := file.Chmod(0o444)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(copyErr, syncErr, chmodErr, closeErr); err != nil {
		if ctx.Err() != nil {
			return e.Wrap(e.CodeTimeout, ctx.Err(), "materializing build-material manifest")
		}
		return e.Wrap(e.CodeBuildFailed, err, "materializing build-material manifest")
	}
	if written != manifest.Size() {
		return e.New(e.CodeVerifyFailed,
			"materialized build-material manifest size %d does not match held size %d", written, manifest.Size())
	}
	return nil
}

func stageBuildMaterialInputs(ctx context.Context, root, manifestDigest string, inputs []BuildMaterialInput) (*stagedBuildMaterialInputs, error) {
	files, err := stageBuildMaterials(ctx, root, inputs)
	if err != nil {
		return nil, err
	}
	files = append(files, privateFileSnapshot{Path: "material-manifest.json", SHA256: manifestDigest, Mode: 0o444})
	if err := sealPrivateInputDirectories(root); err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "sealing private build-material closure")
	}
	staged := &stagedBuildMaterialInputs{root: root, files: files}
	if err := staged.verify(ctx); err != nil {
		return nil, err
	}
	return staged, nil
}

// stageBuildMaterials is the shared producer-material authority boundary. Each
// receipt-derived held capability is re-verified, copied under the private input
// root at its manifest-relative path, and re-verified before it is released.
// A consumer never receives an artifact pathname outside the private run root.
func stageBuildMaterials(ctx context.Context, root string, inputs []BuildMaterialInput) ([]privateFileSnapshot, error) {
	ordered := append([]BuildMaterialInput(nil), inputs...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	snapshots := make([]privateFileSnapshot, 0, len(ordered))
	for _, input := range ordered {
		if err := input.Content.AssertUnchangedContext(ctx); err != nil {
			if ctx.Err() != nil {
				return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "verifying build material %q", input.Path)
			}
			return nil, e.Wrap(e.CodeUnverifiedRef, err, "verifying build material %q", input.Path)
		}
		if err := materializeHeldInput(ctx, root, input.Path, input.Content); err != nil {
			if ctx.Err() != nil {
				return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "staging build material %q", input.Path)
			}
			return nil, e.Wrap(e.CodeBuildFailed, err, "staging build material %q", input.Path)
		}
		if err := input.Content.AssertUnchangedContext(ctx); err != nil {
			if ctx.Err() != nil {
				return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "re-verifying build material %q", input.Path)
			}
			return nil, e.Wrap(e.CodeUnverifiedRef, err, "re-verifying build material %q", input.Path)
		}
		snapshots = append(snapshots, privateFileSnapshot{Path: input.Path, SHA256: input.SHA256, Mode: 0o444})
	}
	return snapshots, nil
}

func materializeHeldInput(ctx context.Context, root, relative string, material *heldfile.File) error {
	parent, finalName, err := securepath.CreateParentBeneath(root, relative)
	if err != nil {
		return err
	}
	defer parent.Close()
	temporary, temporaryName, err := securepath.CreateTemporarySibling(parent)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		_ = unix.Unlinkat(int(parent.Fd()), temporaryName, 0)
	}()
	written, writeErr := material.WriteToContext(ctx, temporary)
	chmodErr := temporary.Chmod(0o444)
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	closed = true
	if err := errors.Join(writeErr, syncErr, chmodErr, closeErr); err != nil {
		return err
	}
	if written != material.Size() {
		return fmt.Errorf("staged material size %d does not match held size %d", written, material.Size())
	}
	if err := transportarchive.PublishAtNoReplace(int(parent.Fd()), temporaryName, int(parent.Fd()), finalName); err != nil {
		return err
	}
	if err := unix.Fsync(int(parent.Fd())); err != nil {
		return err
	}
	return nil
}

type stagedBuildMaterials struct {
	root           string
	manifestDigest string
}

type buildMaterialStageOps struct {
	materializeManifest func(context.Context, *heldfile.File, string) error
	stageInputs         func(context.Context, string, string, []BuildMaterialInput) (*stagedBuildMaterialInputs, error)
	removeTree          func(string) error
}

func (materials *BuildMaterials) stage(ctx context.Context, node ir.Node, parent string) (*stagedBuildMaterials, error) {
	return materials.stageWithOps(ctx, node, parent, buildMaterialStageOps{
		materializeManifest: materializeBuildMaterialManifest,
		stageInputs:         stageBuildMaterialInputs,
		removeTree:          removePrivateTree,
	})
}

func (materials *BuildMaterials) stageWithOps(
	ctx context.Context,
	node ir.Node,
	parent string,
	ops buildMaterialStageOps,
) (staged *stagedBuildMaterials, returnErr error) {
	_, raw, err := materials.validateFor(ctx, node)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "creating private build-material parent")
	}
	root, err := os.MkdirTemp(parent, ".nkd-materials-")
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "creating private build-material root")
	}
	cleanup := true
	defer func() {
		if cleanup {
			if cleanupErr := ops.removeTree(root); cleanupErr != nil {
				staged = nil
				code := e.CodeInternal
				var coded *e.Error
				if errors.As(returnErr, &coded) {
					code = coded.Code
				}
				returnErr = e.Wrap(code, errors.Join(returnErr, cleanupErr),
					"removing rejected private build-material root")
			}
		}
	}()
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "sealing private build-material root")
	}
	manifestPath := filepath.Join(root, "material-manifest.json")
	if err := ops.materializeManifest(ctx, materials.manifest, manifestPath); err != nil {
		return nil, err
	}
	if _, err := ops.stageInputs(ctx, root, materials.manifest.Digest(), materials.inputs); err != nil {
		return nil, err
	}
	cleanup = false
	return &stagedBuildMaterials{root: root, manifestDigest: FileDigest(raw)}, nil
}

func (materials *BuildMaterials) validateFor(ctx context.Context, node ir.Node) (*material.Manifest, []byte, error) {
	if materials == nil || materials.manifest == nil {
		return nil, nil, e.New(e.CodeEngineCapability,
			"build node %q requires authenticated material inputs", node.ID)
	}
	if err := materials.manifest.AssertUnchangedContext(ctx); err != nil {
		return nil, nil, e.Wrap(e.CodeUnverifiedRef, err,
			"verifying build material manifest for %q", node.ID)
	}
	raw, err := materials.manifest.ReadAllContext(ctx, maxBuildMaterialManifestBytes)
	if err != nil {
		return nil, nil, e.Wrap(e.CodeUnverifiedRef, err,
			"reading build material manifest for %q", node.ID)
	}
	if FileDigest(raw) != materials.manifest.Digest() {
		return nil, nil, e.New(e.CodeUnverifiedRef,
			"build material manifest for %q changed after authentication", node.ID)
	}
	var manifest material.Manifest
	if err := canonical.UnmarshalExact(raw, &manifest); err != nil {
		return nil, nil, e.Wrap(e.CodeInvalidCatalog, err,
			"decoding build material manifest for %q", node.ID)
	}
	if err := manifest.Validate(); err != nil {
		return nil, nil, err
	}
	if len(manifest.Materials) != len(node.Materials) || len(materials.inputs) != len(node.Materials) {
		return nil, nil, e.New(e.CodePipelineDrift,
			"build node %q material closure has the wrong cardinality", node.ID)
	}
	inputs := make(map[string]BuildMaterialInput, len(materials.inputs))
	for _, input := range materials.inputs {
		inputs[input.Path] = input
	}
	bindings := append([]ir.MaterialBinding(nil), node.Materials...)
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].Binding < bindings[j].Binding })
	architecture := strings.TrimPrefix(node.Platform, "linux/")
	for index, binding := range bindings {
		entry := manifest.Materials[index]
		path, err := material.StagingPath(binding.Binding)
		if err != nil {
			return nil, nil, err
		}
		input, ok := inputs[path]
		if entry.Binding != binding.Binding || entry.Component != binding.Component ||
			entry.Architecture != architecture || entry.Path != path || !ok ||
			entry.Digest != input.SHA256 {
			return nil, nil, e.New(e.CodePipelineDrift,
				"build node %q material binding %q does not match its authenticated input", node.ID, binding.Binding)
		}
		if err := input.Content.AssertUnchangedContext(ctx); err != nil {
			return nil, nil, e.Wrap(e.CodeUnverifiedRef, err,
				"verifying build material %q", binding.Binding)
		}
	}
	return &manifest, raw, nil
}

func (staged *stagedBuildMaterials) cleanup() error {
	if staged == nil {
		return nil
	}
	return removePrivateTree(staged.root)
}

func admitBuildMaterials(node ir.Node, options RunOptions) error {
	if len(node.Materials) == 0 {
		if options.BuildMaterials != nil {
			return e.New(e.CodePipelineDrift,
				"build node %q received undeclared material inputs", node.ID)
		}
		return nil
	}
	if node.BuilderType != ir.BuilderOCIHermetic {
		return e.New(e.CodeEngineCapability,
			"build node %q cannot consume ordinary material inputs with builder %q", node.ID, node.BuilderType)
	}
	if options.DryRun {
		return nil
	}
	if options.BuildMaterials == nil {
		return e.New(e.CodeEngineCapability,
			"build node %q requires authenticated material inputs", node.ID)
	}
	return nil
}
