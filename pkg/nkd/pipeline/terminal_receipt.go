package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"golang.org/x/sys/unix"
)

const terminalReceiptDomain = canonical.KindTerminalVerification

const (
	maxTerminalTreeEntries = 100_000
	maxTerminalInlineBytes = 256 << 20
)

// terminalVerificationReceipt is deliberately package-private and has no
// constructor accepting declarations. It is minted only by issueTerminalReceipt
// after the terminal verifier has re-opened the selected graph's real artifacts.
type terminalVerificationReceipt struct {
	valid       bool
	binding     string
	producerIDs []string
	artifacts   []terminalArtifact
}

type terminalArtifact struct {
	node       ir.Node
	result     ArtifactResult
	sourceRoot string
	root       string
	relative   string
	fileInfo   os.FileInfo
	tree       *terminalHeldTree
}

type terminalTreeEntry struct {
	relative string
	info     os.FileInfo
	digest   string
}

type terminalArtifactView struct {
	NodeID     string         `json:"nodeId"`
	Result     ArtifactResult `json:"result"`
	LiveRoot   string         `json:"liveRoot,omitempty"`
	LivePath   string         `json:"livePath,omitempty"`
	TreeDigest string         `json:"treeDigest,omitempty"`
}

type terminalReceiptView struct {
	BuildDigest string                 `json:"buildDigest"`
	Nodes       []NodeResult           `json:"nodes"`
	Artifacts   []terminalArtifactView `json:"artifacts"`
}

func issueTerminalReceipt(
	ctx context.Context,
	report *Report,
	producers map[string]ir.Node,
	included map[string]bool,
	opts RunOptions,
) (terminalVerificationReceipt, error) {
	var zero terminalVerificationReceipt
	if ctx == nil {
		return zero, e.New(e.CodeUsage, "terminal verification requires a non-nil context")
	}
	if report == nil {
		return zero, e.New(e.CodeVerifyFailed, "terminal verification received no report")
	}
	byComponent := make(map[string]ArtifactResult, len(report.Artifacts))
	for _, artifact := range report.Artifacts {
		if artifact.Component == "" {
			return zero, e.New(e.CodeVerifyFailed, "terminal result contains an empty component")
		}
		if _, duplicate := byComponent[artifact.Component]; duplicate {
			return zero, e.New(e.CodeVerifyFailed, "terminal result contains duplicate component %q", artifact.Component)
		}
		byComponent[artifact.Component] = artifact
	}
	producerIDs := make([]string, 0, len(producers))
	for id := range producers {
		if included[id] {
			producerIDs = append(producerIDs, id)
		}
	}
	sort.Strings(producerIDs)
	if len(byComponent) != len(producerIDs) {
		return zero, e.New(e.CodeVerifyFailed,
			"terminal result set has %d artifacts, want exact selected producer set of %d", len(byComponent), len(producerIDs))
	}

	receipt := terminalVerificationReceipt{producerIDs: append([]string(nil), producerIDs...)}
	for _, id := range producerIDs {
		if err := ctx.Err(); err != nil {
			return zero, e.Wrap(e.CodeTimeout, err, "terminal artifact verification canceled")
		}
		node := producers[id]
		artifact, ok := byComponent[node.Component]
		if !ok {
			return zero, e.New(e.CodeVerifyFailed, "producer %q produced no terminal artifact", id)
		}
		if err := validateTerminalDeclaration(node, artifact, opts); err != nil {
			return zero, err
		}
		verified, err := captureTerminalArtifact(ctx, node, artifact, opts)
		if err != nil {
			if ctx.Err() != nil {
				return zero, mapCancellation(ctx, err, "terminal artifact verification")
			}
			return zero, e.Wrap(e.CodeVerifyFailed, err, "terminally verifying %s", node.Component)
		}
		if err := ctx.Err(); err != nil {
			return zero, e.Wrap(e.CodeTimeout, err, "terminal artifact verification canceled")
		}
		receipt.artifacts = append(receipt.artifacts, verified)
	}
	binding, err := terminalReceiptBinding(report, receipt.artifacts)
	if err != nil {
		return zero, err
	}
	receipt.binding = binding
	receipt.valid = true
	return receipt, nil
}

