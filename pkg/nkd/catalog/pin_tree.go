package catalog

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/NVIDIA/nke-recipes/internal/ownedcmd"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

const (
	pinTreeFileLimit  = 4 << 20
	pinTreeTotalLimit = 32 << 20
	pinTreeReadLimit  = 1024
	pinTreeTimeout    = 2 * time.Minute
)

// pinnedTree exposes only bounded regular blobs from one authenticated Git tree.
// Close owns removal of the private repository, including on a failed open.
type pinnedTree interface {
	ReadFile(context.Context, string) ([]byte, error)
	Close() error
}

type gitPinnedTree struct {
	dir   string
	tree  string
	gate  chan struct{}
	reads int
	total int64
	done  bool
}

func openPinnedTree(ctx context.Context, src Source, pin Pin) (pinnedTree, error) {
	if err := pinContextError(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(src.Repo) == "" || !fullSHA.MatchString(pin.Commit) ||
		!fullSHA.MatchString(pin.TreeHash) || pin.SourceDateEpoch <= 0 {
		return nil, e.New(e.CodeInvalidCatalog, "pin-set tree requires a repository, full commit/tree hashes, and a positive sourceDateEpoch")
	}
	ctx, cancel := context.WithTimeout(ctx, pinTreeTimeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "nkd-pin-tree-")
	if err != nil {
		return nil, e.Wrap(e.CodeFetchFailed, err, "creating pinned-tree repository")
	}
	tree := &gitPinnedTree{dir: dir, tree: pin.TreeHash, gate: make(chan struct{}, 1)}
	ok := false
	defer func() {
		if !ok {
			_ = tree.Close()
		}
	}()
	for _, args := range [][]string{
		{"init", "--bare", "--template=", "-q"},
		{"fetch", "-q", "--depth=1", "--no-tags", "--recurse-submodules=no", "--no-write-fetch-head", "--", src.Repo, pin.Commit},
	} {
		if _, err := pinTreeGit(ctx, dir, 8192, args...); err != nil {
			return nil, err
		}
	}
	kind, err := pinTreeGit(ctx, dir, 128, "cat-file", "-t", pin.Commit)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(kind)) != "commit" {
		return nil, e.New(e.CodeInvalidCatalog, "pin-set commit %s is not a commit object", pin.Commit)
	}
	metadata, err := pinTreeGit(ctx, dir, 256, "show", "-s", "--format=%T%n%ct", "--no-show-signature", pin.Commit, "--")
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(string(metadata))
	if len(fields) != 2 || fields[0] != pin.TreeHash || fields[1] != strconv.FormatInt(pin.SourceDateEpoch, 10) {
		return nil, e.New(e.CodeInvalidCatalog, "pin-set commit %s disagrees with its locked treeHash or sourceDateEpoch", pin.Commit)
	}
	ok = true
	return tree, nil
}

// validatePinSetFilePath admits canonical repository-relative paths, never host
// paths or Git revision/pathspec expressions. Reads still use literal pathspecs.
func validatePinSetFilePath(name string) error {
	if name == "" || len(name) > 4096 || !utf8.ValidString(name) || path.IsAbs(name) ||
		path.Clean(name) != name || strings.ContainsAny(name, "\\:") ||
		strings.ContainsFunc(name, unicode.IsControl) {
		return e.New(e.CodeInvalidCatalog, "pin-set file %q is not a canonical relative path", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return e.New(e.CodeInvalidCatalog, "pin-set file %q contains a forbidden path component", name)
		}
	}
	return nil
}

