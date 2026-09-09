package factory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/internal/heldfile"
	"github.com/nhensley/nkdctl/pkg/nkd/pipeline"
)

const (
	GeneratedManifestPath   = ".github/nkd-factory/manifest.json"
	generatedManifestSchema = "nkd.dev/factory-bundle/v2"
	maxGeneratedFileBytes   = int64(32 << 20)
)

type GeneratedManifest struct {
	SchemaVersion  string            `json:"schemaVersion"`
	Engine         string            `json:"engine"`
	WorkflowPath   string            `json:"workflowPath"`
	PlanDigest     string            `json:"planDigest"`
	WorkflowDigest string            `json:"workflowDigest"`
	Members        []GeneratedMember `json:"members"`
}

type GeneratedMember struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
}

func ManifestFor(bundle *GeneratedBundle) (GeneratedManifest, []byte, error) {
	if bundle == nil {
		return GeneratedManifest{}, nil, e.New(e.CodeUsage, "factory bundle manifest requires generated files")
	}
	if err := ValidateGenerationEngine(bundle.Engine); err != nil {
		return GeneratedManifest{}, nil, err
	}
	if !safeGeneratedPath(bundle.WorkflowPath) || !safeGeneratedPath(bundle.ManifestPath) ||
		!safeGeneratedPath(bundle.PlanPath) {
		return GeneratedManifest{}, nil, e.New(e.CodeInvalidCatalog, "generated factory bundle paths are unsafe")
	}
	workflow, ok := bundle.Files[bundle.WorkflowPath]
	if !ok || pipeline.FileDigest(workflow) != bundle.WorkflowDigest {
		return GeneratedManifest{}, nil, e.New(e.CodePipelineDrift,
			"generated %s workflow bytes do not match workflowDigest", bundle.Engine)
	}
	names := sortedFileNames(bundle.Files)
	members := make([]GeneratedMember, 0, len(names))
	for _, name := range names {
		if !safeGeneratedPath(name) || int64(len(bundle.Files[name])) > maxGeneratedFileBytes {
			return GeneratedManifest{}, nil, e.New(e.CodeInvalidCatalog, "generated factory member %q is unsafe or oversized", name)
		}
		members = append(members, GeneratedMember{Path: name, SHA256: pipeline.FileDigest(bundle.Files[name]), Mode: 0o644})
	}
	manifest := GeneratedManifest{SchemaVersion: generatedManifestSchema, Engine: bundle.Engine,
		WorkflowPath: bundle.WorkflowPath, PlanDigest: bundle.PlanDigest,
		WorkflowDigest: bundle.WorkflowDigest, Members: members}
	raw, err := canonical.Marshal(manifest)
	if err != nil {
		return GeneratedManifest{}, nil, err
	}
	return manifest, append(raw, '\n'), nil
}

