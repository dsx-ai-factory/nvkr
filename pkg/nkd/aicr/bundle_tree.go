package aicr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

const (
	maxQualifiedBundleEntries   = 100_000
	maxQualifiedBundleFileBytes = int64(8 << 30)
	maxQualifiedBundleTreeBytes = int64(32 << 30)
	maxQualifiedBundleDepth     = 128
	qualifiedBundleReadBatch    = 1024
)

type bundleTree struct {
	SchemaVersion string        `json:"schemaVersion"`
	Entries       []bundleEntry `json:"entries"`
}

type bundleEntry struct {
	Path   string `json:"path"`
	Type   string `json:"type"`
	SHA256 string `json:"sha256,omitempty"`
	Mode   string `json:"mode"`
}

type heldBundleEntry struct {
	evidence bundleEntry
	file     *heldfile.File
}

// heldBundleSnapshot is the authority-bearing AICR tree representation. Its
// files are anonymous immutable capabilities captured through one descriptor-
// rooted directory, so a later rename of the staging pathname cannot change the
// bytes used for identity or publication.
type heldBundleSnapshot struct {
	entries []heldBundleEntry
	digest  string
}

func snapshotBundle(ctx context.Context, rootPath string, recipe *heldfile.File) (*heldBundleSnapshot, error) {
	return snapshotBundleTree(ctx, rootPath, bundleSnapshotOptions{
		heldRecipe: recipe,
		holdFiles:  true,
	})
}

type bundleSnapshotOptions struct {
	heldRecipe *heldfile.File
	holdFiles  bool
}

func descriptorBundleDigest(ctx context.Context, rootPath string) (string, error) {
	snapshot, err := snapshotBundleTree(ctx, rootPath, bundleSnapshotOptions{})
	if err != nil {
		return "", err
	}
	defer func() { _ = snapshot.Close() }()
	return snapshot.digest, nil
}

