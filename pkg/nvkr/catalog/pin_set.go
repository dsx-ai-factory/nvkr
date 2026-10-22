// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"errors"
	"sort"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// PinSet selects exact versions from one immutable upstream declaration. Extraction
// never runs source code or resolves dependencies. Every selected list record must
// be explicitly mapped; new or missing records fail closed.
type PinSet struct {
	File      string                  `json:"file"`
	Format    string                  `json:"format,omitempty"`
	Members   map[string]PinSetMember `json:"members"`
	Accounted map[string]PinSetItem   `json:"accounted,omitempty"`
}

// PinSetMember names exactly one existing source or acquired producer. Path is a
// bounded field path with optional exact record selectors, not an expression.
type PinSetMember struct {
	Source   string     `json:"source,omitempty"`
	Artifact string     `json:"artifact,omitempty"`
	Path     string     `json:"path,omitempty"`
	Chart    *HelmChart `json:"chart,omitempty"`
}

func pinSetMemberNames(set *PinSet) []string {
	names := make([]string, 0, len(set.Members))
	for n := range set.Members {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// validatePinSetDeclaration checks the shape retained in both authored and
// resolved catalogs. It does not authenticate an upstream tree or its derived
// pins. Reciprocal ownership remains a registry/lock invariant: resolved
// components omit pinnedBy and may be replaced by explicit catalog overlays.
func validatePinSetDeclaration(name string, src Source) error {
	set := src.PinSet
	if set == nil {
		return nil
	}
	if src.Kind != "" || src.Tool != nil {
		return e.New(e.CodeInvalidCatalog, "pin set %q requires an ordinary source", name)
	}
	if err := validatePinSetFilePath(set.File); err != nil {
		return err
	}
	switch set.Format {
	case "", "values":
		if len(set.Accounted) != 0 {
			return e.New(e.CodeInvalidCatalog, "pin set %q: accounted items require nvke-deploy-v1", name)
		}
	case "nvke-deploy-v1":
		if err := validateNVKEPinSet(set); err != nil {
			return err
		}
	default:
		return e.New(e.CodeInvalidCatalog, "pin set %q: unsupported format %q", name, set.Format)
	}
	if len(set.Members) == 0 || len(set.Members) > maxPinSetMembers {
		return e.New(e.CodeInvalidCatalog, "pin set %q requires 1..%d explicit members", name, maxPinSetMembers)
	}
	for _, key := range pinSetMemberNames(set) {
		m := set.Members[key]
		if strings.TrimSpace(key) == "" || key != strings.TrimSpace(key) || (m.Source == "") == (m.Artifact == "") {
			return e.New(e.CodeInvalidCatalog, "pin set %q member %q requires exactly one source or artifact", name, key)
		}
		if set.Format != "nvke-deploy-v1" {
			if m.Chart != nil {
				return e.New(e.CodeInvalidCatalog, "pin set %q member %q: chart identity requires nvke-deploy-v1", name, key)
			}
			if err := validatePinSetValuePath(m.Path); err != nil {
				return err
			}
		}
	}
	return nil
}

func validatePinSets(reg *registryDoc) error {
	sourceOwners := map[string]string{}
	artifactOwners := map[string]string{}
	for _, name := range sortedSourceNames(reg.Sources) {
		src := reg.Sources[name]
		if err := validatePinSetDeclaration(name, src); err != nil {
			return err
		}
		set := src.PinSet
		if set == nil {
			continue
		}
		for _, key := range pinSetMemberNames(set) {
			m := set.Members[key]
			if m.Source != "" {
				member, ok := reg.Sources[m.Source]
				if !ok || m.Source == name || member.PinnedBy != name {
					return e.New(e.CodeInvalidCatalog, "pin set %q source %q must exist and declare pinnedBy: %s", name, m.Source, name)
				}
				if owner, ok := sourceOwners[m.Source]; ok {
					return e.New(e.CodeInvalidCatalog, "source %q is mapped more than once (%s and %s)", m.Source, owner, name)
				}
				sourceOwners[m.Source] = name
			} else {
				member, ok := reg.Components[m.Artifact]
				if !ok || member.Prebuilt == nil || member.FromSource || member.Source != "" || member.PinnedBy != name {
					return e.New(e.CodeInvalidCatalog, "pin set %q artifact %q must be an acquired producer with pinnedBy: %s and no source", name, m.Artifact, name)
				}
				if owner, ok := artifactOwners[m.Artifact]; ok {
					return e.New(e.CodeInvalidCatalog, "artifact %q is mapped more than once (%s and %s)", m.Artifact, owner, name)
				}
				artifactOwners[m.Artifact] = name
			}
		}
	}
	for _, name := range sortedSourceNames(reg.Sources) {
		src := reg.Sources[name]
		if src.PinnedBy != "" && sourceOwners[name] != src.PinnedBy {
			return e.New(e.CodeInvalidCatalog, "source %q pinnedBy %q has no matching member declaration", name, src.PinnedBy)
		}
		seen := map[string]bool{}
		for at := name; at != ""; at = reg.Sources[at].PinnedBy {
			if len(seen) >= 64 {
				return e.New(e.CodeInvalidCatalog, "pin set nesting exceeds 64 sources")
			}
			if seen[at] {
				return e.New(e.CodeInvalidCatalog, "pin set cycle through source %q", at)
			}
			seen[at] = true
		}
	}
	for _, name := range sortedRawNames(reg.Components) {
		raw := reg.Components[name]
		if raw.PinnedBy != "" && artifactOwners[name] != raw.PinnedBy {
			return e.New(e.CodeInvalidCatalog, "artifact %q pinnedBy %q has no matching member declaration", name, raw.PinnedBy)
		}
		if raw.Prebuilt != nil && raw.Prebuilt.OCIArchive != "" {
			return e.New(e.CodeInvalidCatalog, "component %q authors resolved ociArchive; declare chart instead", name)
		}
		if raw.Prebuilt != nil && raw.Prebuilt.Executable != nil {
			return e.New(e.CodeInvalidCatalog, "component %q authors a resolved executable pin; declare releaseExecutable instead", name)
		}
		if raw.Prebuilt != nil && raw.Prebuilt.ReleaseExecutable {
			source, ok := reg.Sources[raw.Source]
			if !ok || source.Tool == nil || raw.PinnedBy != "" || raw.FromSource ||
				raw.Prebuilt.URL != "" || raw.Prebuilt.Image != "" || raw.Prebuilt.Chart != nil ||
				len(raw.Prebuilt.Digests) != 0 || len(raw.Platforms) != 1 || raw.Platforms[0] != source.Tool.Platform ||
				raw.OutputType != "host-package" {
				return e.New(e.CodeInvalidCatalog, "release executable %q requires one matching tool-source platform, host-package output and no competing acquisition", name)
			}
		}
		if raw.Prebuilt != nil && raw.Prebuilt.Chart != nil {
			if raw.Prebuilt.URL != "" || raw.Prebuilt.Image != "" || raw.OutputType != "raw" {
				return e.New(e.CodeInvalidCatalog, "chart %q requires outputType raw and no url/image", name)
			}
			if err := validateHelmChart(*raw.Prebuilt.Chart); err != nil {
				return err
			}
		}
	}
	return nil
}

func sortedRawNames(components map[string]rawComponent) []string {
	names := make([]string, 0, len(components))
	for n := range components {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func derivePinSet(ctx context.Context, reg *registryDoc, name string, pin Pin) (map[string]string, error) {
	src := reg.Sources[name]
	tree, err := openPinnedTree(ctx, src, pin)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tree.Close() }()
	return derivePinSetTree(ctx, tree, reg, name, pin)
}

func derivePinSetTree(ctx context.Context, tree pinnedTree, reg *registryDoc, name string, pin Pin) (map[string]string, error) {
	src := reg.Sources[name]
	if src.PinSet.Format == "nvke-deploy-v1" {
		versions, err := deriveNVKEPinSet(ctx, tree, reg, name)
		if err != nil {
			return nil, pinSetDerivationError(err, name, pin.Commit)
		}
		return versions, nil
	}
	raw, err := tree.ReadFile(ctx, src.PinSet.File)
	if err != nil {
		return nil, pinSetDerivationError(err, name, pin.Commit)
	}
	versions, err := extractPinSetVersions(raw, src.PinSet.Members)
	if err != nil {
		return nil, pinSetDerivationError(err, name, pin.Commit)
	}
	return versions, nil
}

// Only invalid declarations/content become mismatches. Operational failures
// retain their original classification, including internal read timeouts.
func pinSetDerivationError(err error, name, commit string) error {
	var coded *e.Error
	if errors.As(err, &coded) && coded.Code == e.CodeInvalidCatalog {
		return e.Wrap(e.CodePinSetMismatch, err, "pin set %q at %s", name, commit)
	}
	return err
}

// validateDerivedPins re-reads the exact pinned tree; provenance strings cannot
// authorize a stale member. No tag lookups or artifact resolution take place here.
func validateDerivedPins(ctx context.Context, reg *registryDoc, lock *PinLock) error {
	for _, name := range sortedSourceNames(reg.Sources) {
		src := reg.Sources[name]
		if src.PinSet == nil {
			continue
		}
		pin := lock.Pins[name]
		versions, err := derivePinSet(ctx, reg, name, pin)
		if err != nil {
			return err
		}
		provenance := name + "@" + pin.Commit
		for _, key := range pinSetMemberNames(src.PinSet) {
			m := src.PinSet.Members[key]
			v := versions[key]
			if m.Source != "" {
				got := lock.Pins[m.Source]
				want := reg.Sources[m.Source].QualifiedTag(v)
				if got.Tag != want || got.DerivedFrom != provenance {
					return e.New(e.CodePinSetMismatch, "source %q must follow %s: want tag %q and derivedFrom %q", m.Source, name, want, provenance)
				}
			} else {
				got, ok := lock.Artifacts[m.Artifact]
				if !ok || got.Tag != v || got.DerivedFrom != provenance {
					return e.New(e.CodePinSetMismatch, "artifact %q must follow %s: want version %q and derivedFrom %q", m.Artifact, name, v, provenance)
				}
			}
		}
	}
	return nil
}

func validatePinProvenance(reg *registryDoc, lock *PinLock) error {
	for _, name := range sortedPinNames(lock.Pins) {
		pin := lock.Pins[name]
		owner := reg.Sources[name].PinnedBy
		want := ""
		if owner != "" {
			want = owner + "@" + lock.Pins[owner].Commit
		}
		if pin.DerivedFrom != want {
			return e.New(e.CodePinSetMismatch, "source %q has derivedFrom %q; expected %q", name, pin.DerivedFrom, want)
		}
	}
	for _, name := range sortedRawNames(reg.Components) {
		pin, exists := lock.Artifacts[name]
		if !exists {
			continue
		}
		raw := reg.Components[name]
		owner := raw.PinnedBy
		if owner == "" && raw.Source != "" {
			owner = reg.Sources[raw.Source].PinnedBy
		}
		want := ""
		if owner != "" {
			want = owner + "@" + lock.Pins[owner].Commit
		}
		if pin.DerivedFrom != want {
			return e.New(e.CodePinSetMismatch, "artifact %q has derivedFrom %q; expected %q", name, pin.DerivedFrom, want)
		}
	}
	return nil
}

func memberVersionPin(name string, raw rawComponent, sources map[string]Source, lock *PinLock) (Source, Pin, error) {
	if raw.PinnedBy != "" {
		pin, ok := lock.Pins[raw.PinnedBy]
		if !ok {
			return Source{}, Pin{}, e.New(e.CodeUnpinnedSource, "missing umbrella %q", raw.PinnedBy)
		}
		artifact, ok := lock.Artifacts[name]
		if !ok {
			return Source{}, Pin{}, e.New(e.CodeUnpinnedSource, "artifact %q has no pin", name)
		}
		pin.Tag = artifact.Tag
		return sources[raw.PinnedBy], pin, nil
	}
	src, ok := sources[raw.Source]
	if !ok {
		return Source{}, Pin{}, e.New(e.CodeInvalidCatalog, "unknown source %q", raw.Source)
	}
	pin, ok := lock.Pins[raw.Source]
	if !ok {
		return Source{}, Pin{}, e.New(e.CodeUnpinnedSource, "source %q has no pin", raw.Source)
	}
	return src, pin, nil
}

func pinSetMemberError(name, owner string) error {
	return e.New(e.CodeUnsupported, "%s is pinnedBy %s; pin or bump umbrella %s instead", name, owner, owner)
}
