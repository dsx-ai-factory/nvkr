package pipeline

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"regexp"
	"sort"
	"strings"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"sigs.k8s.io/yaml"
)

// The pinned GitHub Actions the CI emitters render are DATA, not code (mirroring
// catalog pin locks: a security bump is a lockfile change + regenerate via
// `nkdctl actions bump`, not a source edit + recompile. The lockfile is embedded so the pin
// set still ships with — and is versioned with — the nkdctl binary that emits it.
//
//go:embed actions.lock.yaml
var embeddedActionLock []byte

// ActionLockAPIVersion / ActionLockKind identify the action lockfile document.
const (
	ActionLockAPIVersion = "nkd.dev/lock/v1"
	ActionLockKind       = "ActionLock"
)

// ActionPin is one pinned third-party GitHub Action. SHA is the immutable commit the emitters
// render; Track is the moving major tag (e.g. "v5") that `nkdctl actions bump` resolves to the
// next SHA; Version is the concrete release tag (e.g. "v5.6.0") that SHA corresponds to.
type ActionPin struct {
	Repo    string `json:"repo"`
	Track   string `json:"track"`
	Version string `json:"version"`
	SHA     string `json:"sha"`
}

// Uses renders the `uses:` reference an emitted workflow pins, e.g. actions/checkout@<sha>.
func (p ActionPin) Uses() string { return p.Repo + "@" + p.SHA }

// ActionLock is the machine-managed set of pinned actions.
type ActionLock struct {
	APIVersion string               `json:"apiVersion"`
	Kind       string               `json:"kind"`
	Actions    map[string]ActionPin `json:"actions"`
}

var (
	actionSHA   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	actionRepo  = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)
	actionTrack = regexp.MustCompile(`^v[1-9][0-9]*$`)
)

// Validate fails closed on an unpinned or malformed lock: an action that is not pinned to an
// exact commit is a supply-chain hole, not a convenience.
func (l ActionLock) Validate() error {
	if l.APIVersion != ActionLockAPIVersion || l.Kind != ActionLockKind {
		return e.New(e.CodeInvalidCatalog, "action lock has unsupported apiVersion/kind %q/%q", l.APIVersion, l.Kind)
	}
	if len(l.Actions) == 0 {
		return e.New(e.CodeInvalidCatalog, "action lock declares no actions")
	}
	for name, pin := range l.Actions {
		if strings.TrimSpace(name) == "" {
			return e.New(e.CodeInvalidCatalog, "action lock has an empty action name")
		}
		if !actionRepo.MatchString(pin.Repo) {
			return e.New(e.CodeInvalidCatalog, "action %q repo %q is not owner/name", name, pin.Repo)
		}
		if !actionSHA.MatchString(pin.SHA) {
			return e.New(e.CodeUnpinnedSource, "action %q must pin an exact 40-hex commit sha, got %q", name, pin.SHA)
		}
		if !actionTrack.MatchString(pin.Track) {
			return e.New(e.CodeInvalidCatalog, "action %q track %q must be a major tag such as v6", name, pin.Track)
		}
	}
	return nil
}

// ParseActionLock decodes and validates an action lockfile. UnmarshalStrict rejects unknown
// keys so a typo or a hand-edit that drops the pin cannot slip through.
func ParseActionLock(raw []byte) (ActionLock, error) {
	var l ActionLock
	if err := yaml.UnmarshalStrict(raw, &l); err != nil {
		return ActionLock{}, e.Wrap(e.CodeInvalidCatalog, err, "parsing action lock")
	}
	if err := l.Validate(); err != nil {
		return ActionLock{}, err
	}
	return l, nil
}

// Marshal renders the lockfile deterministically (fixed header, apiVersion/kind, then actions
// sorted by name with fields in a stable order) so a `bump` rewrite is a minimal, reviewable
// diff and the committed bytes round-trip exactly.
func (l ActionLock) Marshal() ([]byte, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString("# Machine-managed by `nkdctl actions bump`. Do not hand-edit.\n")
	b.WriteString("# The pinned third-party GitHub Actions the CI emitters render into generated workflows.\n")
	b.WriteString("# Each is pinned to an immutable commit sha; `track` is the moving major tag bump follows.\n")
	fmt.Fprintf(&b, "apiVersion: %s\n", l.APIVersion)
	fmt.Fprintf(&b, "kind: %s\n", l.Kind)
	b.WriteString("actions:\n")
	names := make([]string, 0, len(l.Actions))
	for name := range l.Actions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := l.Actions[name]
		fmt.Fprintf(&b, "  %s:\n", name)
		fmt.Fprintf(&b, "    repo: %s\n", p.Repo)
		fmt.Fprintf(&b, "    track: %s\n", p.Track)
		fmt.Fprintf(&b, "    version: %s\n", p.Version)
		fmt.Fprintf(&b, "    sha: %s\n", p.SHA)
	}
	return b.Bytes(), nil
}

// Pin returns the named action or an error naming what is available — the emitters must never
// silently render an empty `uses:`.
func (l ActionLock) Pin(name string) (ActionPin, error) {
	pin, ok := l.Actions[name]
	if !ok {
		return ActionPin{}, e.New(e.CodeInvalidCatalog, "action lock has no action %q", name)
	}
	return pin, nil
}