// WriteGeneratedBundle transactionally commits generated payloads and writes the root
// manifest last. Re-running with identical input is idempotent.
func WriteGeneratedBundle(ctx context.Context, root string, bundle *GeneratedBundle) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "factory bundle write requires a context")
	}
	if strings.TrimSpace(root) == "" {
		return e.New(e.CodeUsage, "factory bundle write requires a repository root")
	}
	_, manifestBytes, err := ManifestFor(bundle)
	if err != nil {
		return err
	}
	if err := ensureGeneratedDirectory(root, filepath.ToSlash(filepath.Dir(bundle.ManifestPath))); err != nil {
		return err
	}
	release, err := acquireBundleLock(ctx, filepath.Join(root, bundle.ManifestPath+".lease"))
	if err != nil {
		return err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "factory bundle write canceled")
	}

	type staged struct{ destination, temporary string }
	names := sortedFileNames(bundle.Files)
	stagedFiles := make([]staged, 0, len(names)+1)
	cleanup := func() {
		for _, file := range stagedFiles {
			if file.temporary != "" {
				_ = os.Remove(file.temporary)
			}
		}
	}
	defer cleanup()
	stage := func(relative string, raw []byte) error {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "factory bundle write canceled")
		}
		destination, err := generatedDestination(root, relative)
		if err != nil {
			return err
		}
		if err := ensureGeneratedDirectory(root, filepath.Dir(relative)); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(destination), ".nkd-factory-stage-*")
		if err != nil {
			return e.Wrap(e.CodeInternal, err, "staging generated member %s", relative)
		}
		entry := staged{destination: destination, temporary: tmp.Name()}
		stagedFiles = append(stagedFiles, entry)
		if err := tmp.Chmod(0o644); err != nil {
			_ = tmp.Close()
			return err
		}
		if _, err := copyContext(ctx, tmp, bytes.NewReader(raw)); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := tmp.Sync(); err != nil {
			_ = tmp.Close()
			return err
		}
		return tmp.Close()
	}
	for _, name := range names {
		if err := stage(name, bundle.Files[name]); err != nil {
			return err
		}
	}
	if err := stage(bundle.ManifestPath, manifestBytes); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "factory bundle write canceled before commit")
	}
	// The manifest is staged last and committed last. Once payload publication starts,
	// complete the small commit even if voluntary cancellation arrives.
	for index := range stagedFiles {
		entry := &stagedFiles[index]
		if err := os.Rename(entry.temporary, entry.destination); err != nil {
			return e.Wrap(e.CodeInternal, err, "publishing generated member %s", entry.destination)
		}
		entry.temporary = ""
	}
	for _, directory := range generatedDirectories(root, append(names, bundle.ManifestPath)) {
		opened, err := os.Open(directory)
		if err != nil {
			return err
		}
		syncErr := opened.Sync()
		closeErr := opened.Close()
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

// CheckGeneratedBundle verifies exact committed bytes and manifest without mutating the
// repository. It is the regeneration/drift gate used by update automation.
func CheckGeneratedBundle(ctx context.Context, root string, bundle *GeneratedBundle) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "factory bundle check requires a context")
	}
	manifest, manifestBytes, err := ManifestFor(bundle)
	if err != nil {
		return err
	}
	for _, member := range manifest.Members {
		destination, err := generatedDestination(root, member.Path)
		if err != nil {
			return err
		}
		raw, err := readGeneratedContext(ctx, destination)
		if err != nil {
			return e.Wrap(e.CodePipelineDrift, err, "generated factory member %s is missing or unsafe", member.Path)
		}
		if !bytes.Equal(raw, bundle.Files[member.Path]) {
			return e.New(e.CodePipelineDrift, "generated factory member %s differs; regenerate it", member.Path)
		}
	}
	manifestPath, err := generatedDestination(root, bundle.ManifestPath)
	if err != nil {
		return err
	}
	committedManifest, err := readGeneratedContext(ctx, manifestPath)
	if err != nil || !bytes.Equal(committedManifest, manifestBytes) {
		return e.New(e.CodePipelineDrift, "generated factory root manifest differs; regenerate the bundle")
	}
	return nil
}

func readGeneratedContext(ctx context.Context, path string) ([]byte, error) {
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "generated factory member", MaxBytes: maxGeneratedFileBytes})
	if err != nil {
		return nil, err
	}
	defer func() { _ = held.Close() }()
	return held.ReadAllContext(ctx, maxGeneratedFileBytes)
}

func generatedDestination(root, relative string) (string, error) {
	if !safeGeneratedPath(relative) {
		return "", e.New(e.CodeInvalidCatalog, "unsafe generated factory path %q", relative)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	destination := filepath.Join(absRoot, filepath.FromSlash(relative))
	rel, err := filepath.Rel(absRoot, destination)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", e.New(e.CodeInvalidCatalog, "generated factory path %q escapes root", relative)
	}
	return destination, nil
}

func safeGeneratedPath(value string) bool {
	if value == "" || filepath.IsAbs(value) || filepath.ToSlash(filepath.Clean(value)) != value || value == "." || strings.HasPrefix(value, "../") {
		return false
	}
	return !strings.ContainsRune(value, '\x00')
}

func ensureGeneratedDirectory(root, relative string) error {
	if _, err := generatedDestination(root, filepath.ToSlash(relative)); err != nil {
		return err
	}
	current := filepath.Clean(root)
	rootInfo, err := os.Lstat(current)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return e.New(e.CodeImmutableViolation, "factory bundle root %s is not a real directory", current)
	}
	for _, component := range strings.Split(filepath.ToSlash(relative), "/") {
		if component == "." || component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o755); err != nil {
				return e.Wrap(e.CodeImmutableViolation, err, "creating generated factory directory %s", current)
			}
			info, err = os.Lstat(current)
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return e.New(e.CodeImmutableViolation, "generated factory directory %s is not a real directory", current)
		}
	}
	return nil
}

func generatedDirectories(root string, members []string) []string {
	set := map[string]bool{filepath.Clean(root): true}
	for _, member := range members {
		set[filepath.Join(root, filepath.Dir(filepath.FromSlash(member)))] = true
	}
	directories := sortedKeys(set)
	sort.Slice(directories, func(i, j int) bool {
		return strings.Count(directories[i], string(filepath.Separator)) > strings.Count(directories[j], string(filepath.Separator))
	})
	return directories
}

func copyContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, 128<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, e.Wrap(e.CodeTimeout, err, "factory bundle write canceled")
		}
		n, readErr := source.Read(buffer)
		if n > 0 {
			written, writeErr := destination.Write(buffer[:n])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}

func (manifest GeneratedManifest) String() string {
	return fmt.Sprintf("%s (%d members)", manifest.PlanDigest, len(manifest.Members))
}
