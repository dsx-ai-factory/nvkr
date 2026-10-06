package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"golang.org/x/sys/unix"
)

const (
	sourceClosureSchema     = "nkd.dev/source-closure/v1"
	sourceReceiptSchema     = "nkd.dev/source-closure-receipt/v2"
	maxSourceClosureEntries = 100_000
)

// sourceReceipt is the authority boundary between network-permitted fetch and an
// offline build. It describes the exact post-vendor filesystem closure. A build
// never trusts the receipt alone: it re-hashes the source before copying it into a
// private snapshot and checks the copy again, closing the dirty-worktree and
// fetch/build TOCTOU gaps. Later consumers in the same run may share that exact
// already-verified snapshot only when their complete preparation key matches.
type sourceReceipt struct {
	Schema           string `json:"schema"`
	Commit           string `json:"commit"`
	TreeHash         string `json:"treeHash"`
	VendorSpecDigest string `json:"vendorSpecDigest"`
	ClosureDigest    string `json:"closureDigest"`
}

type materializedSource struct {
	Dir         string
	InputDigest string
	cleanup     func()
}

type preparedSourceEntry struct {
	ready chan struct{}
	value *materializedSource
	err   error
}

// preparedSourceCache is a run-scoped single-flight cache. It deduplicates the
// expensive checkout hash -> normalized copy -> copy hash -> patched-input hash
// chain for build nodes that consume the exact same read-only source input. The
// cache never crosses runs or persists authority; Local.Run or NodeSession owns
// and closes it. A NodeSession keeps separate caches for each source workspace.
type preparedSourceCache struct {
	mu      sync.Mutex
	entries map[string]*preparedSourceEntry
	closed  bool
}

func newPreparedSourceCache() *preparedSourceCache {
	return &preparedSourceCache{entries: make(map[string]*preparedSourceEntry)}
}

func (c *preparedSourceCache) close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	entries := make([]*preparedSourceEntry, 0, len(c.entries))
	for _, entry := range c.entries {
		entries = append(entries, entry)
	}
	c.mu.Unlock()
	for _, entry := range entries {
		<-entry.ready
		if entry.value != nil {
			entry.value.cleanup()
		}
	}
}

func preparedSourceKey(n ir.Node) (string, error) {
	// SourceDest and WriteDir are included even though they are not content
	// identities: they determine the held checkout/receipt and the mountpoint that
	// must exist before concurrent consumers may use the shared snapshot.
	key, err := canonical.Marshal(struct {
		SourceDest       string        `json:"sourceDest"`
		SourceCommit     string        `json:"sourceCommit"`
		SourceTreeHash   string        `json:"sourceTreeHash"`
		VendorSpecDigest string        `json:"vendorSpecDigest"`
		Epoch            int64         `json:"epoch"`
		WriteDir         string        `json:"writeDir"`
		Patches          []ir.PatchRef `json:"patches,omitempty"`
	}{
		SourceDest: n.SourceDest, SourceCommit: n.SourceCommit,
		SourceTreeHash: n.SourceTreeHash, VendorSpecDigest: n.VendorSpecDigest,
		Epoch: envEpoch(n.Env), WriteDir: n.WriteDir, Patches: n.Patches,
	})
	if err != nil {
		return "", err
	}
	return string(key), nil
}

func (c *preparedSourceCache) materialize(n ir.Node, opts RunOptions) (*materializedSource, error) {
	key, err := preparedSourceKey(n)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, e.New(e.CodeInternal, "prepared source cache closed before build %q", n.Component)
	}
	entry, exists := c.entries[key]
	if !exists {
		entry = &preparedSourceEntry{ready: make(chan struct{})}
		c.entries[key] = entry
	}
	c.mu.Unlock()

	if exists {
		select {
		case <-entry.ready:
		case <-opts.ctx().Done():
			return nil, e.Wrap(e.CodeTimeout, opts.ctx().Err(), "waiting for shared source preparation for %s", n.Component)
		}
		if entry.err != nil {
			return nil, entry.err
		}
		opts.progress.printf("[source] build %s — reused the run's exact verified private snapshot\n", n.Component)
		return &materializedSource{Dir: entry.value.Dir, InputDigest: entry.value.InputDigest, cleanup: func() {}}, nil
	}

	value, materializeErr := materializeSourceUncached(n, opts)
	c.mu.Lock()
	entry.value = value
	entry.err = materializeErr
	close(entry.ready)
	c.mu.Unlock()
	if materializeErr != nil {
		return nil, materializeErr
	}
	return &materializedSource{Dir: value.Dir, InputDigest: value.InputDigest, cleanup: func() {}}, nil
}

