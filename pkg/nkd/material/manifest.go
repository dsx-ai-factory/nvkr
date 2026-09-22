// Package material models the deterministic, execution-local input manifest for
// a component build. It does not acquire, verify, or stage artifacts. Callers must
// authenticate producer observations before constructing the manifest and must
// materialize its relative paths beneath a private staging root.
package material

import (
	"net/url"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/safetext"
)

const (
	ManifestSchemaVersion = "nkd.dev/build-material-manifest/v2"
	maxMaterialNameBytes  = 256
	maxMaterialKindBytes  = 128
	maxPathSegmentBytes   = 255
	materialPathPrefix    = "materials/"
)

// Observation is a description of one producer output selected by a catalog
// material binding. Digest identifies the observed bytes. This value is not an
// authenticated receipt and must not be accepted directly from an untrusted
// producer report.
type Observation struct {
	Binding   string `json:"binding"`
	Component string `json:"component"`
	Kind      string `json:"kind"`
	// Architecture repeats the selected target architecture so the source-owned
	// build can cross-check every material against its explicit architecture
	// variable before consuming the staged bytes.
	Architecture string `json:"architecture"`
	Digest       string `json:"digest"`
}

// Entry is one material made available beneath the build's private staging root.
// Path is a deterministic slash-relative path derived from Binding; an absolute or
// random execution root never enters the canonical manifest.
type Entry struct {
	Binding      string `json:"binding"`
	Component    string `json:"component"`
	Kind         string `json:"kind"`
	Architecture string `json:"architecture"`
	Digest       string `json:"digest"`
	Path         string `json:"path"`
}

// Manifest is the canonical material description consumed by a build driver.
// Its identity proves the description's exact bytes, not that the referenced
// artifacts were authenticated or staged correctly.
type Manifest struct {
	SchemaVersion string  `json:"schemaVersion"`
	Materials     []Entry `json:"materials"`
}

// NewManifest sorts observations by binding and derives stable relative paths.
// One component output may satisfy exactly one binding in a manifest.
func NewManifest(observations []Observation) (*Manifest, error) {
	ordered := append([]Observation(nil), observations...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Binding < ordered[j].Binding })
	manifest := &Manifest{SchemaVersion: ManifestSchemaVersion, Materials: make([]Entry, 0, len(ordered))}
	for _, observation := range ordered {
		path, err := StagingPath(observation.Binding)
		if err != nil {
			return nil, err
		}
		manifest.Materials = append(manifest.Materials, Entry{
			Binding:      observation.Binding,
			Component:    observation.Component,
			Kind:         observation.Kind,
			Architecture: observation.Architecture,
			Digest:       observation.Digest,
			Path:         path,
		})
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return manifest, nil
}

// Validate rejects incomplete, ambiguous, reordered, or path-substituted material
// descriptions before they can be serialized or acquire a typed identity.
func (manifest *Manifest) Validate() error {
	if manifest == nil {
		return e.New(e.CodeInvalidCatalog, "build material manifest is required")
	}
	if manifest.SchemaVersion != ManifestSchemaVersion {
		return e.New(e.CodeInvalidCatalog,
			"build material manifest schemaVersion %q is unsupported", manifest.SchemaVersion)
	}
	if manifest.Materials == nil {
		return e.New(e.CodeInvalidCatalog,
			"build material manifest materials must be an explicit array")
	}
	components := make(map[string]string, len(manifest.Materials))
	paths := make(map[string]string, len(manifest.Materials))
	previousBinding := ""
	manifestArchitecture := ""
	for index, entry := range manifest.Materials {
		if !canonicalText(entry.Component, maxMaterialNameBytes) ||
			!canonicalText(entry.Kind, maxMaterialKindBytes) {
			return e.New(e.CodeInvalidCatalog,
				"build material manifest entry %d has non-canonical metadata", index)
		}
		if index > 0 && entry.Binding <= previousBinding {
			return e.New(e.CodeInvalidCatalog,
				"build material manifest bindings must be strictly sorted and duplicate-free")
		}
		previousBinding = entry.Binding
		if entry.Architecture != "amd64" && entry.Architecture != "arm64" {
			return e.New(e.CodeInvalidCatalog,
				"build material %q has unsupported target architecture %q", entry.Binding, entry.Architecture)
		}
		if manifestArchitecture != "" && entry.Architecture != manifestArchitecture {
			return e.New(e.CodeInvalidCatalog,
				"build material manifest mixes target architectures %q and %q",
				manifestArchitecture, entry.Architecture)
		}
		manifestArchitecture = entry.Architecture
		if previous, duplicate := components[entry.Component]; duplicate {
			return e.New(e.CodeInvalidCatalog,
				"build material bindings %q and %q ambiguously select component %q",
				previous, entry.Binding, entry.Component)
		}
		components[entry.Component] = entry.Binding
		if !canonical.IsDigest(entry.Digest) {
			return e.New(e.CodeInvalidCatalog,
				"build material %q has no exact artifact digest", entry.Binding)
		}
		wantPath, err := StagingPath(entry.Binding)
		if err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err,
				"build material manifest entry %d has an invalid binding", index)
		}
		if entry.Path != wantPath {
			return e.New(e.CodeInvalidCatalog,
				"build material %q path %q is not its deterministic staging path %q",
				entry.Binding, entry.Path, wantPath)
		}
		// StagingPath is injective today. Retain this independent check so a future
		// path-contract change cannot silently alias two material bindings.
		if previous, duplicate := paths[entry.Path]; duplicate {
			return e.New(e.CodeInvalidCatalog,
				"build materials %q and %q share staging path %q", previous, entry.Binding, entry.Path)
		}
		paths[entry.Path] = entry.Binding
	}
	return nil
}

// Marshal returns the exact canonical bytes a driver writes and then binds by
// ordinary file SHA-256 before invoking a subprocess.
func (manifest *Manifest) Marshal() ([]byte, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return canonical.Marshal(manifest)
}

// Digest returns the domain-separated semantic identity of the manifest. It is
// distinct from the ordinary SHA-256 of the serialized file used for transport.
func (manifest *Manifest) Digest() (string, error) {
	if err := manifest.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindBuildMaterialManifest, manifest)
}

// StagingPath derives the canonical slash-relative path for one binding. A
// binding is a logical name, not a filesystem path. filepath.Clean and
// filepath.Join would interpret slashes and dot segments and would make the
// result host-dependent. PathEscape instead encodes the complete binding as one
// portable path segment. Callers must use percent escapes as literal filename
// characters and must not URL-decode the returned path before materialization.
//
// StagingPath validates the source name and encoded segment, but does not join
// the path to or grant access beneath any caller-provided staging root.
func StagingPath(binding string) (string, error) {
	if !canonicalText(binding, maxMaterialNameBytes) {
		return "", e.New(e.CodeInvalidCatalog,
			"build material binding %q is not bounded canonical text", binding)
	}
	escaped := url.PathEscape(binding)
	// PathEscape intentionally leaves RFC 3986 dot segments unchanged. Encode the
	// two special filesystem segments explicitly so every valid binding still names
	// one child beneath the material root.
	switch escaped {
	case ".":
		escaped = "%2E"
	case "..":
		escaped = "%2E%2E"
	}
	if len(escaped) > maxPathSegmentBytes {
		return "", e.New(e.CodeInvalidCatalog,
			"build material binding %q exceeds the encoded path-segment limit", binding)
	}
	return materialPathPrefix + escaped, nil
}

func canonicalText(value string, maxBytes int) bool {
	return safetext.Valid(value, maxBytes, false) && value == strings.TrimSpace(value)
}
