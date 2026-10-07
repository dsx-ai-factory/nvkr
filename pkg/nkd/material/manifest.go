// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package material models the deterministic, execution-local input manifest for
// a component build, and diagnostic byte correspondence for held guest-package
// captures. It does not acquire or stage artifacts, authenticate acquisition
// origin, or grant live-Run authority. Callers of the build-manifest API must
// authenticate producer observations before constructing the manifest and must
// materialize its relative paths beneath a private staging root.
package material

import (
	"net/url"
	"sort"
	"strings"
	"unicode"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/safetext"
)

const (
	ManifestSchemaVersion  = "nkd.dev/build-material-manifest/v2"
	maxMaterialNameBytes   = 256
	maxMaterialKindBytes   = 128
	maxPathSegmentBytes    = 255
	materialPathPrefix     = "materials/"
	maxManifestSchemaBytes = 256
)

// Projection is the source-owned wire contract for a material manifest. Nil at
// the Packer boundary selects NKD's native v2 document. A non-nil projection may
// relabel only the envelope schema and the producer-visible kind for each binding;
// it cannot change the authenticated component, architecture, digest, or staged
// path carried by an observation.
type Projection struct {
	SchemaVersion string            `json:"schemaVersion"`
	Kinds         map[string]string `json:"kinds"`
}

// CloneProjection returns a deep copy suitable for crossing catalog and IR
// ownership boundaries.
func CloneProjection(projection *Projection) *Projection {
	if projection == nil {
		return nil
	}
	clone := &Projection{SchemaVersion: projection.SchemaVersion}
	if projection.Kinds != nil {
		clone.Kinds = make(map[string]string, len(projection.Kinds))
		for binding, kind := range projection.Kinds {
			clone.Kinds[binding] = kind
		}
	}
	return clone
}

// ValidateProjection requires an explicit producer-visible kind for every
// declared binding and rejects mappings for undeclared inputs. Binding order is
// irrelevant here; the resulting manifest is sorted independently.
func ValidateProjection(projection *Projection, bindings []string) error {
	if projection == nil {
		return nil
	}
	if !canonicalToken(projection.SchemaVersion, maxManifestSchemaBytes) {
		return e.New(e.CodeInvalidCatalog,
			"build material manifest projection has an invalid schemaVersion %q", projection.SchemaVersion)
	}
	if projection.Kinds == nil {
		return e.New(e.CodeInvalidCatalog,
			"build material manifest projection kinds must be an explicit map")
	}
	expected := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		if _, err := StagingPath(binding); err != nil {
			return err
		}
		if _, duplicate := expected[binding]; duplicate {
			return e.New(e.CodeInvalidCatalog,
				"build material manifest projection repeats binding %q", binding)
		}
		expected[binding] = struct{}{}
	}
	projectedBindings := make([]string, 0, len(projection.Kinds))
	for binding := range projection.Kinds {
		projectedBindings = append(projectedBindings, binding)
	}
	sort.Strings(projectedBindings)
	for _, binding := range projectedBindings {
		kind := projection.Kinds[binding]
		if _, err := StagingPath(binding); err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err,
				"build material manifest projection has an invalid binding")
		}
		if !canonicalToken(kind, maxMaterialKindBytes) {
			return e.New(e.CodeInvalidCatalog,
				"build material manifest projection binding %q has an invalid kind %q", binding, kind)
		}
		if _, ok := expected[binding]; !ok {
			return e.New(e.CodeInvalidCatalog,
				"build material manifest projection maps undeclared binding %q", binding)
		}
		delete(expected, binding)
	}
	if len(expected) != 0 {
		missing := make([]string, 0, len(expected))
		for binding := range expected {
			missing = append(missing, binding)
		}
		sort.Strings(missing)
		return e.New(e.CodeInvalidCatalog,
			"build material manifest projection omits binding %q", missing[0])
	}
	return nil
}

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
	// SchemaVersion is the native v2 value for NewManifest. MarshalProjected may
	// emit a source-owned value; projected documents are validated and serialized
	// by that operation, while Validate, Marshal and Digest admit native v2 only.
	SchemaVersion string  `json:"schemaVersion"`
	Materials     []Entry `json:"materials"`
}

// NewManifest sorts observations by binding and derives stable relative paths.
// One component output may satisfy exactly one binding in a manifest.
func NewManifest(observations []Observation) (*Manifest, error) {
	manifest, err := newManifest(observations, nil)
	if err != nil {
		return nil, err
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return manifest, nil
}

// MarshalProjected returns canonical bytes under a source-owned projection. A
// nil projection preserves the native v2 schema and observed producer kinds.
func MarshalProjected(observations []Observation, projection *Projection) ([]byte, error) {
	manifest, err := newManifest(observations, projection)
	if err != nil {
		return nil, err
	}
	if projection == nil {
		return manifest.Marshal()
	}
	if err := manifest.validate(projection.SchemaVersion); err != nil {
		return nil, err
	}
	return canonical.Marshal(manifest)
}

func newManifest(observations []Observation, projection *Projection) (*Manifest, error) {
	bindings := make([]string, 0, len(observations))
	for _, observation := range observations {
		bindings = append(bindings, observation.Binding)
	}
	if err := ValidateProjection(projection, bindings); err != nil {
		return nil, err
	}
	ordered := append([]Observation(nil), observations...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Binding < ordered[j].Binding })
	schemaVersion := ManifestSchemaVersion
	if projection != nil {
		schemaVersion = projection.SchemaVersion
	}
	manifest := &Manifest{SchemaVersion: schemaVersion, Materials: make([]Entry, 0, len(ordered))}
	for _, observation := range ordered {
		path, err := StagingPath(observation.Binding)
		if err != nil {
			return nil, err
		}
		kind := observation.Kind
		if projection != nil {
			kind = projection.Kinds[observation.Binding]
		}
		manifest.Materials = append(manifest.Materials, Entry{
			Binding:      observation.Binding,
			Component:    observation.Component,
			Kind:         kind,
			Architecture: observation.Architecture,
			Digest:       observation.Digest,
			Path:         path,
		})
	}
	return manifest, nil
}

// Validate rejects incomplete, ambiguous, reordered, or path-substituted material
// descriptions before they can be serialized or acquire a typed identity.
func (manifest *Manifest) Validate() error {
	return manifest.validate(ManifestSchemaVersion)
}

func (manifest *Manifest) validate(schemaVersion string) error {
	if manifest == nil {
		return e.New(e.CodeInvalidCatalog, "build material manifest is required")
	}
	if manifest.SchemaVersion != schemaVersion {
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
			!canonicalToken(entry.Kind, maxMaterialKindBytes) {
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

func canonicalToken(value string, maxBytes int) bool {
	return canonicalText(value, maxBytes) && strings.IndexFunc(value, unicode.IsSpace) == -1
}
