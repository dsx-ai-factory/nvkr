// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"context"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"sigs.k8s.io/yaml"
)

const (
	CompositeAPIVersion = "nvkr.dev/composite-definition/v1"
	CompositeKind       = "CompositeDefinition"
	// MaxCompositeMembers bounds one composite before any member is read (ADR-26 §8).
	MaxCompositeMembers = 16
	// maxCompositeInputBytes is the compile-input capture aggregate shared by the
	// composite and its member documents.
	maxCompositeInputBytes = catalog.MaxCatalogTotalBytes
)

// CompositeDefinition joins complete member Definitions pinned by exact
// nvkr.definition digest. It carries no Definition field of its own (ADR-26 §1).
type CompositeDefinition struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   Metadata          `json:"metadata"`
	Members    []CompositeMember `json:"members"`
}

// CompositeMember locates one member document; only Digest is its identity.
type CompositeMember struct {
	Name       string `json:"name"`
	Definition string `json:"definition"`
	Digest     string `json:"digest"`
}

// MemberIdentity is the identity-bearing part of one member pin.
type MemberIdentity struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

type compositeIdentity struct {
	Metadata Metadata         `json:"metadata"`
	Members  []MemberIdentity `json:"members"`
}

// InputReader returns one bounded regular file named by a clean root-relative
// slash path, without following symlinks.
type InputReader func(ctx context.Context, name string) ([]byte, error)

// Validate checks the composite's own schema and member bounds. Member
// documents are admitted only by AdmitComposite.
func (composite CompositeDefinition) Validate() error {
	if composite.APIVersion != CompositeAPIVersion || composite.Kind != CompositeKind {
		return e.New(e.CodeInvalidCatalog, "composite definition has unsupported apiVersion/kind %q/%q", composite.APIVersion, composite.Kind)
	}
	if err := validateCompositeIdentity(composite.Metadata, composite.MemberIdentities()); err != nil {
		return err
	}
	for _, member := range composite.Members {
		if err := validateLocator(member.Definition); err != nil {
			return err
		}
	}
	return nil
}

func validateCompositeIdentity(metadata Metadata, members []MemberIdentity) error {
	if !namePattern.MatchString(metadata.Name) {
		return e.New(e.CodeInvalidCatalog, "composite definition requires a canonical name")
	}
	if len(members) == 0 {
		return e.New(e.CodeInvalidCatalog, "composite definition requires members")
	}
	if len(members) > MaxCompositeMembers {
		return e.New(e.CodeInvalidCatalog, "composite definition has %d members; at most %d are admitted", len(members), MaxCompositeMembers)
	}
	digests := make(map[string]bool, len(members))
	for i, member := range members {
		if !namePattern.MatchString(member.Name) {
			return e.New(e.CodeInvalidCatalog, "composite member name %q is not canonical", member.Name)
		}
		if i > 0 && member.Name == members[i-1].Name {
			return e.New(e.CodeInvalidCatalog, "composite member %q is repeated", member.Name)
		}
		if i > 0 && member.Name < members[i-1].Name {
			return e.New(e.CodeInvalidCatalog, "composite members must be sorted by name")
		}
		if !canonical.IsDigest(member.Digest) {
			return e.New(e.CodeInvalidCatalog, "composite member %q requires an exact sha256 digest", member.Name)
		}
		if digests[member.Digest] {
			return e.New(e.CodeInvalidCatalog, "composite member digest %s is repeated", member.Digest)
		}
		digests[member.Digest] = true
	}
	return nil
}

// Digest is the nvkr.composite-definition identity: metadata and the sorted
// member (name, digest) list. Locators are transport and are excluded.
func (composite CompositeDefinition) Digest() (string, error) {
	if err := composite.Validate(); err != nil {
		return "", err
	}
	return CompositeDigest(composite.Metadata, composite.MemberIdentities())
}

// MemberIdentities returns the sorted identity-bearing member pins.
func (composite CompositeDefinition) MemberIdentities() []MemberIdentity {
	members := make([]MemberIdentity, 0, len(composite.Members))
	for _, member := range composite.Members {
		members = append(members, MemberIdentity{Name: member.Name, Digest: member.Digest})
	}
	return members
}

// CompositeDigest recomputes a composite identity from recorded metadata and
// member pins, so records can be verified without member locators.
func CompositeDigest(metadata Metadata, members []MemberIdentity) (string, error) {
	if err := validateCompositeIdentity(metadata, members); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindCompositeDefinition, compositeIdentity{Metadata: metadata, Members: members})
}