func snapshotBundleTree(ctx context.Context, rootPath string,
	options bundleSnapshotOptions,
) (*heldBundleSnapshot, error) {
	if err := contextError(ctx, "snapshotting AICR bundle"); err != nil {
		return nil, err
	}
	if options.holdFiles && options.heldRecipe == nil {
		return nil, e.New(e.CodeInternal, "qualified AICR bundle snapshot has no held recipe")
	}
	root, rootInfo, err := openQualifiedBundleRoot(rootPath)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err,
			"opening descriptor-rooted AICR bundle snapshot")
	}
	defer func() { _ = root.Close() }()

	snapshot := &heldBundleSnapshot{}
	fail := func(err error) (*heldBundleSnapshot, error) {
		_ = snapshot.Close()
		return nil, err
	}
	var totalBytes int64
	hasRecipe := false
	recipeMatches := false
	hasChecksums := false
	entryCount := 0
	var walk func(directory *os.File, relative string, depth int) error
	walk = func(directory *os.File, relative string, depth int) error {
		if depth > maxQualifiedBundleDepth {
			return e.New(e.CodeVerifyFailed, "AICR bundle exceeds the %d-directory depth limit",
				maxQualifiedBundleDepth)
		}
		for {
			if err := contextError(ctx, "snapshotting AICR bundle"); err != nil {
				return err
			}
			remaining := maxQualifiedBundleEntries - entryCount
			batchSize := qualifiedBundleReadBatch
			if remaining < batchSize {
				// Read one sentinel entry beyond the remaining allowance so an
				// exactly-full tree can still be distinguished from an overflow.
				batchSize = remaining + 1
			}
			children, readErr := directory.ReadDir(batchSize)
			if err := contextError(ctx, "snapshotting AICR bundle"); err != nil {
				return err
			}
			for _, child := range children {
				if entryCount >= maxQualifiedBundleEntries {
					return e.New(e.CodeVerifyFailed, "AICR bundle exceeds the %d-entry limit",
						maxQualifiedBundleEntries)
				}
				name := child.Name()
				if name == "" || name == "." || name == ".." || filepath.Base(name) != name ||
					strings.Contains(name, `\`) || !utf8.ValidString(name) {
					return e.New(e.CodeVerifyFailed, "AICR bundle contains invalid entry name %q", name)
				}
				entryCount++
				entryPath := name
				if relative != "" {
					entryPath = filepath.Join(relative, name)
				}
				slashPath := filepath.ToSlash(entryPath)
				if !fs.ValidPath(slashPath) {
					return e.New(e.CodeVerifyFailed, "AICR bundle contains invalid relative path %q", slashPath)
				}
				opened, info, err := openQualifiedBundleChild(directory, name)
				if err != nil {
					return e.Wrap(e.CodeVerifyFailed, err,
						"opening descriptor-rooted AICR bundle entry %q", slashPath)
				}
				heldEntry := heldBundleEntry{evidence: bundleEntry{
					Path: slashPath, Mode: normalizedMode(info.Mode()),
				}}
				if info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
					_ = opened.Close()
					return e.New(e.CodeVerifyFailed,
						"AICR bundle contains special permission bits on %q", slashPath)
				}
				if info.IsDir() {
					heldEntry.evidence.Type = "directory"
					snapshot.entries = append(snapshot.entries, heldEntry)
					walkErr := walk(opened, entryPath, depth+1)
					if walkErr != nil {
						_ = opened.Close()
						return walkErr
					}
					recheckErr := qualifiedBundleChildUnchanged(directory, name, opened, info)
					closeErr := opened.Close()
					if recheckErr != nil || closeErr != nil {
						if recheckErr == nil {
							recheckErr = closeErr
						}
						return e.Wrap(e.CodeVerifyFailed, recheckErr,
							"rechecking AICR bundle directory %q", slashPath)
					}
					continue
				}
				if !info.Mode().IsRegular() {
					_ = opened.Close()
					return e.New(e.CodeVerifyFailed, "AICR bundle contains non-regular entry %q", slashPath)
				}
				if info.Size() < 0 || info.Size() > maxQualifiedBundleFileBytes ||
					info.Size() > maxQualifiedBundleTreeBytes-totalBytes {
					_ = opened.Close()
					return e.New(e.CodeVerifyFailed, "AICR bundle file %q exceeds its qualified size bound", slashPath)
				}
				var (
					held       *heldfile.File
					fileDigest string
					fileSize   int64
					captureErr error
				)
				if options.holdFiles {
					remainingBytes := maxQualifiedBundleTreeBytes - totalBytes
					captureLimit := min(maxQualifiedBundleFileBytes, remainingBytes)
					if captureLimit == 0 {
						// heldfile requires a positive limit. A zero-byte file is still
						// admissible at the exact tree limit; any content is rejected below.
						captureLimit = 1
					}
					held, captureErr = heldfile.FromReaderContext(ctx, opened, heldfile.Options{
						Label:    "aicr-bundle-" + slashPath,
						MaxBytes: captureLimit,
					})
					if captureErr == nil {
						fileDigest, fileSize = held.Digest(), held.Size()
					}
				} else {
					fileDigest, fileSize, captureErr = digestQualifiedBundleFile(
						ctx, opened, min(maxQualifiedBundleFileBytes, maxQualifiedBundleTreeBytes-totalBytes))
				}
				if captureErr != nil {
					_ = opened.Close()
					if ctxErr := contextError(ctx, "snapshotting AICR bundle"); ctxErr != nil {
						return ctxErr
					}
					return e.Wrap(e.CodeVerifyFailed, captureErr,
						"reading AICR bundle file %q", slashPath)
				}
				recheckErr := qualifiedBundleChildUnchanged(directory, name, opened, info)
				closeErr := opened.Close()
				if recheckErr != nil || closeErr != nil {
					_ = held.Close()
					if recheckErr == nil {
						recheckErr = closeErr
					}
					return e.Wrap(e.CodeVerifyFailed, recheckErr,
						"rechecking AICR bundle file %q", slashPath)
				}
				totalBytes += fileSize
				if totalBytes > maxQualifiedBundleTreeBytes {
					if held != nil {
						_ = held.Close()
					}
					return e.New(e.CodeVerifyFailed, "AICR bundle exceeds the %d-byte tree limit",
						maxQualifiedBundleTreeBytes)
				}
				heldEntry.evidence.Type = "file"
				heldEntry.file = held
				heldEntry.evidence.SHA256 = fileDigest
				snapshot.entries = append(snapshot.entries, heldEntry)
				switch slashPath {
				case "recipe.yaml":
					hasRecipe = true
					recipeMatches = options.heldRecipe != nil && fileDigest == options.heldRecipe.Digest() &&
						fileSize == options.heldRecipe.Size()
				case "checksums.txt":
					hasChecksums = true
				}
			}
			if readErr == io.EOF {
				return nil
			}
			if readErr != nil {
				return e.Wrap(e.CodeVerifyFailed, readErr,
					"enumerating descriptor-rooted AICR bundle directory")
			}
		}
	}
	err = walk(root, "", 0)
	if err != nil {
		var coded *e.Error
		if errors.As(err, &coded) {
			return fail(err)
		}
		return fail(e.Wrap(e.CodeVerifyFailed, err, "walking descriptor-rooted AICR bundle snapshot"))
	}
	rootRecheckErr := qualifiedBundleChildUnchanged(root, ".", root, rootInfo)
	if rootRecheckErr != nil {
		return fail(e.Wrap(e.CodeVerifyFailed, rootRecheckErr,
			"rechecking descriptor-rooted AICR bundle root"))
	}
	if !hasRecipe {
		return fail(e.New(e.CodeVerifyFailed, "AICR bundle has no regular recipe.yaml"))
	}
	if options.holdFiles && !recipeMatches {
		return fail(e.New(e.CodeVerifyFailed,
			"AICR bundle recipe.yaml is not the exact held recipe verified by the generator"))
	}
	if !hasChecksums {
		return fail(e.New(e.CodeVerifyFailed, "AICR bundle has no regular checksums.txt"))
	}
	sort.Slice(snapshot.entries, func(i, j int) bool {
		return snapshot.entries[i].evidence.Path < snapshot.entries[j].evidence.Path
	})
	tree := bundleTree{SchemaVersion: bundleSchemaVersion, Entries: make([]bundleEntry, len(snapshot.entries))}
	for i := range snapshot.entries {
		tree.Entries[i] = snapshot.entries[i].evidence
	}
	snapshot.digest, err = canonical.Digest(canonical.KindAICRBundle, tree)
	if err != nil {
		return fail(err)
	}
	return snapshot, nil
}

func digestQualifiedBundleFile(ctx context.Context, file *os.File, maxBytes int64) (string, int64, error) {
	if file == nil || maxBytes < 0 {
		return "", 0, errors.New("invalid qualified AICR bundle file authority")
	}
	hash := sha256.New()
	buffer := make([]byte, 128<<10)
	var size int64
	for {
		if err := contextError(ctx, "hashing qualified AICR bundle file"); err != nil {
			return "", 0, err
		}
		read, readErr := file.Read(buffer)
		if read > 0 {
			size += int64(read)
			if size > maxBytes {
				return "", 0, e.New(e.CodeVerifyFailed,
					"AICR bundle file exceeds its %d-byte qualified bound", maxBytes)
			}
			_, _ = hash.Write(buffer[:read])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", 0, readErr
		}
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), size, nil
}

func (snapshot *heldBundleSnapshot) Close() error {
	if snapshot == nil {
		return nil
	}
	var first error
	for i := range snapshot.entries {
		if snapshot.entries[i].file != nil {
			if err := snapshot.entries[i].file.Close(); err != nil && first == nil {
				first = err
			}
			snapshot.entries[i].file = nil
		}
	}
	return first
}

func (snapshot *heldBundleSnapshot) materialize(ctx context.Context, destination string) error {
	if snapshot == nil || snapshot.digest == "" {
		return e.New(e.CodeInternal, "held AICR bundle snapshot is missing")
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "opening AICR commit tree")
	}
	defer func() { _ = root.Close() }()
	for _, entry := range snapshot.entries {
		if err := contextError(ctx, "materializing held AICR bundle"); err != nil {
			return err
		}
		if entry.evidence.Type == "directory" {
			if err := rootedMkdirAll(root, entry.evidence.Path, 0o700); err != nil {
				return e.Wrap(e.CodeBuildFailed, err, "creating held AICR directory %q", entry.evidence.Path)
			}
			continue
		}
		if err := rootedMkdirAll(root, filepath.ToSlash(filepath.Dir(entry.evidence.Path)), 0o700); err != nil {
			return e.Wrap(e.CodeBuildFailed, err, "creating held AICR file parent")
		}
		out, err := root.OpenFile(entry.evidence.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return e.Wrap(e.CodeBuildFailed, err, "creating held AICR file %q", entry.evidence.Path)
		}
		written, copyErr := entry.file.WriteToContext(ctx, out)
		syncErr := out.Sync()
		chmodErr := out.Chmod(materializedMode(entry.evidence.Mode))
		closeErr := out.Close()
		if copyErr != nil || written != entry.file.Size() {
			if ctxErr := contextError(ctx, "materializing held AICR bundle"); ctxErr != nil {
				return ctxErr
			}
			if copyErr == nil {
				copyErr = io.ErrShortWrite
			}
			return e.Wrap(e.CodeBuildFailed, copyErr, "materializing held AICR file %q", entry.evidence.Path)
		}
		for _, operationErr := range []error{syncErr, chmodErr, closeErr} {
			if operationErr != nil {
				return e.Wrap(e.CodeBuildFailed, operationErr, "persisting held AICR file %q", entry.evidence.Path)
			}
		}
	}
	for i := len(snapshot.entries) - 1; i >= 0; i-- {
		entry := snapshot.entries[i]
		if entry.evidence.Type == "directory" {
			directory, err := root.Open(entry.evidence.Path)
			if err != nil {
				return e.Wrap(e.CodeBuildFailed, err, "opening held AICR directory %q", entry.evidence.Path)
			}
			info, statErr := directory.Stat()
			if statErr != nil || !info.IsDir() {
				_ = directory.Close()
				if statErr != nil {
					return e.Wrap(e.CodeBuildFailed, statErr,
						"inspecting held AICR directory %q", entry.evidence.Path)
				}
				return e.New(e.CodeBuildFailed,
					"held AICR directory %q changed during materialization", entry.evidence.Path)
			}
			chmodErr := directory.Chmod(materializedMode(entry.evidence.Mode))
			closeErr := directory.Close()
			if chmodErr != nil {
				return e.Wrap(e.CodeBuildFailed, chmodErr, "setting held AICR directory mode %q", entry.evidence.Path)
			}
			if closeErr != nil {
				return e.Wrap(e.CodeBuildFailed, closeErr, "closing held AICR directory %q", entry.evidence.Path)
			}
		}
	}
	return nil
}

// rootedMkdirAll is the Go 1.24-compatible subset of os.Root.MkdirAll that the
// materializer needs. Every component is resolved relative to the already-open
// commit root and must remain a real directory.
func rootedMkdirAll(root *os.Root, path string, mode os.FileMode) error {
	if root == nil {
		return errors.New("nil AICR commit root")
	}
	path = filepath.ToSlash(path)
	if path == "." || path == "" {
		return nil
	}
	if strings.HasPrefix(path, "/") || strings.HasPrefix(path, "../") ||
		strings.Contains(path, `\`) {
		return fs.ErrInvalid
	}
	current := ""
	for _, component := range strings.Split(path, "/") {
		if component == "" || component == "." || component == ".." {
			return fs.ErrInvalid
		}
		if current == "" {
			current = component
		} else {
			current += "/" + component
		}
		info, err := root.Lstat(current)
		if os.IsNotExist(err) {
			if err := root.Mkdir(current, mode); err != nil && !os.IsExist(err) {
				return err
			}
			info, err = root.Lstat(current)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fs.ErrInvalid
		}
	}
	return nil
}

func materializedMode(mode string) os.FileMode {
	if mode == "0755" {
		return 0o755
	}
	return 0o644
}

func normalizedMode(mode fs.FileMode) string {
	if mode.Perm()&0o111 != 0 {
		return "0755"
	}
	return "0644"
}