// ensureSourceClosure completes a fetch: a pristine pinned checkout is optionally
// vendored, then a receipt for the exact resulting files is written atomically.
// Existing receipts are reusable only when both their declaration and current bytes
// still match. Any drift fails closed; this function never silently blesses it.
func ensureSourceClosure(n ir.Node, dir string, opts RunOptions, runVendor func() error) error {
	logicalDir := dir
	if opts.fetchDirectory != nil {
		var err error
		dir, err = sourceFetchDescriptorPath(opts.fetchDirectory.Fd())
		if err != nil {
			return err
		}
	}
	wantVendor, err := fetchVendorSpecDigest(n)
	if err != nil {
		return err
	}
	receiptPath, err := sourceReceiptPath(opts.SourceRoot, n.Dest)
	if err != nil {
		return err
	}
	receipt, exists, err := readSourceReceiptContext(opts.ctx(), receiptPath, opts.fetchReceiptDirectory)
	if err != nil {
		return err
	}
	if exists {
		if receipt.Schema != sourceReceiptSchema || receipt.Commit != n.Commit || receipt.TreeHash != n.TreeHash || receipt.VendorSpecDigest != wantVendor {
			return e.New(e.CodeFetchFailed, "source receipt for %q does not match the pinned fetch declaration", n.Dest)
		}
		got, err := sourceTreeDigestContext(opts.ctx(), dir)
		if err != nil {
			return e.Wrap(e.CodeFetchFailed, err, "hashing fetched source %s", n.Dest)
		}
		if got != receipt.ClosureDigest {
			return e.New(e.CodeFetchFailed, "source closure drift for %q: got %s want %s", n.Dest, got, receipt.ClosureDigest)
		}
		return nil
	}

	// A missing receipt is accepted only for a pristine checkout. Include ignored
	// files in the inspection: stale build/vendor caches can affect compilation even
	// though ordinary `git status` hides them.
	status, err := sourceFetchGitOut(opts, dir, "status", "--porcelain=v1", "--untracked-files=all", "--ignored=matching")
	if err != nil {
		if opts.ctx().Err() != nil {
			return err
		}
		return e.Wrap(e.CodeFetchFailed, err, "checking source cleanliness in %s", logicalDir)
	}
	if status != "" {
		return e.New(e.CodeFetchFailed, "source %q has unreceipted tracked, untracked, or ignored files; use a fresh source root", n.Dest)
	}
	if len(n.VendorCmd) > 0 {
		if runVendor == nil {
			return e.New(e.CodeFetchFailed, "source %q requires vendoring but no vendor runner was provided", n.Dest)
		}
		if err := runVendor(); err != nil {
			return err
		}
	}
	closure, err := sourceTreeDigestContext(opts.ctx(), dir)
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "hashing fetched source %s", n.Dest)
	}
	return writeSourceReceiptContext(opts.ctx(), receiptPath, sourceReceipt{
		Schema: sourceReceiptSchema, Commit: n.Commit, TreeHash: n.TreeHash,
		VendorSpecDigest: wantVendor, ClosureDigest: closure,
	}, opts.fetchReceiptDirectory)
}

// materializeSource authenticates the fetch receipt, copies the exact closure to
// a private directory, applies the ordered verified patch series, and computes the
// resolved-input identity. Exact read-only consumers in one execution session
// share a single-flight result; standalone callers retain per-call ownership.
func materializeSource(n ir.Node, opts RunOptions) (*materializedSource, error) {
	if opts.sources != nil && n.SourceReadOnly && n.WriteDir != "" {
		return opts.sources.materialize(n, opts)
	}
	return materializeSourceUncached(n, opts)
}