// Clone returns an independent composite value.
func (composite CompositeDefinition) Clone() CompositeDefinition {
	composite.Members = append([]CompositeMember(nil), composite.Members...)
	return composite
}

// DecodeComposite strictly decodes and validates bounded composite bytes.
func DecodeComposite(raw []byte) (CompositeDefinition, error) {
	var composite CompositeDefinition
	if err := decodeStrict(raw, "composite definition", &composite); err != nil {
		return CompositeDefinition{}, err
	}
	if err := composite.Validate(); err != nil {
		return CompositeDefinition{}, err
	}
	return composite, nil
}

// DocumentKind reports the authored kind of bounded Definition-form bytes
// without admitting them. Unknown kinds are rejected.
func DocumentKind(raw []byte) (string, error) {
	if int64(len(raw)) > maxDocumentBytes {
		return "", e.New(e.CodeInvalidCatalog, "definition document exceeds its byte bound")
	}
	var header struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
	}
	if err := yaml.Unmarshal(raw, &header); err != nil {
		return "", e.Wrap(e.CodeInvalidCatalog, err, "parsing definition kind")
	}
	switch {
	case header.APIVersion == DefinitionAPIVersion && header.Kind == DefinitionKind:
		return DefinitionKind, nil
	case header.APIVersion == CompositeAPIVersion && header.Kind == CompositeKind:
		return CompositeKind, nil
	case header.Kind == CompositeKind || header.APIVersion == CompositeAPIVersion:
		return "", e.New(e.CodeInvalidCatalog, "composite definition has unsupported apiVersion/kind %q/%q", header.APIVersion, header.Kind)
	default:
		return "", e.New(e.CodeInvalidCatalog, "definition has unsupported apiVersion/kind %q/%q", header.APIVersion, header.Kind)
	}
}

// AdmittedMember is one member Definition admitted under its exact pin.
type AdmittedMember struct {
	Name       string
	Path       string
	Digest     string
	Definition Definition
}

// Composition is an admitted composite: every member was read through the
// bounded reader, is a complete plain Definition and equals its pinned digest.
// Its fields are unexported so callers cannot assemble one without admission.
type Composition struct {
	path      string
	composite CompositeDefinition
	digest    string
	members   []AdmittedMember
}

// Path is the root-relative composite locator. It is transport, not identity.
func (composition Composition) Path() string { return composition.path }

// Digest is the admitted nvkr.composite-definition identity.
func (composition Composition) Digest() string { return composition.digest }

// Composite returns an independent copy of the admitted composite document.
func (composition Composition) Composite() CompositeDefinition { return composition.composite.Clone() }

// Members returns independent copies of the admitted members in name order.
func (composition Composition) Members() []AdmittedMember {
	members := make([]AdmittedMember, 0, len(composition.members))
	for _, member := range composition.members {
		member.Definition = member.Definition.Clone()
		members = append(members, member)
	}
	return members
}

// LoadComposite admits the composite at a clean path beneath root. Every read
// stays beneath root and refuses symlinks and non-regular files.
func LoadComposite(ctx context.Context, root, compositePath string) (Composition, error) {
	if ctx == nil {
		return Composition{}, e.New(e.CodeUsage, "composite definition load requires a context")
	}
	if err := ctx.Err(); err != nil {
		return Composition{}, e.Wrap(e.CodeTimeout, err, "composite definition load canceled")
	}
	read := RootReader(root)
	raw, err := read(ctx, compositePath)
	if err != nil {
		return Composition{}, err
	}
	return AdmitComposite(ctx, compositePath, raw, read)
}

// RootReader returns the bounded, no-follow regular-file reader beneath root.
func RootReader(root string) InputReader {
	provider := catalog.Dir(root).(catalog.ContextDataProvider)
	return func(ctx context.Context, name string) ([]byte, error) {
		if err := validateRootPath(name); err != nil {
			return nil, err
		}
		raw, err := provider.ReadFileContext(ctx, name)
		if err != nil {
			if ctx.Err() != nil {
				return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "reading %s canceled", name)
			}
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "reading %s", name)
		}
		return raw, nil
	}
}