func validateTerminalDeclaration(node ir.Node, artifact ArtifactResult, opts RunOptions) error {
	if artifact.Component != node.Component || artifact.OutputType != node.OutputType {
		return e.New(e.CodeVerifyFailed, "artifact %q does not match producer %q", artifact.Component, node.ID)
	}
	switch node.Kind {
	case ir.KindBuild:
		if artifact.Toolchain != node.Toolchain || artifact.BuilderType != node.BuilderType || artifact.InputDigest == "" {
			return e.New(e.CodeVerifyFailed, "build artifact %q does not match its pinned build declaration", node.Component)
		}
		if !artifact.Packaged && artifact.Path != node.ArtifactPath {
			return e.New(e.CodeVerifyFailed, "build artifact %q path does not match its graph declaration", node.Component)
		}
		if opts.execution != nil {
			identity, ok := observedNodeIdentity(opts.execution, node.ID)
			if !ok || artifact.Qualification != ArtifactQualifiedExecution || artifact.EngineClass != identity.EngineClass ||
				artifact.ProfileDigest != opts.execution.receipt.ProfileDigest() ||
				artifact.ExecutionBinding != opts.execution.receipt.ExecutionBinding() {
				return e.New(e.CodeVerifyFailed, "build artifact %q lacks the exact selected execution identity", node.Component)
			}
		}
	case ir.KindPull:
		if artifact.Qualification != ArtifactProfileFreeAcquisition || artifact.BuilderType != "" ||
			artifact.ProfileDigest != "" || artifact.ExecutionBinding != "" ||
			artifact.EngineClass != ir.EnginePrebuiltAcquire || artifact.SHA256 != node.Digest {
			return e.New(e.CodeVerifyFailed, "prebuilt artifact %q is not the exact graph-selected acquisition", node.Component)
		}
		if node.OutputType == "oci-image" && artifact.Ref != node.Artifact+"@"+node.Digest {
			return e.New(e.CodeVerifyFailed, "prebuilt artifact %q does not retain its digest-pinned reference", node.Component)
		}
	default:
		return e.New(e.CodeInternal, "node %q is not an artifact producer", node.ID)
	}
	return nil
}

func captureTerminalArtifact(ctx context.Context, node ir.Node, artifact ArtifactResult, opts RunOptions) (terminalArtifact, error) {
	captured := terminalArtifact{node: node, result: artifact}
	switch {
	case artifact.Ref != "":
		captured.sourceRoot = artifact.TransportPath
		snapshot, err := snapshotRegularTreeContext(ctx, captured.sourceRoot)
		if err != nil {
			return terminalArtifact{}, err
		}
		captured.tree = snapshot
		_, want, err := parseDigestReference(artifact.Ref)
		if err != nil {
			return terminalArtifact{}, err
		}
		if err := verifyOCILayoutSource(ctx, snapshot, want); err != nil {
			return terminalArtifact{}, err
		}
		return captured, nil
	case artifact.Packaged && artifact.OutputType == "oci-image":
		captured.sourceRoot = artifact.Path
		snapshot, err := snapshotRegularTreeContext(ctx, captured.sourceRoot)
		if err != nil {
			return terminalArtifact{}, err
		}
		captured.tree = snapshot
		if err := verifyOCILayoutSource(ctx, snapshot, artifact.SHA256); err != nil {
			return terminalArtifact{}, err
		}
		return captured, nil
	}

	root, expectedPath, err := trustedArtifactPath(node, artifact, opts)
	if err != nil {
		return terminalArtifact{}, err
	}
	if artifact.AbsPath != expectedPath {
		return terminalArtifact{}, fmt.Errorf("artifact %s live path %q does not match graph-derived path %q", node.Component, artifact.AbsPath, expectedPath)
	}
	relative, err := relativeBeneath(root, expectedPath)
	if err != nil {
		return terminalArtifact{}, err
	}
	digest, size, info, err := hashRegularBeneathContext(ctx, root, relative)
	if err != nil {
		return terminalArtifact{}, err
	}
	if digest != artifact.SHA256 || size != artifact.Size {
		return terminalArtifact{}, fmt.Errorf("artifact %s bytes are %s/%d, want %s/%d", node.Component, digest, size, artifact.SHA256, artifact.Size)
	}
	captured.sourceRoot, captured.root, captured.relative, captured.fileInfo = root, root, relative, info
	return captured, nil
}