func materializeSourceUncached(n ir.Node, opts RunOptions) (*materializedSource, error) {
	totalStarted := time.Now()
	src, err := pathWithin(opts.SourceRoot, n.SourceDest)
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "resolving source for %s", n.Component)
	}
	receiptPath, err := sourceReceiptPath(opts.SourceRoot, n.SourceDest)
	if err != nil {
		return nil, err
	}
	receipt, exists, err := readSourceReceiptContext(opts.ctx(), receiptPath)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, e.New(e.CodeBuildFailed, "build %q has no authenticated fetch receipt for source %q", n.Component, n.SourceDest)
	}
	if receipt.Schema != sourceReceiptSchema || receipt.Commit != n.SourceCommit || receipt.TreeHash != n.SourceTreeHash || receipt.VendorSpecDigest != n.VendorSpecDigest {
		return nil, e.New(e.CodeBuildFailed, "build %q source receipt does not match its pinned source declaration", n.Component)
	}
	phaseStarted := time.Now()
	current, err := sourceTreeDigestContext(opts.ctx(), src)
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "hashing source for %s", n.Component)
	}
	if current != receipt.ClosureDigest {
		return nil, e.New(e.CodeBuildFailed, "build %q source changed after fetch: got %s want %s", n.Component, current, receipt.ClosureDigest)
	}
	opts.progress.printf("[source] build %s — checkout closure re-hash verified in %s\n",
		n.Component, time.Since(phaseStarted).Round(time.Millisecond))

	root := filepath.Join(filepath.Dir(opts.SourceRoot), "snapshots")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "creating source snapshot root")
	}
	dir, err := os.MkdirTemp(root, sanitizeName(n.Component)+"-")
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "creating private source snapshot for %s", n.Component)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	epoch := envEpoch(n.Env)
	phaseStarted = time.Now()
	if err := copySourceTreeContext(opts.ctx(), src, dir, epoch); err != nil {
		cleanup()
		return nil, e.Wrap(e.CodeBuildFailed, err, "copying private source snapshot for %s", n.Component)
	}
	opts.progress.printf("[source] build %s — private normalized snapshot copied in %s\n",
		n.Component, time.Since(phaseStarted).Round(time.Millisecond))
	phaseStarted = time.Now()
	copied, err := sourceTreeDigestContext(opts.ctx(), dir)
	if err != nil {
		cleanup()
		return nil, e.Wrap(e.CodeBuildFailed, err, "hashing private source snapshot for %s", n.Component)
	}
	if copied != receipt.ClosureDigest {
		cleanup()
		return nil, e.New(e.CodeBuildFailed, "build %q source changed while snapshotting: got %s want %s", n.Component, copied, receipt.ClosureDigest)
	}
	opts.progress.printf("[source] build %s — private snapshot re-hash verified in %s\n",
		n.Component, time.Since(phaseStarted).Round(time.Millisecond))
	prepared := copied
	if len(n.Patches) == 0 {
		opts.progress.printf("[source] build %s — no patches; verified snapshot digest is the final input digest\n", n.Component)
	} else {
		if err := applyPatches(n, dir, opts); err != nil {
			cleanup()
			return nil, err
		}
		phaseStarted = time.Now()
		prepared, err = sourceTreeDigestContext(opts.ctx(), dir)
		if err != nil {
			cleanup()
			return nil, e.Wrap(e.CodeBuildFailed, err, "hashing patched source snapshot for %s", n.Component)
		}
		if prepared == copied {
			cleanup()
			return nil, e.New(e.CodeBuildFailed,
				"build %q authenticated patch series did not change the prepared source", n.Component)
		}
		opts.progress.printf("[source] build %s — final patched-input hash computed in %s\n",
			n.Component, time.Since(phaseStarted).Round(time.Millisecond))
	}
	type patchIdentity struct {
		Name   string `json:"name"`
		SHA256 string `json:"sha256"`
	}
	pins := make([]patchIdentity, len(n.Patches))
	for i, p := range n.Patches {
		pins[i] = patchIdentity{Name: p.Name, SHA256: p.SHA256}
	}
	inputDigest, err := canonical.Digest(canonical.KindResolvedInput, struct {
		SourceClosure   string          `json:"sourceClosure"`
		PreparedClosure string          `json:"preparedClosure"`
		Patches         []patchIdentity `json:"patches,omitempty"`
	}{SourceClosure: receipt.ClosureDigest, PreparedClosure: prepared, Patches: pins})
	if err != nil {
		cleanup()
		return nil, err
	}
	if n.SourceReadOnly && n.WriteDir != "" {
		mountpoint, err := pathWithin(dir, n.WriteDir)
		if err != nil {
			cleanup()
			return nil, e.Wrap(e.CodeBuildFailed, err, "resolving output mountpoint for %s", n.Component)
		}
		if err := os.MkdirAll(mountpoint, 0o755); err != nil {
			cleanup()
			return nil, e.Wrap(e.CodeBuildFailed, err, "preparing output mountpoint for %s", n.Component)
		}
	}
	opts.progress.printf("[source] build %s — source preparation complete in %s\n",
		n.Component, time.Since(totalStarted).Round(time.Millisecond))
	return &materializedSource{Dir: dir, InputDigest: inputDigest, cleanup: cleanup}, nil
}