// AdmitComposite applies ADR-26 §2 in order: the composite schema and bounds,
// then each member's bounded read, plain-kind check, strict admission, name and
// exact digest. It performs no catalog, network, subprocess or output work.
func AdmitComposite(ctx context.Context, compositePath string, raw []byte, read InputReader) (Composition, error) {
	if ctx == nil {
		return Composition{}, e.New(e.CodeUsage, "composite definition admission requires a context")
	}
	if err := ctx.Err(); err != nil {
		return Composition{}, e.Wrap(e.CodeTimeout, err, "composite definition admission canceled")
	}
	if read == nil {
		return Composition{}, e.New(e.CodeUsage, "composite definition admission requires a reader")
	}
	if err := validateRootPath(compositePath); err != nil {
		return Composition{}, err
	}
	composite, err := DecodeComposite(raw)
	if err != nil {
		return Composition{}, err
	}
	digest, err := composite.Digest()
	if err != nil {
		return Composition{}, err
	}
	locators := make(map[string]string, len(composite.Members))
	paths := make([]string, 0, len(composite.Members))
	for _, member := range composite.Members {
		resolved, err := ResolveInputPath(compositePath, member.Definition, false)
		if err != nil {
			return Composition{}, err
		}
		if previous, repeated := locators[resolved]; repeated {
			return Composition{}, e.New(e.CodeInvalidCatalog, "composite members %q and %q resolve to the same file %q", previous, member.Name, resolved)
		}
		locators[resolved] = member.Name
		paths = append(paths, resolved)
	}
	total := int64(len(raw))
	composition := Composition{path: compositePath, composite: composite.Clone(), digest: digest}
	for i, member := range composite.Members {
		if err := ctx.Err(); err != nil {
			return Composition{}, e.Wrap(e.CodeTimeout, err, "composite definition admission canceled")
		}
		memberRaw, err := read(ctx, paths[i])
		if err != nil {
			if ctx.Err() != nil {
				return Composition{}, e.Wrap(e.CodeTimeout, ctx.Err(), "reading composite member %q canceled", member.Name)
			}
			return Composition{}, e.Wrap(e.CodeInvalidCatalog, err, "reading composite member %q", member.Name)
		}
		if int64(len(memberRaw)) > maxCompositeInputBytes-total {
			return Composition{}, e.New(e.CodeInvalidCatalog, "composite and member documents exceed the %d-byte input bound", maxCompositeInputBytes)
		}
		total += int64(len(memberRaw))
		kind, err := DocumentKind(memberRaw)
		if err != nil {
			return Composition{}, e.Wrap(e.CodeInvalidCatalog, err, "composite member %q", member.Name)
		}
		if kind != DefinitionKind {
			return Composition{}, e.New(e.CodeInvalidCatalog, "composite member %q is a nested composite; members must be plain Definitions", member.Name)
		}
		definition, err := DecodeDefinition(memberRaw)
		if err != nil {
			return Composition{}, e.Wrap(e.CodeInvalidCatalog, err, "admitting composite member %q", member.Name)
		}
		if definition.Metadata.Name != member.Name {
			return Composition{}, e.New(e.CodeInvalidCatalog, "composite member %q names Definition %q", member.Name, definition.Metadata.Name)
		}
		memberDigest, err := definition.Digest()
		if err != nil {
			return Composition{}, err
		}
		if memberDigest != member.Digest {
			return Composition{}, e.New(e.CodeUnverifiedRef, "composite member %q has digest %s, not its pinned %s; run definition pin", member.Name, memberDigest, member.Digest)
		}
		composition.members = append(composition.members, AdmittedMember{Name: member.Name, Path: paths[i], Digest: memberDigest, Definition: definition})
	}
	return composition, nil
}

// ResolveInputPath joins a relative authored locator to the directory of a
// root-relative file. The result must stay beneath the admitted input root.
func ResolveInputPath(from, value string, directory bool) (string, error) {
	if err := validateLocator(value); err != nil {
		return "", err
	}
	name := path.Join(path.Dir(from), value)
	if directory && name == "." {
		return name, nil
	}
	if err := validateRootPath(name); err != nil {
		return "", e.Wrap(e.CodeInvalidCatalog, err, "input reference %q leaves the admitted input root", value)
	}
	return name, nil
}

func validateLocator(value string) error {
	if value == "" || len(value) > 4096 || path.IsAbs(value) || filepath.IsAbs(value) || !utf8.ValidString(value) ||
		strings.ContainsAny(value, "\\:") || strings.ContainsFunc(value, unicode.IsControl) {
		return e.New(e.CodeInvalidCatalog, "input reference %q is not root-relative", value)
	}
	return nil
}

func validateRootPath(name string) error {
	if len(name) > 4096 || !utf8.ValidString(name) || name == "." || !fs.ValidPath(name) ||
		strings.ContainsAny(name, "\\:") || strings.ContainsFunc(name, unicode.IsControl) {
		return e.New(e.CodeInvalidCatalog, "unsafe root-relative input path %q", name)
	}
	return nil
}
