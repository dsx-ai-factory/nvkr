package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/NVIDIA/nke-recipes/internal/ownedcmd"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"sigs.k8s.io/yaml"
)

// This file implements the automatable half of the ADR-22 pin lifecycle:
// resolving an upstream tag to a full pin and writing the machine-managed lock, so
// `nkdctl catalog pin`/`bump` — and a scheduled CI job — keep pins current without
// editing the hand-authored registry or the binary.

const (
	lockAPIVersion = "nkd.dev/lock/v1"
	lockKind       = "PinLock"
)

// readSources returns the declared sources without requiring the lock.
func readSources(p DataProvider) (map[string]Source, error) {
	reg, err := readRegistry(p)
	if err != nil {
		return nil, err
	}
	return reg.Sources, nil
}

// ReadSourcesContext is ReadSources through the bounded context-aware provider
// contract.
func ReadSourcesContext(ctx context.Context, p DataProvider) (map[string]Source, error) {
	bound, err := BindContext(ctx, p)
	if err != nil {
		return nil, err
	}
	sources, err := readSources(bound)
	return sources, normalizeCatalogContextError(ctx, err, "catalog source read")
}

// PrebuiltTarget is a prebuilt component's acquisition, resolved against its version
// pin for `catalog pin-artifacts`: URL/Image are version-rendered (a binary URL still
// carries the {{.Arch}} token; an image ref does not), and Tag records the version the
// resolved digests will correspond to.
type PrebuiltTarget struct {
	Platforms []string
	Name      string
	Tag       string
	URL       string
	Image     string
}

// ReadPrebuiltTargets returns the prebuilt components of a registry with their
// acquisition refs version-rendered from the lock's source pins — WITHOUT requiring
// artifact digests to exist yet, so `catalog pin-artifacts` can resolve them on a first
// run (full Load would fail closed on the not-yet-pinned digests). Targets are returned
// in stable (sorted-by-name) order.
func readPrebuiltTargets(p DataProvider) ([]PrebuiltTarget, error) {
	reg, err := readRegistry(p)
	if err != nil {
		return nil, err
	}
	lock, err := readLock(p)
	if err != nil {
		return nil, err
	}
	if err := validateLockAgainstRegistry(reg, lock, false); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(reg.Components))
	for n := range reg.Components {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []PrebuiltTarget
	for _, name := range names {
		raw := reg.Components[name]
		if raw.Prebuilt == nil {
			continue
		}
		pin, ok := lock.Pins[raw.Source]
		if !ok {
			return nil, e.New(e.CodeUnpinnedSource,
				"prebuilt component %q: source %q has no pin in the lock (run: nkdctl catalog pin %s)", name, raw.Source, raw.Source)
		}
		url, err := renderRef("prebuilt.url", raw.Prebuilt.URL, pin)
		if err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "prebuilt component %q", name)
		}
		img, err := renderRef("prebuilt.image", raw.Prebuilt.Image, pin)
		if err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "prebuilt component %q", name)
		}
		out = append(out, PrebuiltTarget{Name: name, Tag: pin.Tag, URL: url, Image: img, Platforms: append([]string(nil), raw.Platforms...)})
	}
	return out, nil
}

// ReadPrebuiltTargetsContext is ReadPrebuiltTargets through the bounded
// context-aware provider contract.
func ReadPrebuiltTargetsContext(ctx context.Context, p DataProvider) ([]PrebuiltTarget, error) {
	bound, err := BindContext(ctx, p)
	if err != nil {
		return nil, err
	}
	targets, err := readPrebuiltTargets(bound)
	return targets, normalizeCatalogContextError(ctx, err, "catalog prebuilt-target read")
}

