package catalog

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/target"
	"sigs.k8s.io/yaml"
)

// lockRevision is an exact-byte optimistic-concurrency token. The fields are private
// so only a successful LoadLock can authorize replacement of an existing lock.
type lockRevision struct {
	known          bool
	exists         bool
	dir            string
	digest         [sha256.Size]byte
	registryKnown  bool
	registryDigest [sha256.Size]byte
}

func revisionOf(raw []byte, exists bool, dir string) lockRevision {
	r := lockRevision{known: true, exists: exists, dir: dir}
	if exists {
		r.digest = sha256.Sum256(raw)
	}
	return r
}

func (r lockRevision) withRegistry(raw []byte) lockRevision {
	r.registryKnown = true
	r.registryDigest = sha256.Sum256(raw)
	return r
}

func decodePinLock(raw []byte, where string) (*PinLock, error) {
	var lock PinLock
	if err := yaml.UnmarshalStrict(raw, &lock); err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "parsing %s", where)
	}
	if err := validatePinLockShape(&lock); err != nil {
		return nil, err
	}
	return &lock, nil
}

// validatePinLockShape validates the exact v1 schema independently of a registry.
// Registry-dependent completeness is enforced separately by validateLockAgainstRegistry.
func validatePinLockShape(lock *PinLock) error {
	if lock == nil {
		return e.New(e.CodeInvalidCatalog, "lock is nil")
	}
	if lock.APIVersion != lockAPIVersion {
		return e.New(e.CodeInvalidCatalog, "lock apiVersion %q is unsupported; want exactly %q", lock.APIVersion, lockAPIVersion)
	}
	if lock.Kind != lockKind {
		return e.New(e.CodeInvalidCatalog, "lock kind %q is unsupported; want exactly %q", lock.Kind, lockKind)
	}
	if lock.Pins == nil {
		return e.New(e.CodeInvalidCatalog, "lock is missing required pins map")
	}

	for _, name := range sortedPinNames(lock.Pins) {
		pin := lock.Pins[name]
		if pin.Tool != nil {
			if err := validateToolPin(*pin.Tool); err != nil {
				return err
			}
		}
		if strings.TrimSpace(name) == "" {
			return e.New(e.CodeInvalidCatalog, "lock has an empty source pin name")
		}
		if strings.TrimSpace(pin.Tag) == "" {
			return e.New(e.CodeUnpinnedSource, "lock source %q has no tag", name)
		}
		if !fullSHA.MatchString(pin.Commit) {
			return e.New(e.CodeUnpinnedSource, "lock source %q commit %q is not a full 40-hex SHA", name, pin.Commit)
		}
		if !fullSHA.MatchString(pin.TreeHash) {
			return e.New(e.CodeUnpinnedSource, "lock source %q treeHash %q is not a full 40-hex SHA", name, pin.TreeHash)
		}
		if pin.SourceDateEpoch <= 0 {
			return e.New(e.CodeUnpinnedSource, "lock source %q has no positive sourceDateEpoch", name)
		}
		if pin.ToolchainImage != "" && !digestPinned.MatchString(pin.ToolchainImage) {
			return e.New(e.CodeTagToolchain, "lock source %q toolchainImage %q is not digest-pinned", name, pin.ToolchainImage)
		}
	}

	artifactNames := make([]string, 0, len(lock.Artifacts))
	for name := range lock.Artifacts {
		artifactNames = append(artifactNames, name)
	}
	sort.Strings(artifactNames)
	for _, name := range artifactNames {
		pin := lock.Artifacts[name]
		if strings.TrimSpace(name) == "" {
			return e.New(e.CodeInvalidCatalog, "lock has an empty artifact pin name")
		}
		if strings.TrimSpace(pin.Tag) == "" {
			return e.New(e.CodeUnpinnedSource, "lock artifact %q has no tag", name)
		}
		if len(pin.Digests) == 0 {
			return e.New(e.CodeUnpinnedSource, "lock artifact %q has no architecture digests", name)
		}
		arches := make([]string, 0, len(pin.Digests))
		for arch := range pin.Digests {
			arches = append(arches, arch)
		}
		sort.Strings(arches)
		for _, arch := range arches {
			if !target.ValidArch(arch) {
				return e.New(e.CodeInvalidCatalog, "lock artifact %q has undeclared v1 architecture %q", name, arch)
			}
			if !canonical.IsDigest(pin.Digests[arch]) {
				return e.New(e.CodeUnpinnedSource,
					"lock artifact %q digest for arch %q (%q) is not sha256:<64-hex>", name, arch, pin.Digests[arch])
			}
		}
	}
	return nil
}

