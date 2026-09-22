package catalog

import (
	"context"
	"reflect"
	"sort"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/target"
)

// SourcePinRequest selects one explicit source pin or a sequential source bump.
// Check resolves the same changes but withholds the lock commit.
type SourcePinRequest struct {
	Directory string
	Sources   []string
	Tag       string
	Bump      bool
	All       bool
	Check     bool
}

// SourcePinChange describes one completed resolution, including no-op selections.
// It is descriptive and never substitutes for the lock's private CAS snapshot.
type SourcePinChange struct {
	Name    string
	Before  Pin
	After   Pin
	Changed bool
}

type SourcePinUpdate struct {
	Pins    []SourcePinChange
	Changed int
}

// UpdateSourcePins owns source selection, upstream resolution and the lock CAS.
// A failure can return already-resolved descriptions, but never commits a partial
// update. Named sources retain request order: an unknown later source is rejected
// after earlier resolutions, matching the existing maintenance contract.
func UpdateSourcePins(ctx context.Context, request SourcePinRequest) (SourcePinUpdate, error) {
	return updateSourcePins(ctx, request, ListTags, ResolvePin)
}

func updateSourcePins(ctx context.Context, request SourcePinRequest,
	listTags func(context.Context, string) ([]string, error),
	resolvePin func(context.Context, Source, string) (Pin, error),
) (SourcePinUpdate, error) {
	var update SourcePinUpdate
	sources, err := ReadSourcesContext(ctx, Dir(request.Directory))
	if err != nil {
		return update, err
	}
	var targets []string
	switch {
	case !request.Bump:
		if len(request.Sources) != 1 {
			return update, e.New(e.CodeUnsupported, "pin: expects exactly one <source>")
		}
		targets = request.Sources
	case len(request.Sources) > 0:
		targets = request.Sources
	case request.All:
		targets = SortedSourceNames(sources)
	default:
		return update, e.New(e.CodeUnsupported, "bump: name a source or pass --all")
	}
	lock, err := LoadLockContext(ctx, request.Directory)
	if err != nil {
		return update, err
	}
	for _, name := range targets {
		source, ok := sources[name]
		if !ok {
			return update, e.New(e.CodeInvalidCatalog, "unknown source %q", name)
		}
		tag := request.Tag
		if request.Bump || tag == "" {
			tags, err := listTags(ctx, source.Repo)
			if err != nil {
				return update, err
			}
			tag, err = SelectTagForSource(tags, source)
			if err != nil {
				return update, err
			}
		}
		pin, err := resolvePin(ctx, source, tag)
		if err != nil {
			return update, err
		}
		change := SourcePinChange{Name: name, Before: lock.Pins[name], After: pin}
		change.Changed = !reflect.DeepEqual(change.Before, pin)
		update.Pins = append(update.Pins, change)
		if change.Changed {
			lock.Pins[name] = pin
			update.Changed++
		}
	}
	if update.Changed == 0 {
		return update, nil
	}
	if request.Check {
		return update, e.New(e.CodeDiff, "%d pin(s) would change (dry-run: --check)", update.Changed)
	}
	return update, WriteLockContext(ctx, request.Directory, lock)
}

// ArtifactPinRequest selects complete declared architecture sets. There is no
// artifact check mode; source bump dry-runs remain a separate operation.
type ArtifactPinRequest struct {
	Directory     string
	Components    []string
	Architectures []string
	All           bool
	Runtime       string
}

type ArtifactPinChange struct {
	Name    string
	Before  ArtifactPin
	After   ArtifactPin
	Changed bool
}

type ArtifactPinUpdate struct {
	Pins    []ArtifactPinChange
	Changed int
}

// UpdateArtifactPins admits the entire component/architecture selection before
// contacting an upstream and commits every resolved pin against one lock snapshot.
func UpdateArtifactPins(ctx context.Context, request ArtifactPinRequest) (ArtifactPinUpdate, error) {
	var update ArtifactPinUpdate
	targets, err := ReadPrebuiltTargetsContext(ctx, Dir(request.Directory))
	if err != nil {
		return update, err
	}
	if len(targets) == 0 {
		return update, e.New(e.CodeUnsupported, "no prebuilt components in %s/%s", request.Directory, RegistryFile)
	}
	want := map[string]bool{}
	for _, name := range request.Components {
		if want[name] {
			return update, e.New(e.CodeUsage, "pin-artifacts: component %q is selected more than once", name)
		}
		want[name] = true
	}
	if !request.All && len(want) == 0 {
		return update, e.New(e.CodeUnsupported, "pin-artifacts: name a component or pass --all")
	}
	available := make(map[string]bool, len(targets))
	for _, selected := range targets {
		available[selected.Name] = true
	}
	for _, name := range request.Components {
		if !available[name] {
			return update, e.New(e.CodeInvalidCatalog, "pin-artifacts: unknown prebuilt component %q", name)
		}
	}
	for _, selected := range targets {
		if !request.All && !want[selected.Name] {
			continue
		}
		if len(request.Architectures) > 0 {
			declared := make([]string, 0, len(selected.Platforms))
			for _, platform := range selected.Platforms {
				declared = append(declared, target.Arch(platform))
			}
			requested := append([]string(nil), request.Architectures...)
			sort.Strings(requested)
			if !reflect.DeepEqual(requested, declared) {
				return update, e.New(e.CodeUsage, "component %q requires exact declared architectures %v", selected.Name, declared)
			}
		}
	}
	lock, err := LoadLockContext(ctx, request.Directory)
	if err != nil {
		return update, err
	}
	if lock.Artifacts == nil {
		lock.Artifacts = map[string]ArtifactPin{}
	}
	for _, selected := range targets {
		if !request.All && !want[selected.Name] {
			continue
		}
		arches := make([]string, 0, len(selected.Platforms))
		for _, platform := range selected.Platforms {
			arches = append(arches, target.Arch(platform))
		}
		digests := map[string]string{}
		if selected.Image != "" {
			digests, err = resolveImageDigests(ctx, request.Runtime, selected.Image, arches)
			if err != nil {
				return update, err
			}
		} else {
			for _, arch := range arches {
				digest, err := resolveBinaryDigest(ctx, strings.ReplaceAll(selected.URL, "{{.Arch}}", arch))
				if err != nil {
					return update, err
				}
				digests[arch] = digest
			}
		}
		pin := ArtifactPin{Tag: selected.Tag, Digests: digests}
		change := ArtifactPinChange{Name: selected.Name, Before: lock.Artifacts[selected.Name], After: pin}
		change.Changed = !artifactPinEqual(change.Before, pin)
		update.Pins = append(update.Pins, change)
		if change.Changed {
			lock.Artifacts[selected.Name] = pin
			update.Changed++
		}
	}
	if update.Changed == 0 {
		return update, nil
	}
	return update, WriteLockContext(ctx, request.Directory, lock)
}