// LoadLockContext reads the registry and optional lock through one bounded,
// no-follow context provider. The returned value carries a private CAS token consumed
// by WriteLock.
func LoadLockContext(ctx context.Context, dir string) (*PinLock, error) {
	if err := requireCatalogContext(ctx, "catalog lock snapshot"); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "resolving catalog directory %q", dir)
	}
	bound, err := BindContext(ctx, Dir(abs))
	if err != nil {
		return nil, err
	}
	if err := rejectLegacyComposition(bound); err != nil {
		return nil, err
	}
	registryRaw, registryExists, err := readOptionalProviderFile(bound, RegistryFile)
	if err != nil {
		return nil, catalogLockReadError(ctx, err, RegistryFile)
	}
	if !registryExists {
		return nil, e.New(e.CodeInvalidCatalog, "required %s is missing from %q", RegistryFile, abs)
	}
	if _, err := decodeRegistry(registryRaw, filepath.Join(abs, RegistryFile)); err != nil {
		return nil, err
	}
	raw, lockExists, err := readOptionalProviderFile(bound, LockFile)
	if err != nil {
		return nil, catalogLockReadError(ctx, err, LockFile)
	}
	if !lockExists {
		return &PinLock{
			APIVersion: lockAPIVersion,
			Kind:       lockKind,
			Pins:       map[string]Pin{},
			revision:   revisionOf(nil, false, abs).withRegistry(registryRaw),
		}, nil
	}
	lock, err := decodePinLock(raw, filepath.Join(abs, LockFile))
	if err != nil {
		return nil, err
	}
	lock.revision = revisionOf(raw, true, abs).withRegistry(registryRaw)
	return lock, nil
}

func catalogLockReadError(ctx context.Context, err error, name string) error {
	if ctx == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return normalizeCatalogContextError(ctx, err, "catalog lock snapshot")
	}
	return e.Wrap(e.CodeInvalidCatalog, err, "reading %s", name)
}

func readOptionalProviderFile(provider DataProvider, name string) ([]byte, bool, error) {
	raw, err := provider.ReadFile(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return raw, true, nil
}

// WriteLockContext atomically replaces the lock iff the exact bytes observed by
// LoadLock are still current. It stages in the target directory, fsyncs file and
// directory, and serializes same-checkout writers with an advisory lock. Cancellation
// before rename aborts and removes the stage; after rename, commit wins and durability
// work completes. A stale writer receives CodeWriteConflict.
func WriteLockContext(ctx context.Context, dir string, lock *PinLock) error {
	if err := requireCatalogContext(ctx, "catalog lock write"); err != nil {
		return err
	}
	if lock == nil {
		return e.New(e.CodeInvalidCatalog, "cannot write a nil lock")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "resolving catalog directory %q", dir)
	}
	if !lock.revision.known {
		return e.New(e.CodeWriteConflict, "lock has no authenticated update snapshot; call LoadLock before WriteLock")
	}
	if lock.revision.dir != abs {
		return e.New(e.CodeWriteConflict, "lock snapshot belongs to %q, not %q", lock.revision.dir, abs)
	}

	if err := validatePinLockShape(lock); err != nil {
		return err
	}
	candidate := clonePinLock(lock)
	body, err := yaml.Marshal(candidate)
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "marshaling lock")
	}
	header := "# Machine-managed by `nkdctl catalog pin` / `nkdctl catalog bump`. Do not hand-edit.\n"
	out := append([]byte(header), body...)
	if int64(len(out)) > MaxCatalogFileBytes {
		return e.New(e.CodeInvalidCatalog, "serialized %s exceeds the %d-byte bound", LockFile, MaxCatalogFileBytes)
	}
	if err := requireCatalogContext(ctx, "catalog lock write"); err != nil {
		return err
	}

	bound, err := BindContext(ctx, Dir(abs))
	if err != nil {
		return err
	}
	if err := rejectLegacyComposition(bound); err != nil {
		return err
	}

	guard, err := os.OpenFile(filepath.Join(abs, "."+LockFile+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "opening lock writer guard")
	}
	defer guard.Close()
	if err := syscall.Flock(int(guard.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return e.New(e.CodeWriteConflict, "another catalog lock writer is active")
		}
		return e.Wrap(e.CodeInvalidCatalog, err, "locking lock writer guard")
	}
	defer func() { _ = syscall.Flock(int(guard.Fd()), syscall.LOCK_UN) }() // best effort during return
	if err := requireCatalogContext(ctx, "catalog lock write"); err != nil {
		return err
	}

	target := filepath.Join(abs, LockFile)
	current, exists, err := readLockBytesContext(ctx, target)
	if err != nil {
		return err
	}
	currentRevision := revisionOf(current, exists, abs)
	if currentRevision.exists != lock.revision.exists ||
		(currentRevision.exists && currentRevision.digest != lock.revision.digest) {
		return e.New(e.CodeWriteConflict,
			"%s changed since LoadLock (expected %s, found %s)", LockFile,
			revisionString(lock.revision), revisionString(currentRevision))
	}
	registryRaw, registryExists, err := readLockBytesContext(ctx, filepath.Join(abs, RegistryFile))
	if err != nil {
		return err
	}
	if !lock.revision.registryKnown || !registryExists ||
		sha256.Sum256(registryRaw) != lock.revision.registryDigest {
		return e.New(e.CodeWriteConflict,
			"%s changed since LoadLock; reload catalog sources and lock before resolving pins", RegistryFile)
	}
	reg, err := decodeRegistry(registryRaw, filepath.Join(abs, RegistryFile))
	if err != nil {
		return err
	}
	if err := validateLockAgainstRegistry(reg, candidate, true); err != nil {
		return err
	}
	if err := requireCatalogContext(ctx, "catalog lock write"); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(abs, "."+LockFile+".stage-*")
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "staging %s", LockFile)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return e.Wrap(e.CodeInvalidCatalog, err, "setting staged lock permissions")
	}
	written, err := io.Copy(tmp, catalogContextReader{ctx: ctx, reader: bytes.NewReader(out)})
	if err != nil || written != int64(len(out)) {
		_ = tmp.Close()
		if ctx.Err() != nil {
			return normalizeCatalogContextError(ctx, err, "catalog lock write")
		}
		if err == nil {
			err = io.ErrShortWrite
		}
		return e.Wrap(e.CodeInvalidCatalog, err, "writing staged %s", LockFile)
	}
	if err := requireCatalogContext(ctx, "catalog lock write"); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return e.Wrap(e.CodeInvalidCatalog, err, "syncing staged %s", LockFile)
	}
	if err := tmp.Close(); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "closing staged %s", LockFile)
	}
	if err := requireCatalogContext(ctx, "catalog lock write"); err != nil {
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "atomically replacing %s", LockFile)
	}
	dirHandle, err := os.Open(abs)
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "opening catalog directory for sync")
	}
	if err := dirHandle.Sync(); err != nil {
		_ = dirHandle.Close()
		return e.Wrap(e.CodeInvalidCatalog, err, "syncing catalog directory")
	}
	if err := dirHandle.Close(); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "closing catalog directory")
	}
	committed = true
	lock.APIVersion = candidate.APIVersion
	lock.Kind = candidate.Kind
	lock.revision = revisionOf(out, true, abs).withRegistry(registryRaw)
	return nil
}