func applyPatches(n ir.Node, dir string, opts RunOptions) error {
	ceiling, err := filepath.Abs(filepath.Dir(dir))
	if err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "resolving private patch root for %s", n.Component)
	}
	for i, p := range n.Patches {
		got := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(p.Content)))
		if got != p.SHA256 {
			return e.New(e.CodeBuildFailed, "build %q patch[%d] %q bytes do not match %s", n.Component, i, p.Name, p.SHA256)
		}
		for _, args := range [][]string{
			{"apply", "--no-index", "--check", "--whitespace=nowarn", "-"},
			{"apply", "--no-index", "--whitespace=nowarn", "-"},
			{"apply", "--no-index", "--reverse", "--check", "--whitespace=nowarn", "-"},
		} {
			cmd := execCmd(opts, "git", args...)
			cmd.Dir = dir
			// Run snapshots may live beneath the nke-recipes checkout. Prevent Git
			// from discovering that enclosing repository, which would make apply
			// treat the snapshot as a subdirectory and silently skip unrelated paths.
			cmd.Env = deterministicExecEnv(nil, map[string]string{
				"GIT_CEILING_DIRECTORIES": ceiling,
				"LC_ALL":                  "C",
			})
			cmd.Stdin = strings.NewReader(p.Content)
			cmd.Stdout, cmd.Stderr = opts.Stdout, opts.Stderr
			if err := runOwnedCommand(opts.ctx(), cmd); err != nil {
				return e.Wrap(e.CodeBuildFailed, err, "applying patch[%d] %q to %s", i, p.Name, n.Component)
			}
		}
	}
	return nil
}

// sourceClosureManifest is the transport-independent identity view of a fetched
// source tree. The schema version makes changes to entry semantics explicit;
// canonical.Digest supplies both stable serialization and domain separation.
type sourceClosureManifest struct {
	SchemaVersion string               `json:"schemaVersion"`
	Entries       []sourceClosureEntry `json:"entries"`
}

type sourceClosureEntry struct {
	Path          string `json:"path"`
	Type          string `json:"type"`
	Mode          string `json:"mode"`
	ContentDigest string `json:"contentDigest,omitempty"`
	Target        string `json:"target,omitempty"`
}

// sourceTreeDigest hashes the canonical manifest of all build-visible bytes,
// relative paths, entry types, permission bits, and symlink targets while
// excluding Git's administrative directory. It does not hash mtimes: the
// materializer normalizes those to SOURCE_DATE_EPOCH and mtime is not a
// source-content identity.
func sourceTreeDigest(root string) (string, error) {
	return sourceTreeDigestContext(context.Background(), root)
}

func sourceTreeDigestContext(ctx context.Context, root string) (string, error) {
	manifest, err := sourceTreeManifestContext(ctx, root)
	if err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindSourceClosure, manifest)
}

func sourceTreeManifest(root string) (sourceClosureManifest, error) {
	return sourceTreeManifestContext(context.Background(), root)
}