func trustedArtifactPath(node ir.Node, artifact ArtifactResult, opts RunOptions) (string, string, error) {
	switch node.Kind {
	case ir.KindBuild:
		if !artifact.Packaged {
			return privOutDir(opts, node), artifactHostPath(opts, node), nil
		}
		root := opts.PackageDir
		if artifact.OutputType == "host-package" {
			want := filepath.Join(root, fmt.Sprintf("%s_%s_%s.deb", node.Component, orVersion(node.Version), debArch(node.Platform)))
			return root, want, nil
		}
		return "", "", fmt.Errorf("unsupported packaged artifact type %q", artifact.OutputType)
	case ir.KindPull:
		root := prebuiltDir(opts, node)
		name := node.ArtifactPath
		if name == "" {
			name = node.Component
		}
		want, err := pathWithin(root, name)
		return root, want, err
	default:
		return "", "", fmt.Errorf("node %q is not an artifact producer", node.ID)
	}
}

func (receipt terminalVerificationReceipt) require(ctx context.Context, report *Report) error {
	if !receipt.valid || receipt.binding == "" {
		return e.New(e.CodePreflightFailed, "report lacks an opaque terminal verification receipt")
	}
	binding, err := terminalReceiptBinding(report, receipt.artifacts)
	if err != nil {
		return err
	}
	if binding != receipt.binding {
		return e.New(e.CodeVerifyFailed, "report changed after terminal verification")
	}
	for _, artifact := range receipt.artifacts {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "qualified artifact revalidation canceled")
		}
		if artifact.tree != nil {
			if artifact.result.Ref != "" {
				_, want, err := parseDigestReference(artifact.result.Ref)
				if err != nil {
					return err
				}
				if err := verifyOCILayoutSource(ctx, artifact.tree, want); err != nil {
					if ctx.Err() != nil {
						return mapCancellation(ctx, err, "qualified artifact revalidation")
					}
					return e.Wrap(e.CodeVerifyFailed, err, "revalidating OCI artifact %s", artifact.node.Component)
				}
			} else if err := verifyOCILayoutSource(ctx, artifact.tree, artifact.result.SHA256); err != nil {
				if ctx.Err() != nil {
					return mapCancellation(ctx, err, "qualified artifact revalidation")
				}
				return e.Wrap(e.CodeVerifyFailed, err, "revalidating OCI artifact %s", artifact.node.Component)
			}
			continue
		}
		digest, size, info, err := hashRegularBeneathContext(ctx, artifact.root, artifact.relative)
		if err != nil || !os.SameFile(artifact.fileInfo, info) || digest != artifact.result.SHA256 || size != artifact.result.Size {
			if ctx.Err() != nil {
				return e.Wrap(e.CodeTimeout, ctx.Err(), "qualified artifact revalidation canceled")
			}
			return e.New(e.CodeVerifyFailed, "artifact %q changed after terminal verification", artifact.node.Component)
		}
	}
	return nil
}

func terminalReceiptBinding(report *Report, captured []terminalArtifact) (string, error) {
	byComponent := make(map[string]ArtifactResult, len(report.Artifacts))
	for _, artifact := range report.Artifacts {
		if _, exists := byComponent[artifact.Component]; exists {
			return "", e.New(e.CodeVerifyFailed, "report contains duplicate component %q", artifact.Component)
		}
		byComponent[artifact.Component] = artifact
	}
	views := make([]terminalArtifactView, 0, len(captured))
	for _, artifact := range captured {
		current, ok := byComponent[artifact.node.Component]
		if !ok {
			return "", e.New(e.CodeVerifyFailed, "report omits terminal artifact %q", artifact.node.Component)
		}
		delete(byComponent, artifact.node.Component)
		treeDigest := ""
		if artifact.tree != nil {
			treeDigest = artifact.tree.digest
		}
		views = append(views, terminalArtifactView{
			NodeID: artifact.node.ID, Result: current,
			LiveRoot: artifact.sourceRoot, LivePath: current.AbsPath, TreeDigest: treeDigest,
		})
	}
	if len(byComponent) != 0 {
		return "", e.New(e.CodeVerifyFailed, "report contains artifacts outside the terminal producer selection")
	}
	return canonical.Digest(terminalReceiptDomain, terminalReceiptView{
		BuildDigest: report.BuildDigest,
		Nodes:       append([]NodeResult(nil), report.Nodes...), Artifacts: views,
	})
}

type terminalHeldFile struct {
	digest string
	size   int64
	inline bool
	raw    []byte
}

type terminalHeldTree struct {
	files       map[string]terminalHeldFile
	dirs        map[string]bool
	digest      string
	inlineBytes int64
}

type terminalHeldTreeView struct {
	SchemaVersion string                      `json:"schemaVersion"`
	Entries       []terminalHeldTreeEntryView `json:"entries"`
}