func readLockBytesContext(ctx context.Context, path string) ([]byte, bool, error) {
	if err := requireCatalogContext(ctx, "catalog lock read"); err != nil {
		return nil, false, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, false, e.Wrap(e.CodeInvalidCatalog, err, "resolving %s", path)
	}
	provider := Dir(filepath.Dir(abs)).(ContextDataProvider)
	raw, err := provider.ReadFileContext(ctx, filepath.Base(abs))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, false, normalizeCatalogContextError(ctx, err, "catalog lock read")
		}
		return nil, false, e.Wrap(e.CodeInvalidCatalog, err, "reading %s", abs)
	}
	return raw, true, nil
}

// ResolvePin resolves an upstream tag to a full, immutable pin: the peeled commit
// SHA, its tree hash, its committer timestamp (SOURCE_DATE_EPOCH), the .go-version,
// and — when the source declares a ToolchainBaseImage — the resolved toolchain
// image digest for that Go version. Network + git/docker are used only here (a
// fetch/resolve step), never during a build.
func ResolvePin(ctx context.Context, src Source, tag string) (Pin, error) {
	if err := pinContextError(ctx); err != nil {
		return Pin{}, err
	}
	if src.Tool != nil {
		if err := validateToolSource(*src.Tool); err != nil {
			return Pin{}, err
		}
	}
	if err := validateSourceKind(src); err != nil {
		return Pin{}, err
	}
	if src.Kind == SourceGitHubAction && !actionVersion.MatchString(tag) {
		return Pin{}, e.New(e.CodeInvalidCatalog, "GitHub actions require an exact release tag, got %q", tag)
	}
	tag = src.QualifiedTag(tag)
	commit, err := resolveTagCommit(ctx, src.Repo, tag)
	if err != nil {
		return Pin{}, err
	}
	tmp, err := os.MkdirTemp("", "nkd-pin-")
	if err != nil {
		return Pin{}, e.Wrap(e.CodeFetchFailed, err, "temp dir")
	}
	defer os.RemoveAll(tmp)

	for _, args := range [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", src.Repo},
		{"fetch", "-q", "--depth", "1", "origin", commit},
	} {
		if out, err := gitAt(ctx, tmp, args...); err != nil {
			if ctx.Err() != nil {
				return Pin{}, pinContextError(ctx)
			}
			return Pin{}, e.Wrap(e.CodeFetchFailed, err, "git %s: %s", strings.Join(args, " "), out)
		}
	}
	tree, err := gitAt(ctx, tmp, "rev-parse", commit+"^{tree}")
	if err != nil {
		return Pin{}, e.Wrap(e.CodeFetchFailed, err, "resolving tree for %s", commit)
	}
	epochStr, err := gitAt(ctx, tmp, "show", "-s", "--format=%ct", commit)
	if err != nil {
		return Pin{}, e.Wrap(e.CodeFetchFailed, err, "resolving epoch for %s", commit)
	}
	epoch, err := strconv.ParseInt(strings.TrimSpace(epochStr), 10, 64)
	if err != nil {
		return Pin{}, e.Wrap(e.CodeFetchFailed, err, "parsing committer epoch %q", epochStr)
	}

	pin := Pin{Tag: tag, Commit: strings.TrimSpace(commit), TreeHash: strings.TrimSpace(tree), SourceDateEpoch: epoch}

	// .go-version is optional; read it from the fetched commit if present.
	if gv, err := gitAt(ctx, tmp, "show", commit+":.go-version"); err == nil {
		pin.GoVersion = strings.TrimSpace(gv)
	} else if ctx.Err() != nil {
		return Pin{}, pinContextError(ctx)
	}

	// Resolve the toolchain digest from the Go version when the source asks for it.
	if src.ToolchainBaseImage != "" {
		if pin.GoVersion == "" {
			return Pin{}, e.New(e.CodeUnpinnedSource,
				"source declares toolchainBaseImage %q but tag %s has no .go-version to resolve it", src.ToolchainBaseImage, tag)
		}
		dig, err := resolveImageDigest(ctx, src.ToolchainBaseImage+":"+pin.GoVersion)
		if err != nil {
			return Pin{}, err
		}
		pin.ToolchainImage = dig
	}
	if src.Tool != nil {
		tool, err := resolveToolPin(ctx, *src.Tool, pin)
		if err != nil {
			return Pin{}, err
		}
		pin.Tool = &tool
	}
	return pin, nil
}