// validateLockAgainstRegistry enforces ADR-22's exact key-set contract. Update flows
// pass requireArtifacts=false so a first pin-artifacts run can fill missing entries;
// all existing entries are still shape-checked and orphan entries are always rejected.
func validateLockAgainstRegistry(reg *registryDoc, lock *PinLock, requireArtifacts bool) error {
	if err := validatePinLockShape(lock); err != nil {
		return err
	}
	for _, name := range sortedSourceNames(reg.Sources) {
		pin, ok := lock.Pins[name]
		if !ok {
			return e.New(e.CodeUnpinnedSource, "lock is incomplete: declared source %q has no pin", name)
		}
		src := reg.Sources[name]
		if src.Kind == SourceGitHubAction {
			if err := (ActionPin{Repo: strings.TrimPrefix(src.Repo, "https://github.com/"), Version: pin.Tag, SHA: pin.Commit}).Validate(); err != nil {
				return err
			}
		}
		if err := checkToolPin(src.Tool, pin); err != nil {
			return err
		}
		if src.ToolchainBaseImage != "" {
			if strings.TrimSpace(pin.GoVersion) == "" || pin.ToolchainImage == "" {
				return e.New(e.CodeUnpinnedSource,
					"lock source %q must pin goVersion and toolchainImage for toolchainBaseImage %q", name, src.ToolchainBaseImage)
			}
		} else if pin.ToolchainImage != "" {
			return e.New(e.CodeInvalidCatalog,
				"lock source %q carries toolchainImage but registry source declares no toolchainBaseImage", name)
		}
	}
	for _, name := range sortedPinNames(lock.Pins) {
		if _, ok := reg.Sources[name]; !ok {
			return e.New(e.CodeInvalidCatalog, "lock has orphan source pin %q", name)
		}
	}

	prebuilt := make(map[string]rawComponent)
	componentNames := make([]string, 0, len(reg.Components))
	for name := range reg.Components {
		componentNames = append(componentNames, name)
	}
	sort.Strings(componentNames)
	for _, name := range componentNames {
		raw := reg.Components[name]
		if raw.Prebuilt == nil {
			continue
		}
		prebuilt[name] = raw
		if len(raw.Prebuilt.Digests) != 0 {
			return e.New(e.CodeInvalidCatalog,
				"prebuilt component %q authors digests in registry.yaml; artifacts must be pinned only in %s", name, LockFile)
		}
		ap, ok := lock.Artifacts[name]
		if !ok {
			if requireArtifacts {
				return e.New(e.CodeUnpinnedSource, "lock is incomplete: prebuilt component %q has no artifact pin", name)
			}
			continue
		}
		if raw.Prebuilt.Chart != nil {
			var chartDigest string
			for _, arch := range target.Arches() {
				d := ap.Digests[arch]
				if d == "" {
					continue
				}
				if chartDigest != "" && d != chartDigest {
					return e.New(e.CodeInvalidCatalog, "chart %q must pin the same package for every architecture", name)
				}
				chartDigest = d
			}
			if !reflect.DeepEqual(raw.Prebuilt.Chart, ap.Chart) {
				return e.New(e.CodeInvalidCatalog, "chart %q acquisition changed; re-pin its owner", name)
			}
			if err := checkPrebuiltURLScheme(name, ap.URL); err != nil {
				return err
			}
		} else if ap.URL != "" || ap.Chart != nil {
			return e.New(e.CodeInvalidCatalog, "artifact %q has a chart URL without chart acquisition", name)
		}
		requiredArches := requiredArtifactArches(reg, name)
		for _, arch := range target.Arches() {
			_, required := requiredArches[arch]
			_, present := ap.Digests[arch]
			if requireArtifacts && required && !present {
				return e.New(e.CodeUnpinnedSource,
					"lock artifact %q is incomplete: required arch %q has no digest", name, arch)
			}
			if present && !required {
				return e.New(e.CodeInvalidCatalog,
					"lock artifact %q has digest for undeclared component arch %q", name, arch)
			}
		}
		// If acquisition is version-rendered from the source pin, its artifact pin must
		// track that exact tag. Fixed independent refs may use their own component tag.
		if raw.PinnedBy == "" && (usesSourceVersionTemplate(raw.Prebuilt) || raw.Prebuilt.Chart != nil) {
			pin := lock.Pins[raw.Source]
			if ap.Tag != pin.Tag {
				return e.New(e.CodeInvalidCatalog,
					"lock artifact %q is stale: tag %q does not match source pin tag %q", name, ap.Tag, pin.Tag)
			}
		}
	}
	artifactNames := make([]string, 0, len(lock.Artifacts))
	for name := range lock.Artifacts {
		artifactNames = append(artifactNames, name)
	}
	sort.Strings(artifactNames)
	for _, name := range artifactNames {
		if _, ok := prebuilt[name]; !ok {
			return e.New(e.CodeInvalidCatalog, "lock has orphan artifact pin %q", name)
		}
	}
	return validatePinProvenance(reg, lock)
}

// requiredArtifactArches derives lock completeness from explicit producer targets.
func requiredArtifactArches(reg *registryDoc, component string) map[string]bool {
	required := make(map[string]bool)
	for _, platform := range reg.Components[component].Platforms {
		required[target.Arch(platform)] = true
	}
	return required
}

func usesSourceVersionTemplate(p *Prebuilt) bool {
	if p == nil {
		return false
	}
	ref := p.URL + "\x00" + p.Image
	for _, token := range []string{"{{.Tag}}", "{{.Version}}", "{{.Commit}}", "{{.Major}}", "{{.Minor}}", "{{.Patch}}"} {
		if strings.Contains(ref, token) {
			return true
		}
	}
	return false
}

func sortedSourceNames(m map[string]Source) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedPinNames(m map[string]Pin) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func clonePinLock(lock *PinLock) *PinLock {
	out := &PinLock{APIVersion: lockAPIVersion, Kind: lockKind, Pins: make(map[string]Pin, len(lock.Pins))}
	for name, pin := range lock.Pins {
		if pin.Tool != nil {
			copy := *pin.Tool
			pin.Tool = &copy
		}
		out.Pins[name] = pin
	}
	if len(lock.Artifacts) > 0 {
		out.Artifacts = make(map[string]ArtifactPin, len(lock.Artifacts))
		for name, pin := range lock.Artifacts {
			if pin.Chart != nil {
				chart := *pin.Chart
				pin.Chart = &chart
			}
			digests := make(map[string]string, len(pin.Digests))
			for arch, digest := range pin.Digests {
				digests[arch] = digest
			}
			pin.Digests = digests
			out.Artifacts[name] = pin
		}
	}
	return out
}

func revisionString(r lockRevision) string {
	if !r.exists {
		return "absent"
	}
	return fmt.Sprintf("sha256:%x", r.digest)
}
