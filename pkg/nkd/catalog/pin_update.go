package catalog

import (
	"context"
	"crypto/sha256"
	"reflect"
	"sort"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/target"
)

// Pin updates read one snapshot, resolve the requested pins, validate the complete
// candidate, and save it atomically. Both source and artifact workflows live here.

// SourcePinRequest selects one explicit source pin or a sequential source bump.
// Check resolves the same changes but withholds the lock commit.
type SourcePinRequest struct {
	Directory string
	Sources   []string
	Tag       string
	Runtime   string // Container executable for acquired image members; defaults to docker.
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
	Pins      []SourcePinChange
	Artifacts []ArtifactPinChange
	Changed   int
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
	// Read one snapshot and select the independent sources to update.
	var update SourcePinUpdate
	if request.Runtime == "" {
		request.Runtime = "docker"
	}
	reg, lock, err := pinUpdateInputs(ctx, request.Directory)
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
		for _, n := range sortedSourceNames(reg.Sources) {
			if reg.Sources[n].PinnedBy == "" {
				targets = append(targets, n)
			}
		}
	default:
		return update, e.New(e.CodeUnsupported, "bump: name a source or pass --all")
	}
	// Member selections are admitted before any upstream lookup, including a mixed
	// request naming the umbrella and one of its members.
	selected := map[string]bool{}
	for _, n := range targets {
		if selected[n] {
			return update, e.New(e.CodeUsage, "source %q selected more than once", n)
		}
		selected[n] = true
		if src, ok := reg.Sources[n]; ok && src.PinnedBy != "" {
			return update, pinSetMemberError(n, src.PinnedBy)
		}
	}
	if lock.Artifacts == nil {
		lock.Artifacts = map[string]ArtifactPin{}
	}
	// Resolve each source and its declared members into the candidate lock.
	updated := map[string]bool{}
	var apply func(string, string, string, string) error
	apply = func(name, tag, owner, declaredVersion string) error {
		if err := pinContextError(ctx); err != nil {
			return err
		}
		if updated[name] {
			return e.New(e.CodeInvalidCatalog, "source %q resolved twice", name)
		}
		updated[name] = true
		src, ok := reg.Sources[name]
		if !ok {
			return e.New(e.CodeInvalidCatalog, "unknown source %q", name)
		}
		if tag == "" {
			tags, err := listTags(ctx, src.Repo)
			if err != nil {
				return err
			}
			tag, err = SelectTagForSource(tags, src)
			if err != nil {
				return err
			}
		}
		pin, err := resolvePin(ctx, src, tag)
		if err != nil {
			return err
		}
		pin.DerivedFrom = owner
		change := SourcePinChange{Name: name, Before: lock.Pins[name], After: pin}
		change.Changed = !reflect.DeepEqual(change.Before, pin)
		update.Pins = append(update.Pins, change)
		if change.Changed {
			update.Changed++
		}
		lock.Pins[name] = pin
		if src.PinSet != nil {
			versions, err := derivePinSet(ctx, reg, name, pin)
			if err != nil {
				return err
			}
			provenance := name + "@" + pin.Commit
			for _, key := range pinSetMemberNames(src.PinSet) {
				member := src.PinSet.Members[key]
				if member.Source != "" {
					if err := apply(member.Source, reg.Sources[member.Source].QualifiedTag(versions[key]), provenance, versions[key]); err != nil {
						return err
					}
					continue
				}
				raw := reg.Components[member.Artifact]
				versionPin := pin
				versionPin.Tag = versions[key]
				if err := updateDerivedArtifact(ctx, raw, member.Artifact, versionPin, provenance, versions[key], request.Runtime, lock, &update); err != nil {
					return err
				}
			}
		}
		// A source member may also provide versions for prebuilt producers. Those
		// artifact pins belong to the same atomic update, rather than becoming stale.
		if owner != "" {
			for _, component := range sortedRawNames(reg.Components) {
				raw := reg.Components[component]
				if raw.Source != name || raw.Prebuilt == nil {
					continue
				}
				raw.PinnedBy = src.PinnedBy
				if err := updateDerivedArtifact(ctx, raw, component, pin, owner, declaredVersion, request.Runtime, lock, &update); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, name := range targets {
		tag := request.Tag
		if request.Bump {
			tag = ""
		}
		if err := apply(name, tag, "", ""); err != nil {
			return update, err
		}
	}
	// Validate the whole candidate before either reporting a diff or saving it.
	if err := validateLockAgainstRegistry(reg, lock, true); err != nil {
		return update, err
	}
	if err := validateDerivedPins(ctx, reg, lock); err != nil {
		return update, err
	}
	if _, err := serializePinLock(lock); err != nil {
		return update, err
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
	reg, lock, err := pinUpdateInputs(ctx, request.Directory)
	if err != nil {
		return update, err
	}
	if err := validateLockAgainstRegistry(reg, lock, false); err != nil {
		return update, err
	}
	targets, err := prebuiltTargetsFrom(reg, lock)
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
		if selected.PinnedBy != "" {
			return update, pinSetMemberError(selected.Name, selected.PinnedBy)
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
	if err := validateDerivedPins(ctx, reg, lock); err != nil {
		return update, err
	}
	if lock.Artifacts == nil {
		lock.Artifacts = map[string]ArtifactPin{}
	}
	for _, selected := range targets {
		if !request.All && !want[selected.Name] {
			continue
		}
		pin, err := resolvePrebuiltTarget(ctx, selected, request.Runtime)
		if err != nil {
			return update, err
		}
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

func updateDerivedArtifact(ctx context.Context, raw rawComponent, name string, version Pin, provenance, chartVersion, runtime string, lock *PinLock, update *SourcePinUpdate) error {
	acquisition, err := prebuiltTargetFor(name, raw, version)
	if err != nil {
		return err
	}
	// Source tags may include a module prefix. Chart lookup uses the exact
	// upstream declaration; the artifact lock still tracks the source Git tag.
	if acquisition.Chart != nil {
		if !exactPinSetVersion(chartVersion) {
			return e.New(e.CodeInvalidCatalog, "derived chart %q requires an exact declared version", name)
		}
		acquisition.Tag = chartVersion
	}
	pin, err := resolvePrebuiltTarget(ctx, acquisition, runtime)
	if err != nil {
		return err
	}
	pin.Tag = version.Tag
	pin.DerivedFrom = provenance
	change := ArtifactPinChange{Name: name, Before: lock.Artifacts[name], After: pin}
	change.Changed = !artifactPinEqual(change.Before, pin)
	update.Artifacts = append(update.Artifacts, change)
	if change.Changed {
		update.Changed++
	}
	lock.Artifacts[name] = pin
	return nil
}

// Read declarations from the same snapshot that authorizes the final CAS. A
// registry edit between either read is a conflict, never a mixed-input update.
func pinUpdateInputs(ctx context.Context, directory string) (*registryDoc, *PinLock, error) {
	lock, err := LoadLockContext(ctx, directory)
	if err != nil {
		return nil, nil, err
	}
	provider, err := BindContext(ctx, Dir(directory))
	if err != nil {
		return nil, nil, err
	}
	raw, err := provider.ReadFile(RegistryFile)
	if err != nil {
		return nil, nil, e.Wrap(e.CodeInvalidCatalog, err, "reading pin registry")
	}
	if sha256.Sum256(raw) != lock.revision.registryDigest {
		return nil, nil, e.New(e.CodeWriteConflict, "registry changed while reading pin inputs")
	}
	reg, err := decodeRegistry(raw, RegistryFile)
	return reg, lock, err
}