// resolveImageDigest resolves an official image ref to its immutable repo digest
// (name@sha256:...) by pulling and inspecting it.
func resolveImageDigest(ctx context.Context, ref string) (string, error) {
	if out, err := ownedcmd.CombinedOutput(ctx, "docker", "pull", "-q", ref); err != nil {
		if ctx.Err() != nil {
			return "", pinContextError(ctx)
		}
		return "", e.Wrap(e.CodeFetchFailed, err, "docker pull %s: %s", ref, strings.TrimSpace(string(out)))
	}
	out, err := ownedcmd.Output(ctx, "docker", "inspect", ref, "--format", "{{index .RepoDigests 0}}")
	if err != nil {
		if ctx.Err() != nil {
			return "", pinContextError(ctx)
		}
		return "", e.Wrap(e.CodeFetchFailed, err, "inspecting %s", ref)
	}
	dig := strings.TrimSpace(string(out))
	if !digestPinned.MatchString(dig) {
		return "", e.New(e.CodeFetchFailed, "resolved toolchain %q is not a digest", dig)
	}
	return dig, nil
}

// resolveTagCommit maps a tag to its commit SHA, preferring the peeled (^{})
// target so annotated tags resolve to the underlying commit.
func resolveTagCommit(ctx context.Context, repo, tag string) (string, error) {
	out, err := git(ctx, "ls-remote", repo, "refs/tags/"+tag, "refs/tags/"+tag+"^{}")
	if err != nil {
		if ctx.Err() != nil {
			return "", pinContextError(ctx)
		}
		return "", e.Wrap(e.CodeFetchFailed, err, "ls-remote %s %s", repo, tag)
	}
	var direct, peeled string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		if strings.HasSuffix(f[1], "^{}") {
			peeled = f[0]
		} else {
			direct = f[0]
		}
	}
	if peeled != "" {
		return peeled, nil
	}
	if direct != "" {
		return direct, nil
	}
	return "", e.New(e.CodeFetchFailed, "tag %q not found in %s", tag, repo)
}

// ListTags returns the stable release tags of a repo (peeled refs excluded).
func ListTags(ctx context.Context, repo string) ([]string, error) {
	if err := pinContextError(ctx); err != nil {
		return nil, err
	}
	out, err := git(ctx, "ls-remote", "--tags", "--refs", repo)
	if err != nil {
		if ctx.Err() != nil {
			return nil, pinContextError(ctx)
		}
		return nil, e.Wrap(e.CodeFetchFailed, err, "ls-remote --tags %s", repo)
	}
	var tags []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		tags = append(tags, strings.TrimPrefix(f[1], "refs/tags/"))
	}
	return tags, nil
}