func sourceTreeManifestContext(ctx context.Context, root string) (sourceClosureManifest, error) {
	if ctx == nil {
		return sourceClosureManifest{}, fmt.Errorf("source-tree hashing requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return sourceClosureManifest{}, err
	}
	rootDir, _, err := securepath.OpenDirectory(root)
	if err != nil {
		return sourceClosureManifest{}, fmt.Errorf("source root %s is not a real directory: %w", root, err)
	}
	defer rootDir.Close()
	entries := make([]sourceClosureEntry, 0)
	var walk func(*os.File, string) error
	walk = func(directory *os.File, prefix string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		names, err := sourceChildNames(directory)
		if err != nil {
			return err
		}
		for _, name := range names {
			if err := ctx.Err(); err != nil {
				return err
			}
			if prefix == "" && name == ".git" {
				continue
			}
			if len(entries) >= maxSourceClosureEntries {
				return fmt.Errorf("source tree exceeds its %d-entry bound", maxSourceClosureEntries)
			}
			relative := name
			if prefix != "" {
				relative = filepath.Join(prefix, name)
			}
			entry, err := securepath.InspectChild(directory, name, relative)
			if err != nil {
				return err
			}
			mode := fmt.Sprintf("%04o", entry.Mode.Perm())
			slashPath := filepath.ToSlash(relative)
			switch entry.Kind {
			case "directory":
				entries = append(entries, sourceClosureEntry{Path: slashPath, Type: entry.Kind, Mode: mode})
				err = walk(entry.File, relative)
				closeErr := entry.File.Close()
				if err != nil {
					return err
				}
				if closeErr != nil {
					return closeErr
				}
			case "file":
				contentDigest, _, _, hashErr := hashOpenRegularContext(ctx, entry.File, entry.Info)
				closeErr := entry.File.Close()
				if hashErr != nil {
					return fmt.Errorf("hashing source file %s: %w", relative, hashErr)
				}
				if closeErr != nil {
					return closeErr
				}
				entries = append(entries, sourceClosureEntry{
					Path: slashPath, Type: entry.Kind, Mode: mode, ContentDigest: contentDigest,
				})
			case "symlink":
				entries = append(entries, sourceClosureEntry{
					Path: slashPath, Type: entry.Kind, Mode: mode, Target: entry.Target,
				})
			default:
				return fmt.Errorf("unsupported source entry %s", relative)
			}
		}
		return nil
	}
	err = walk(rootDir, "")
	if err != nil {
		return sourceClosureManifest{}, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return sourceClosureManifest{SchemaVersion: sourceClosureSchema, Entries: entries}, nil
}

func sourceChildNames(directory *os.File) ([]string, error) {
	names := make([]string, 0)
	for {
		children, err := directory.ReadDir(1024)
		for _, child := range children {
			name := child.Name()
			if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
				return nil, fmt.Errorf("unsafe source child name %q", name)
			}
			names = append(names, name)
			if len(names) > maxSourceClosureEntries {
				return nil, fmt.Errorf("source directory exceeds its %d-entry bound", maxSourceClosureEntries)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(names)
	return names, nil
}

func copySourceTree(src, dst string, epoch int64) error {
	return copySourceTreeContext(context.Background(), src, dst, epoch)
}

func copySourceTreeContext(ctx context.Context, src, dst string, epoch int64) error {
	if ctx == nil {
		return fmt.Errorf("source-tree copy requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	sourceRoot, _, err := securepath.OpenDirectory(src)
	if err != nil {
		return fmt.Errorf("source root %s is not a real directory: %w", src, err)
	}
	defer sourceRoot.Close()
	destinationRoot, _, err := securepath.OpenDirectory(dst)
	if err != nil {
		return fmt.Errorf("snapshot root %s is not a real directory: %w", dst, err)
	}
	defer destinationRoot.Close()
	stamp := time.Unix(epoch, 0).UTC()
	entries := 0
	if err := copySourceDirectoryContext(ctx, sourceRoot, destinationRoot, "", stamp, &entries); err != nil {
		return err
	}
	if err := destinationRoot.Chmod(0o755); err != nil {
		return err
	}
	return setOpenFileTimes(destinationRoot, stamp)
}

func copySourceDirectoryContext(ctx context.Context, source, destination *os.File, prefix string, stamp time.Time, entries *int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	names, err := sourceChildNames(source)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		if prefix == "" && name == ".git" {
			continue
		}
		*entries++
		if *entries > maxSourceClosureEntries {
			return fmt.Errorf("source tree exceeds its %d-entry bound", maxSourceClosureEntries)
		}
		relative := name
		if prefix != "" {
			relative = filepath.Join(prefix, name)
		}
		entry, err := securepath.InspectChild(source, name, relative)
		if err != nil {
			return err
		}
		switch entry.Kind {
		case "directory":
			if err := unix.Mkdirat(int(destination.Fd()), name, 0o700); err != nil {
				entry.File.Close()
				return fmt.Errorf("creating snapshot directory %s: %w", relative, err)
			}
			to, _, err := securepath.OpenChild(destination, name, true)
			if err != nil {
				entry.File.Close()
				return fmt.Errorf("opening snapshot directory %s: %w", relative, err)
			}
			copyErr := copySourceDirectoryContext(ctx, entry.File, to, relative, stamp, entries)
			if copyErr == nil {
				copyErr = to.Chmod(entry.Mode.Perm())
			}
			if copyErr == nil {
				copyErr = setOpenFileTimes(to, stamp)
			}
			sourceCloseErr := entry.File.Close()
			destCloseErr := to.Close()
			if copyErr != nil {
				return copyErr
			}
			if sourceCloseErr != nil {
				return sourceCloseErr
			}
			if destCloseErr != nil {
				return destCloseErr
			}
		case "file":
			if err := copyOpenSourceFileContext(ctx, entry, destination, name, relative, stamp); err != nil {
				entry.File.Close()
				return err
			}
			if err := entry.File.Close(); err != nil {
				return err
			}
		case "symlink":
			if err := unix.Symlinkat(entry.Target, int(destination.Fd()), name); err != nil {
				return fmt.Errorf("creating snapshot symlink %s: %w", relative, err)
			}
			times := []unix.Timespec{unix.NsecToTimespec(stamp.UnixNano()), unix.NsecToTimespec(stamp.UnixNano())}
			if err := unix.UtimesNanoAt(int(destination.Fd()), name, times, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return fmt.Errorf("normalizing snapshot symlink %s: %w", relative, err)
			}
		}
	}
	return nil
}

func copyOpenSourceFileContext(ctx context.Context, entry securepath.Entry, destination *os.File, name, relative string, stamp time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fd, err := unix.Openat(int(destination.Fd()), name,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("creating snapshot file %s: %w", relative, err)
	}
	out := os.NewFile(uintptr(fd), name)
	if out == nil {
		_ = unix.Close(fd)
		return fmt.Errorf("opening snapshot file %s", relative)
	}
	size, copyErr := copyPipelineContext(ctx, out, entry.File)
	after, statErr := entry.File.Stat()
	if copyErr == nil && statErr == nil &&
		(!os.SameFile(entry.Info, after) || size != entry.Info.Size() || after.Size() != entry.Info.Size() ||
			!after.ModTime().Equal(entry.Info.ModTime()) || after.Mode() != entry.Info.Mode()) {
		copyErr = fmt.Errorf("source file %s changed while being copied", relative)
	}
	if copyErr == nil {
		copyErr = out.Chmod(entry.Mode.Perm())
	}
	if copyErr == nil {
		copyErr = setOpenFileTimes(out, stamp)
	}
	// This is an ephemeral, per-run input snapshot, not a published durable object.
	// The next phase closes every file, descriptor-reopens the complete tree, and
	// re-hashes it before Docker can consume it. Per-file fsync therefore adds no
	// authority or recovery guarantee; on filesystems such as APFS it turns a small
	// source copy into thousands of serialized durability barriers.
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if statErr != nil {
		return statErr
	}
	return closeErr
}

func setOpenFileTimes(file *os.File, stamp time.Time) error {
	timeval := unix.NsecToTimeval(stamp.UnixNano())
	return unix.Futimes(int(file.Fd()), []unix.Timeval{timeval, timeval})
}

func copyCollectedArtifactContext(ctx context.Context, sourceRoot, sourceRelative, dst string) error {
	if ctx == nil {
		return fmt.Errorf("artifact collection requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	in, info, err := openRegularBeneath(sourceRoot, sourceRelative)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".artifact-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		tmp.Close()
		return err
	}
	written, copyErr := copyPipelineContext(ctx, tmp, in)
	after, statErr := in.Stat()
	if copyErr == nil && (statErr != nil || !securepath.SameMetadata(info, after) || written != info.Size()) {
		copyErr = fmt.Errorf("collected artifact changed while being copied")
	}
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if copyErr != nil {
		return copyErr
	}
	if statErr != nil {
		return statErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return err
	}
	return nil
}

func fetchVendorSpecDigest(n ir.Node) (string, error) {
	digest, err := ir.VendorSpecIdentity(n.VendorCmd, n.VendorImage)
	if err != nil {
		return "", err
	}
	if n.VendorSpecDigest != "" && n.VendorSpecDigest != digest {
		return "", e.New(e.CodeFetchFailed, "fetch %q vendorSpecDigest mismatch: got %s want %s", n.ID, n.VendorSpecDigest, digest)
	}
	return digest, nil
}

// SourceReceiptPath describes the exact receipt file for a source destination.
// Generators use it to transport one fetch result without capturing sibling receipts.
// The returned path grants no authority; execution still authenticates its bytes.
func SourceReceiptPath(sourceRoot, dest string) (string, error) {
	path, err := sourceReceiptPath(sourceRoot, dest)
	if err != nil {
		return "", e.Wrap(e.CodeInvalidCatalog, err, "locating source receipt")
	}
	return path, nil
}

func sourceReceiptPath(sourceRoot, dest string) (string, error) {
	if _, err := pathWithin(sourceRoot, dest); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(dest))
	name := sanitizeName(filepath.Base(dest)) + "-" + hex.EncodeToString(sum[:6]) + ".json"
	return filepath.Join(filepath.Dir(sourceRoot), "source-receipts", name), nil
}

func pathWithin(root, rel string) (string, error) {
	if strings.TrimSpace(rel) == "" || filepath.IsAbs(rel) || filepath.Clean(rel) != rel {
		return "", fmt.Errorf("unsafe relative path %q", rel)
	}
	base, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	full, err := filepath.Abs(filepath.Join(base, rel))
	if err != nil {
		return "", err
	}
	back, err := filepath.Rel(base, full)
	if err != nil || back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes source root", rel)
	}
	return full, nil
}

func readSourceReceiptContext(ctx context.Context, path string, directories ...*os.File) (sourceReceipt, bool, error) {
	var out sourceReceipt
	if ctx == nil {
		return out, false, e.New(e.CodeUsage, "reading a source receipt requires a non-nil context")
	}
	root := filepath.Dir(path)
	if len(directories) != 0 && directories[0] != nil {
		var err error
		root, err = sourceFetchDescriptorPath(directories[0].Fd())
		if err != nil {
			return out, false, err
		}
	}
	b, err := readRegularBeneathContext(ctx, root, filepath.Base(path), 1<<20)
	if err != nil {
		// The descriptor reader wraps a missing parent/final path.
		// os.IsNotExist only recognizes a limited set of legacy wrappers; errors.Is
		// preserves the fresh-workspace cache-miss contract through %w wrapping.
		if errors.Is(err, fs.ErrNotExist) {
			return out, false, nil
		}
		return out, false, e.Wrap(e.CodeFetchFailed, err, "reading source receipt %s", path)
	}
	if err := canonical.UnmarshalExact(b, &out); err != nil {
		return out, false, e.Wrap(e.CodeFetchFailed, err, "decoding source receipt %s", path)
	}
	return out, true, nil
}

func writeSourceReceipt(path string, receipt sourceReceipt) error {
	return writeSourceReceiptContext(context.Background(), path, receipt)
}

func writeSourceReceiptContext(ctx context.Context, path string, receipt sourceReceipt, directories ...*os.File) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "writing a source receipt requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "writing source receipt %s", path)
	}
	b, err := canonical.Marshal(receipt)
	if err != nil {
		return err
	}
	var parent *os.File
	if len(directories) != 0 {
		parent = directories[0]
	}
	var tmp *os.File
	var tmpName string
	if parent == nil {
		// Ordinary Local fetching retains its developer-selected workspace path.
		// Only the separately admitted hosted route supplies a held receipt parent.
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return e.Wrap(e.CodeFetchFailed, err, "creating source receipt directory")
		}
		tmp, err = os.CreateTemp(filepath.Dir(path), ".source-receipt-")
		if err == nil {
			tmpName = tmp.Name()
		}
	} else {
		tmp, tmpName, err = securepath.CreateTemporarySibling(parent)
	}
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "creating source receipt")
	}
	defer func() {
		if parent == nil {
			_ = os.Remove(tmpName)
		} else {
			_ = unix.Unlinkat(int(parent.Fd()), tmpName, 0)
		}
	}()
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "publishing source receipt %s", path)
	}
	if parent == nil {
		if err := os.Rename(tmpName, path); err != nil {
			return e.Wrap(e.CodeFetchFailed, err, "publishing source receipt")
		}
		return nil
	}
	if err := unix.Renameat(int(parent.Fd()), tmpName, int(parent.Fd()), filepath.Base(path)); err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "publishing source receipt")
	}
	if err := parent.Sync(); err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "syncing source receipt directory")
	}
	return nil
}