type terminalHeldTreeEntryView struct {
	Path   string `json:"path"`
	Type   string `json:"type"`
	Mode   string `json:"mode"`
	Digest string `json:"digest,omitempty"`
	Size   int64  `json:"size,omitempty"`
}

// snapshotRegularTree turns a producer-controlled tree into immutable in-process
// facts while walking only already-open directory descriptors. Large blobs are
// retained as their independently computed digest and size; bounded OCI documents
// are defensively copied into private memory for semantic parsing. No retained
// pathname exists for a same-UID process to rename, replace, or temporarily swap.
func snapshotRegularTree(root string) (*terminalHeldTree, error) {
	return snapshotRegularTreeContext(context.Background(), root)
}

func snapshotRegularTreeContext(ctx context.Context, root string) (*terminalHeldTree, error) {
	if ctx == nil {
		return nil, fmt.Errorf("artifact-tree capture requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source, sourceInfo, err := securepath.OpenDirectory(root)
	if err != nil {
		return nil, fmt.Errorf("artifact tree %s is not a real directory: %w", root, err)
	}
	defer source.Close()
	tree := &terminalHeldTree{files: map[string]terminalHeldFile{}, dirs: map[string]bool{}}
	state, views, err := captureTerminalDirectoryContext(ctx, source, sourceInfo, "", tree)
	if err != nil {
		return nil, err
	}

	// Rewalk the same held root inode after copying. This detects additions,
	// removals, replacements, and in-place content changes during the snapshot.
	reopened, reopenedInfo, err := reopenDirectoryDescriptor(source)
	if err != nil {
		return nil, err
	}
	after, captureErr := captureRegularTreeOpenContext(ctx, reopened, reopenedInfo)
	closeErr := reopened.Close()
	if captureErr != nil {
		return nil, captureErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if !sameTreeState(state, after) {
		return nil, fmt.Errorf("artifact tree %s changed while being captured", root)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Path < views[j].Path })
	tree.digest, err = canonical.Digest(canonical.KindTerminalTree, terminalHeldTreeView{
		SchemaVersion: canonical.KindTerminalTree, Entries: views,
	})
	if err != nil {
		return nil, err
	}
	return tree, nil
}

func reopenDirectoryDescriptor(directory *os.File) (*os.File, os.FileInfo, error) {
	if directory == nil {
		return nil, nil, fmt.Errorf("cannot reopen a nil directory descriptor")
	}
	fd, err := unix.Openat(int(directory.Fd()), ".",
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	reopened := os.NewFile(uintptr(fd), directory.Name())
	if reopened == nil {
		_ = unix.Close(fd)
		return nil, nil, fmt.Errorf("reopening directory descriptor")
	}
	info, err := reopened.Stat()
	if err != nil || !info.IsDir() {
		_ = reopened.Close()
		return nil, nil, fmt.Errorf("reopened descriptor is not a directory")
	}
	return reopened, info, nil
}

func captureTerminalDirectoryContext(ctx context.Context, source *os.File, sourceInfo os.FileInfo, prefix string, tree *terminalHeldTree) ([]terminalTreeEntry, []terminalHeldTreeEntryView, error) {
	if ctx == nil {
		return nil, nil, fmt.Errorf("artifact-tree capture requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if tree == nil || len(tree.files)+len(tree.dirs) >= maxTerminalTreeEntries {
		return nil, nil, fmt.Errorf("artifact tree exceeds its %d-entry qualification bound", maxTerminalTreeEntries)
	}
	entries := []terminalTreeEntry{{relative: ".", info: sourceInfo}}
	viewPath := "."
	if prefix != "" {
		viewPath = filepath.ToSlash(prefix)
	}
	views := []terminalHeldTreeEntryView{{Path: viewPath, Type: "directory", Mode: fmt.Sprintf("%04o", sourceInfo.Mode().Perm())}}
	tree.dirs[viewPath] = true
	children, err := readDirectoryEntriesBounded(source, maxTerminalTreeEntries)
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
	for _, child := range children {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		name := child.Name()
		if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
			return nil, nil, fmt.Errorf("unsafe artifact tree child name %q", name)
		}
		relative := name
		if prefix != "" {
			relative = filepath.Join(prefix, name)
		}
		if child.IsDir() {
			opened, info, err := securepath.OpenChild(source, name, true)
			if err != nil {
				return nil, nil, fmt.Errorf("opening artifact directory %s: %w", relative, err)
			}
			childrenState, childrenViews, captureErr := captureTerminalDirectoryContext(ctx, opened, info, relative, tree)
			after, sourceStatErr := opened.Stat()
			sourceCloseErr := opened.Close()
			if captureErr != nil {
				return nil, nil, captureErr
			}
			if sourceStatErr != nil || !securepath.SameMetadata(info, after) {
				return nil, nil, fmt.Errorf("artifact directory %s changed while being captured", relative)
			}
			if sourceCloseErr != nil {
				return nil, nil, sourceCloseErr
			}
			childrenState[0].relative = relative
			entries = append(entries, childrenState...)
			views = append(views, childrenViews...)
			continue
		}

		opened, info, err := securepath.OpenChild(source, name, false)
		if err != nil {
			return nil, nil, fmt.Errorf("artifact tree entry %s is not a regular file: %w", relative, err)
		}
		held, observed, captureErr := captureTerminalRegularContext(ctx, opened, info, relative)
		closeErr := opened.Close()
		if captureErr != nil {
			return nil, nil, captureErr
		}
		if closeErr != nil {
			return nil, nil, closeErr
		}
		slashPath := filepath.ToSlash(relative)
		if len(tree.files)+len(tree.dirs) >= maxTerminalTreeEntries {
			return nil, nil, fmt.Errorf("artifact tree exceeds its %d-entry qualification bound", maxTerminalTreeEntries)
		}
		if held.inline {
			if held.size > maxTerminalInlineBytes-tree.inlineBytes {
				return nil, nil, fmt.Errorf("artifact tree exceeds its %d-byte inline qualification bound", maxTerminalInlineBytes)
			}
			tree.inlineBytes += held.size
		}
		tree.files[slashPath] = held
		entries = append(entries, terminalTreeEntry{relative: relative, info: observed, digest: held.digest})
		views = append(views, terminalHeldTreeEntryView{
			Path: slashPath, Type: "file", Mode: fmt.Sprintf("%04o", observed.Mode().Perm()),
			Digest: held.digest, Size: held.size,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].relative < entries[j].relative })
	return entries, views, nil
}

func captureTerminalRegularContext(ctx context.Context, source *os.File, before os.FileInfo, relative string) (terminalHeldFile, os.FileInfo, error) {
	if ctx == nil {
		return terminalHeldFile{}, nil, fmt.Errorf("artifact capture requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return terminalHeldFile{}, nil, err
	}
	if source == nil || before == nil || !before.Mode().IsRegular() || before.Size() < 0 {
		return terminalHeldFile{}, nil, fmt.Errorf("artifact file %s is not a regular file", relative)
	}
	hash := sha256.New()
	var bounded bytes.Buffer
	writer := io.Writer(hash)
	if before.Size() <= maxOCIManifestBytes {
		writer = io.MultiWriter(hash, &bounded)
	}
	size, copyErr := copyPipelineContext(ctx, writer, io.LimitReader(source, before.Size()+1))
	after, statErr := source.Stat()
	if copyErr == nil && statErr == nil &&
		(!securepath.SameMetadata(before, after) || size != before.Size() || after.Size() != before.Size()) {
		copyErr = fmt.Errorf("artifact file %s changed while being captured", relative)
	}
	if copyErr != nil {
		return terminalHeldFile{}, nil, copyErr
	}
	if statErr != nil {
		return terminalHeldFile{}, nil, statErr
	}
	held := terminalHeldFile{digest: "sha256:" + hex.EncodeToString(hash.Sum(nil)), size: size}
	if size <= maxOCIManifestBytes {
		held.inline = true
		held.raw = append([]byte(nil), bounded.Bytes()...)
	}
	return held, after, nil
}

func (tree *terminalHeldTree) readRegular(ctx context.Context, relative string, limit int64) ([]byte, error) {
	if ctx == nil {
		return nil, fmt.Errorf("reading held OCI bytes requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tree == nil || limit < 0 || filepath.IsAbs(relative) ||
		filepath.ToSlash(filepath.Clean(relative)) != relative || strings.HasPrefix(relative, "../") {
		return nil, fmt.Errorf("unsafe held OCI path %q", relative)
	}
	file, ok := tree.files[relative]
	if !ok || file.size > limit || !file.inline || int64(len(file.raw)) != file.size {
		return nil, fmt.Errorf("held OCI file %s is unavailable within the %d-byte limit", relative, limit)
	}
	return append([]byte(nil), file.raw...), nil
}

func (tree *terminalHeldTree) blobInventory(ctx context.Context) (map[string]int64, error) {
	if tree == nil {
		return nil, fmt.Errorf("held OCI tree is nil")
	}
	if len(tree.dirs) != 3 || !tree.dirs["."] || !tree.dirs["blobs"] || !tree.dirs["blobs/sha256"] {
		return nil, fmt.Errorf("held OCI layout has an unexpected directory set")
	}
	if _, ok := tree.files["oci-layout"]; !ok {
		return nil, fmt.Errorf("held OCI layout omits oci-layout")
	}
	if _, ok := tree.files["index.json"]; !ok {
		return nil, fmt.Errorf("held OCI layout omits index.json")
	}
	actual := make(map[string]int64, len(tree.files)-2)
	for path, file := range tree.files {
		if err := ctx.Err(); err != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "offline OCI verification canceled")
		}
		if path == "oci-layout" || path == "index.json" {
			continue
		}
		const prefix = "blobs/sha256/"
		name := strings.TrimPrefix(path, prefix)
		if name == path || len(name) != 64 || strings.ContainsRune(name, '/') || file.digest != "sha256:"+name {
			return nil, fmt.Errorf("held OCI layout contains unexpected or misnamed file %s", path)
		}
		actual[file.digest] = file.size
	}
	return actual, nil
}

func captureRegularTreeContext(ctx context.Context, root string) ([]terminalTreeEntry, error) {
	if ctx == nil {
		return nil, fmt.Errorf("artifact-tree verification requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rootDir, rootInfo, err := securepath.OpenDirectory(root)
	if err != nil {
		return nil, fmt.Errorf("artifact tree %s is not a real directory: %w", root, err)
	}
	defer rootDir.Close()
	return captureRegularTreeOpenContext(ctx, rootDir, rootInfo)
}

func captureRegularTreeOpenContext(ctx context.Context, rootDir *os.File, rootInfo os.FileInfo) ([]terminalTreeEntry, error) {
	if ctx == nil {
		return nil, fmt.Errorf("artifact-tree verification requires a non-nil context")
	}
	entries := []terminalTreeEntry{{relative: ".", info: rootInfo}}
	var walk func(*os.File, string) error
	walk = func(directory *os.File, prefix string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		children, err := readDirectoryEntriesBounded(directory, maxTerminalTreeEntries)
		if err != nil {
			return err
		}
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
		for _, child := range children {
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(entries) >= maxTerminalTreeEntries {
				return fmt.Errorf("artifact tree exceeds its %d-entry qualification bound", maxTerminalTreeEntries)
			}
			name := child.Name()
			relative := name
			if prefix != "" {
				relative = filepath.Join(prefix, name)
			}
			if child.IsDir() {
				opened, info, err := securepath.OpenChild(directory, name, true)
				if err != nil {
					return fmt.Errorf("opening artifact directory %s: %w", relative, err)
				}
				entries = append(entries, terminalTreeEntry{relative: relative, info: info})
				err = walk(opened, relative)
				closeErr := opened.Close()
				if err != nil {
					return err
				}
				if closeErr != nil {
					return closeErr
				}
				continue
			}
			opened, info, err := securepath.OpenChild(directory, name, false)
			if err != nil {
				return fmt.Errorf("artifact tree entry %s is not a regular file: %w", relative, err)
			}
			digest, _, observed, hashErr := hashOpenRegularContext(ctx, opened, info)
			closeErr := opened.Close()
			if hashErr != nil {
				return hashErr
			}
			if closeErr != nil {
				return closeErr
			}
			entries = append(entries, terminalTreeEntry{relative: relative, info: observed, digest: digest})
		}
		return nil
	}
	err := walk(rootDir, "")
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].relative < entries[j].relative })
	return entries, nil
}

func readDirectoryEntriesBounded(directory *os.File, maxEntries int) ([]os.DirEntry, error) {
	entries := make([]os.DirEntry, 0)
	for {
		batch, err := directory.ReadDir(1024)
		entries = append(entries, batch...)
		if len(entries) > maxEntries {
			return nil, fmt.Errorf("directory exceeds its %d-entry qualification bound", maxEntries)
		}
		if err == io.EOF {
			return entries, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func sameTreeState(want, got []terminalTreeEntry) bool {
	if len(want) != len(got) {
		return false
	}
	for i := range want {
		if want[i].relative != got[i].relative || want[i].digest != got[i].digest ||
			want[i].info == nil || got[i].info == nil || !os.SameFile(want[i].info, got[i].info) ||
			want[i].info.Mode() != got[i].info.Mode() || want[i].info.Size() != got[i].info.Size() {
			return false
		}
	}
	return true
}