// QualifiedTag returns the exact upstream ref name a version selects for this source.
// A module-scoped source (tagPrefix "cli/") is published as refs/tags/cli/<version>, so
// the prefix is prepended unless the caller already supplied it: `--tag v0.1.0-rc.2` and
// `--tag cli/v0.1.0-rc.2` must resolve the same commit rather than one of them silently
// missing the ref. A root-tagged source returns the tag unchanged.
func (s Source) QualifiedTag(tag string) string {
	if s.TagPrefix == "" || strings.HasPrefix(tag, s.TagPrefix) {
		return tag
	}
	return s.TagPrefix + tag
}

// SelectTag picks the newest stable tag matching a constraint (e.g. 1.32.x, 1.x,
// or an exact 1.32.0). Pre-release tags are ignored. It is the root-tagged form of
// SelectTagForSource.
func SelectTag(tags []string, constraint string) (string, error) {
	return SelectTagForSource(tags, Source{Constraint: constraint})
}

// SelectTagForSource picks the newest stable tag a source's constraint admits, honoring
// its optional module-scoped tag prefix: only tags carrying the prefix are candidates
// (a monorepo's other modules bump independently and must never be selected here), the
// prefix is stripped before semver parsing, and the FULL upstream tag is returned so the
// lock records what git can resolve. Pre-release tags are ignored.
func SelectTagForSource(tags []string, src Source) (string, error) {
	var best string
	var bestV [3]int
	for _, t := range tags {
		v, ok := parseSourceSemver(src, t)
		if !ok || !constraintMatches(src.Constraint, v) {
			continue
		}
		if best == "" || semverLess(bestV, v) {
			best, bestV = t, v
		}
	}
	if best == "" {
		if src.TagPrefix != "" {
			return "", e.New(e.CodeNoMatch,
				"no stable %s-prefixed tag matches constraint %q", src.TagPrefix, src.Constraint)
		}
		return "", e.New(e.CodeNoMatch, "no stable tag matches constraint %q", src.Constraint)
	}
	return best, nil
}

// parseSourceSemver parses an upstream tag as this source's version: a module-scoped
// source admits only its own prefixed tags and parses what follows the prefix.
func parseSourceSemver(src Source, tag string) ([3]int, bool) {
	if src.TagPrefix != "" {
		rest, ok := strings.CutPrefix(tag, src.TagPrefix)
		if !ok {
			return [3]int{}, false
		}
		tag = rest
	}
	return parseSemver(tag)
}

// parseSemver parses a stable vMAJOR.MINOR.PATCH tag; pre-release/build tags return
// ok=false so `bump` only tracks stable releases. It deliberately does NOT strip a
// module-scoped prefix: a source that declares none must not select another module's
// tag, so `cli/v0.2.0` fails to parse here and is skipped.
func parseSemver(tag string) ([3]int, bool) {
	v := strings.TrimPrefix(tag, "v")
	if strings.ContainsAny(v, "-+") {
		return [3]int{}, false
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// constraintMatches reports whether v satisfies a dotted constraint whose segments
// are exact numbers or the wildcard x/*.
func constraintMatches(constraint string, v [3]int) bool {
	segs := strings.Split(NormalizeVersion(constraint), ".")
	for i := 0; i < len(segs) && i < 3; i++ {
		s := segs[i]
		if s == "x" || s == "*" || s == "" {
			continue
		}
		n, err := strconv.Atoi(s)
		if err != nil || n != v[i] {
			return false
		}
	}
	return true
}

func semverLess(a, b [3]int) bool {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// SortedSourceNames returns the source names of a registry in stable order.
func SortedSourceNames(sources map[string]Source) []string {
	names := make([]string, 0, len(sources))
	for n := range sources {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func git(ctx context.Context, args ...string) (string, error) {
	out, err := ownedcmd.CombinedOutput(ctx, "git", args...)
	return string(out), err
}

func gitAt(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := ownedcmd.CombinedOutput(ctx, "git", append([]string{"-C", dir}, args...)...)
	return string(out), err
}

func pinContextError(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "catalog pin resolution requires a context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "catalog pin resolution canceled")
	}
	return nil
}