// RefResolver resolves a repo's moving track (e.g. "v5") to the exact commit sha it points to
// and the most specific release tag for that sha. It is an interface so `bump` is testable
// without a live GitHub API.
type RefResolver interface {
	Resolve(ctx context.Context, repo, track string) (sha, version string, err error)
}

// ActionChange records one pin the bump moved, for a reviewable summary.
type ActionChange struct {
	Name       string
	OldSHA     string
	NewSHA     string
	OldVersion string
	NewVersion string
}

// ActionTrackChange records an intentional major-track transition separately from the
// resolved commit change. A track-only transition must still rewrite the machine lock.
type ActionTrackChange struct {
	Name     string
	OldTrack string
	NewTrack string
}

// Retarget returns a copy with explicit major-track overrides applied. Unknown actions and
// malformed tracks fail before any resolver I/O in the caller.
func (l ActionLock) Retarget(overrides map[string]string) (ActionLock, []ActionTrackChange, error) {
	if err := l.Validate(); err != nil {
		return ActionLock{}, nil, err
	}
	out := ActionLock{APIVersion: l.APIVersion, Kind: l.Kind, Actions: make(map[string]ActionPin, len(l.Actions))}
	for name, pin := range l.Actions {
		out.Actions[name] = pin
	}
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	sort.Strings(names)
	var changes []ActionTrackChange
	for _, name := range names {
		pin, ok := out.Actions[name]
		if !ok {
			return ActionLock{}, nil, e.New(e.CodeUsage, "action lock has no action %q to retarget", name)
		}
		track := overrides[name]
		if !actionTrack.MatchString(track) {
			return ActionLock{}, nil, e.New(e.CodeUsage, "action %q track %q must be a major tag such as v6", name, track)
		}
		if pin.Track == track {
			continue
		}
		changes = append(changes, ActionTrackChange{Name: name, OldTrack: pin.Track, NewTrack: track})
		pin.Track = track
		out.Actions[name] = pin
	}
	return out, changes, nil
}

// Bump resolves each selected action's track to the current sha/version and returns the updated
// lock plus the set of changes. only limits the bump to named actions (empty = all). It does not
// write anything; the caller owns the lockfile bytes.
func (l ActionLock) Bump(ctx context.Context, r RefResolver, only []string) (ActionLock, []ActionChange, error) {
	selected := map[string]bool{}
	for _, name := range only {
		if _, ok := l.Actions[name]; !ok {
			return ActionLock{}, nil, e.New(e.CodeUsage, "action lock has no action %q to bump", name)
		}
		selected[name] = true
	}
	out := ActionLock{APIVersion: l.APIVersion, Kind: l.Kind, Actions: make(map[string]ActionPin, len(l.Actions))}
	for name, pin := range l.Actions {
		out.Actions[name] = pin
	}
	var changes []ActionChange
	names := make([]string, 0, len(out.Actions))
	for name := range out.Actions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if len(selected) > 0 && !selected[name] {
			continue
		}
		pin := out.Actions[name]
		sha, version, err := r.Resolve(ctx, pin.Repo, pin.Track)
		if err != nil {
			return ActionLock{}, nil, e.Wrap(e.CodeEngineCapability, err, "resolving %s@%s", pin.Repo, pin.Track)
		}
		if !actionSHA.MatchString(sha) {
			return ActionLock{}, nil, e.New(e.CodeUnpinnedSource, "resolver returned a non-sha ref %q for %s@%s", sha, pin.Repo, pin.Track)
		}
		if sha != pin.SHA {
			changes = append(changes, ActionChange{Name: name, OldSHA: pin.SHA, NewSHA: sha, OldVersion: pin.Version, NewVersion: version})
		}
		pin.SHA, pin.Version = sha, version
		out.Actions[name] = pin
	}
	return out, changes, nil
}

// embeddedActions is the compiled-in pin set, validated at package init. A malformed embedded
// lockfile is a build/test-time failure, never a silent runtime one.
var embeddedActions = mustParseEmbeddedActionLock()

func mustParseEmbeddedActionLock() ActionLock {
	l, err := ParseActionLock(embeddedActionLock)
	if err != nil {
		panic("nkd: embedded actions.lock.yaml is invalid: " + err.Error())
	}
	return l
}

// EmbeddedActionLock returns a copy of the compiled-in pin set (for `actions bump --check` and
// tests), so callers cannot mutate the shared map.
func EmbeddedActionLock() ActionLock {
	out := ActionLock{APIVersion: embeddedActions.APIVersion, Kind: embeddedActions.Kind,
		Actions: make(map[string]ActionPin, len(embeddedActions.Actions))}
	for name, pin := range embeddedActions.Actions {
		out.Actions[name] = pin
	}
	return out
}

func mustEmbeddedUses(name string) string {
	pin, err := embeddedActions.Pin(name)
	if err != nil {
		panic("nkd: " + err.Error())
	}
	return pin.Uses()
}

// The pinned GitHub Action `uses:` references both GitHub emitters render, sourced from the
// embedded lockfile so a security bump is a data change (`nkdctl actions bump`), not a code
// edit. The factory review-topology generator aliases these, so a bump lands in exactly one
// place and cannot drift between the two emitters.
var (
	GitHubCheckoutAction = mustEmbeddedUses("checkout")
	GitHubUploadAction   = mustEmbeddedUses("upload-artifact")
	GitHubDownloadAction = mustEmbeddedUses("download-artifact")
	GitHubSetupGoAction  = mustEmbeddedUses("setup-go")
)
