package catalog

import (
	"context"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/NVIDIA/nke-recipes/internal/ownedcmd"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

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
	return resolveSourceRevision(ctx, src, tag, commit)
}

// ResolveCommitPin pins one exact Git revision without pretending it is a release
// tag. The lock's descriptive tag is the full commit reported by the held tree;
// release tools and GitHub actions retain their exact-release-tag contract.
func ResolveCommitPin(ctx context.Context, src Source, commit string) (Pin, error) {
	if err := pinContextError(ctx); err != nil {
		return Pin{}, err
	}
	if err := validateSourceKind(src); err != nil {
		return Pin{}, err
	}
	if !fullSHA.MatchString(commit) || src.Kind == SourceGitHubAction || src.Tool != nil {
		return Pin{}, e.New(e.CodeInvalidCatalog, "exact commit pin requires a full Git commit and a source without a release tool or GitHub action")
	}
	return resolveSourceRevision(ctx, src, commit, commit)
}

func resolveSourceRevision(ctx context.Context, src Source, tag, commit string) (Pin, error) {
	return resolveSourceRevisionWithGit(ctx, src, tag, commit, gitAt)
}

func resolveSourceRevisionWithGit(ctx context.Context, src Source, tag, commit string,
	gitAt func(context.Context, string, ...string) (string, error),
) (Pin, error) {
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
	heldCommit, err := gitAt(ctx, tmp, "rev-parse", "FETCH_HEAD^{commit}")
	if err != nil || strings.TrimSpace(heldCommit) != commit {
		return Pin{}, e.Wrap(e.CodeFetchFailed, err, "fetched revision does not match exact source commit %s", commit)
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