func (tree *gitPinnedTree) ReadFile(ctx context.Context, name string) ([]byte, error) {
	if err := pinContextError(ctx); err != nil {
		return nil, err
	}
	if err := validatePinSetFilePath(name); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, pinTreeTimeout)
	defer cancel()
	select {
	case tree.gate <- struct{}{}:
		defer func() { <-tree.gate }()
	case <-ctx.Done():
		return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "waiting for pin-set tree read")
	}
	if tree.done {
		return nil, e.New(e.CodeInvalidCatalog, "pin-set tree is closed")
	}
	if tree.reads >= pinTreeReadLimit {
		return nil, e.New(e.CodeInvalidCatalog, "pin-set tree exceeds %d file reads", pinTreeReadLimit)
	}
	tree.reads++
	entry, err := pinTreeGit(ctx, tree.dir, 8192, "ls-tree", "-z", tree.tree, "--", name)
	if err != nil {
		return nil, err
	}
	// One exact path must resolve to one regular blob, including executable data
	// files. Trees, symlinks, gitlinks and absent/ambiguous paths are not readable.
	if bytes.Count(entry, []byte{0}) != 1 || len(entry) == 0 || entry[len(entry)-1] != 0 {
		return nil, e.New(e.CodeInvalidCatalog, "pin-set file %q does not identify exactly one regular blob", name)
	}
	header, actual, found := strings.Cut(string(entry[:len(entry)-1]), "\t")
	fields := strings.Fields(header)
	if !found || actual != name || len(fields) != 3 || fields[1] != "blob" ||
		(fields[0] != "100644" && fields[0] != "100755") || !fullSHA.MatchString(fields[2]) {
		return nil, e.New(e.CodeInvalidCatalog, "pin-set file %q is not a regular blob at the exact path", name)
	}
	blob := fields[2]
	sizeText, err := pinTreeGit(ctx, tree.dir, 128, "cat-file", "-s", blob)
	if err != nil {
		return nil, err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(sizeText)), 10, 64)
	if err != nil || size < 0 || size > pinTreeFileLimit || size > pinTreeTotalLimit-tree.total {
		return nil, e.New(e.CodeInvalidCatalog, "pin-set file %q exceeds file/total byte limits or has an invalid blob size", name)
	}
	// Charge an attempted body read too, so repeated failures cannot bypass the
	// aggregate budget. Git must return exactly the size checked above.
	tree.total += size
	body, err := pinTreeGit(ctx, tree.dir, int(size), "cat-file", "blob", blob)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) != size {
		return nil, e.New(e.CodeInvalidCatalog, "pin-set file %q changed size while reading", name)
	}
	return body, nil
}

func (tree *gitPinnedTree) Close() error {
	tree.gate <- struct{}{}
	defer func() { <-tree.gate }()
	if tree.done {
		return nil
	}
	if err := os.RemoveAll(tree.dir); err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "removing private pinned-tree repository")
	}
	tree.done = true
	return nil
}

// pinTreeGit bounds both command duration and captured bytes. No checkout,
// filter, hook, submodule traversal, replacement object or tag resolution runs.
func pinTreeGit(ctx context.Context, dir string, limit int, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, pinTreeTimeout)
	defer cancel()
	stdout := pinTreeBuffer{limit: limit, cancel: cancel}
	stderr := pinTreeBuffer{limit: 16 << 10, cancel: cancel}
	gitArgs := []string{
		"--no-replace-objects", "--literal-pathspecs", "-C", dir,
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		"-c", "protocol.allow=never",
		"-c", "protocol.file.allow=always", "-c", "protocol.https.allow=always",
		"-c", "protocol.http.allow=always", "-c", "protocol.ssh.allow=always", "-c", "protocol.git.allow=always",
		"-c", "protocol.ext.allow=never", "-c", "fetch.fsckObjects=true",
	}
	cmd := exec.CommandContext(ctx, "git", append(gitArgs, args...)...)
	cmd.Env = pinTreeGitEnv()
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := ownedcmd.Run(ctx, cmd)
	if stdout.exceeded || stderr.exceeded {
		return nil, e.New(e.CodeFetchFailed, "pin-set git %s exceeded its output limit", args[0])
	}
	if ctx.Err() != nil {
		return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "pin-set git %s canceled", args[0])
	}
	if err != nil {
		return nil, e.Wrap(e.CodeFetchFailed, err, "pin-set git %s: %s", args[0], strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func pinTreeGitEnv() []string {
	env := make([]string, 0, len(os.Environ()))
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
			"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE", "GIT_SHALLOW_FILE":
			continue
		}
		env = append(env, value)
	}
	return env
}

type pinTreeBuffer struct {
	data     bytes.Buffer
	limit    int
	cancel   context.CancelFunc
	exceeded bool
}

func (buffer *pinTreeBuffer) Write(raw []byte) (int, error) {
	if len(raw) > buffer.limit-buffer.Len() {
		buffer.exceeded = true
		buffer.cancel()
		return 0, e.New(e.CodeFetchFailed, "pin-set git output exceeds %d bytes", buffer.limit)
	}
	return buffer.data.Write(raw)
}

func (buffer *pinTreeBuffer) Len() int       { return buffer.data.Len() }
func (buffer *pinTreeBuffer) Bytes() []byte  { return buffer.data.Bytes() }
func (buffer *pinTreeBuffer) String() string { return buffer.data.String() }
