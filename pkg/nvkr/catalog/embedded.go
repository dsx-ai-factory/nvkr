// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/documentpath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/safetext"
	"go.yaml.in/yaml/v3"
)

// EmbeddedComponents declares the complete component map at Path in an upstream
// YAML/JSON manifest. The manifest is read only from this producer's pinned Git
// commit/tree. Members are metadata expectations, never observed disk contents.
// An explicitly empty map is allowed only when that exact upstream map is empty.
type EmbeddedComponents struct {
	Manifest string                     `json:"manifest"`
	Path     string                     `json:"path"`
	Members  map[string]EmbeddedVersion `json:"members"`
}

// EmbeddedVersion is the closed upstream member profile: a string version, or
// an object containing version and optionally both repo and commit. A known
// source identity must be an exact commit and must agree in both documents.
type EmbeddedVersion struct {
	Version string `json:"version"`
	Repo    string `json:"repo,omitempty"`
	Commit  string `json:"commit,omitempty"`
}

func CloneEmbeddedComponents(in *EmbeddedComponents) *EmbeddedComponents {
	if in == nil {
		return nil
	}
	out := *in
	if in.Members != nil {
		out.Members = make(map[string]EmbeddedVersion, len(in.Members))
		for name, version := range in.Members {
			out.Members[name] = version
		}
	}
	return &out
}

func validateEmbeddedDeclaration(declaration *EmbeddedComponents, fromSource bool) error {
	if declaration == nil {
		return nil
	}
	if !fromSource || declaration.Members == nil || len(declaration.Members) > documentpath.MaxMembers {
		return e.New(e.CodeInvalidCatalog, "embeds requires a source producer and an explicit bounded member map")
	}
	if err := validatePinSetFilePath(declaration.Manifest); err != nil {
		return err
	}
	if err := documentpath.Validate(declaration.Path); err != nil {
		return err
	}
	for _, name := range embeddedNames(declaration.Members) {
		if !safetext.Valid(name, 256, false) || strings.TrimSpace(name) != name || name == "" {
			return e.New(e.CodeInvalidCatalog, "embeds has an invalid component name")
		}
		version := declaration.Members[name]
		if !exactPinSetVersion(version.Version) || (version.Repo == "") != (version.Commit == "") {
			return e.New(e.CodeInvalidCatalog, "embedded component %q requires an exact version and paired source identity", name)
		}
		if version.Repo != "" {
			if !fullSHA.MatchString(version.Commit) {
				return e.New(e.CodeInvalidCatalog, "embedded component %q requires a full source commit", name)
			}
			if err := checkSourceRepoScheme(name, version.Repo); err != nil {
				return err
			}
		}
	}
	return nil
}

type EmbeddedComponentResult struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type EmbeddedDescription struct {
	CatalogDigest string                    `json:"catalogDigest"`
	Components    []EmbeddedComponentResult `json:"components"`
}

// EmbeddedReceipt authenticates only declaration consistency at the pinned
// source. It has no artifact, inspection, execution or signing authority.
type EmbeddedReceipt struct {
	issued      bool
	description EmbeddedDescription
}

func (receipt *EmbeddedReceipt) Description() (EmbeddedDescription, error) {
	if receipt == nil || !receipt.issued {
		return EmbeddedDescription{}, e.New(e.CodeGateUnmet, "embedded declaration receipt was not issued")
	}
	value := receipt.description
	value.Components = append([]EmbeddedComponentResult(nil), value.Components...)
	return value, nil
}

// CheckEmbeddedComponents rechecks every selected producer's applicable authored
// declarations. Wrapped image/disk producers require an explicit declaration,
// even for an empty upstream map. Other source producers can opt in. Acquired
// bytes and hermetic producers without a declaration are not applicable.
func CheckEmbeddedComponents(ctx context.Context, snapshot *Catalog, selected []string) (*EmbeddedReceipt, error) {
	return checkEmbeddedComponents(ctx, snapshot, selected, openPinnedTree)
}

type embeddedTreeOpener func(context.Context, Source, Pin) (pinnedTree, error)

func checkEmbeddedComponents(ctx context.Context, snapshot *Catalog, selected []string, open embeddedTreeOpener) (*EmbeddedReceipt, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "embedded checks require a context")
	}
	if err := pinContextError(ctx); err != nil {
		return nil, err
	}
	if len(selected) == 0 || len(selected) > documentpath.MaxMembers {
		return nil, e.New(e.CodeInvalidCatalog, "embedded checks require a bounded nonempty producer selection")
	}
	digest, err := snapshot.SnapshotDigest()
	if err != nil {
		return nil, err
	}
	names := append([]string(nil), selected...)
	sort.Strings(names)
	components := make([]Component, len(names))
	// Admit the whole request before any source acquisition.
	for i, name := range names {
		component, exists := snapshot.Components[name]
		if !exists || (i > 0 && names[i-1] == name) || (!component.FromSource && component.Prebuilt == nil) {
			return nil, e.New(e.CodeInvalidCatalog, "embedded checks have an unknown, duplicate or excluded producer %q", name)
		}
		if component.Embeds == nil && embeddedRequired(component) {
			return nil, e.New(e.CodePinSetMismatch, "wrapped producer %q is missing its embedded-component declaration", name)
		}
		components[i] = component
		components[i].Embeds = CloneEmbeddedComponents(component.Embeds)
	}
	result := EmbeddedDescription{CatalogDigest: digest, Components: make([]EmbeddedComponentResult, 0, len(names))}
	for i, name := range names {
		component := components[i]
		status := "not-applicable"
		if component.Embeds != nil {
			if err := checkEmbeddedProducer(ctx, component, open); err != nil {
				return nil, pinSetDerivationError(err, name, component.Commit)
			}
			status = "pass"
		}
		result.Components = append(result.Components, EmbeddedComponentResult{Name: name, Status: status})
	}
	if err := pinContextError(ctx); err != nil {
		return nil, err
	}
	return &EmbeddedReceipt{issued: true, description: result}, nil
}

func embeddedRequired(component Component) bool {
	return component.FromSource && (component.BuilderType == BuilderBuildKit || component.BuilderType == BuilderVMImage || component.BuildNetwork == "default")
}

func checkEmbeddedProducer(ctx context.Context, component Component, open embeddedTreeOpener) error {
	tree, err := open(ctx, Source{Repo: component.Repo}, Pin{Commit: component.Commit, TreeHash: component.TreeHash, SourceDateEpoch: component.SourceDate})
	if err != nil {
		return err
	}
	defer func() { _ = tree.Close() }()
	raw, err := tree.ReadFile(ctx, component.Embeds.Manifest)
	if err != nil {
		return err
	}
	return compareEmbeddedManifest(raw, component.Embeds)
}

func compareEmbeddedManifest(raw []byte, declaration *EmbeddedComponents) error {
	root, err := documentpath.Decode(raw)
	if err != nil {
		return err
	}
	selected, err := documentpath.Select(root, []string{declaration.Path}, false)
	if err != nil {
		return err
	}
	members := selected[declaration.Path]
	if members.Kind != yaml.MappingNode || len(members.Content)/2 != len(declaration.Members) {
		return e.New(e.CodePinSetMismatch, "upstream embedded component map differs from complete authored membership")
	}
	for i := 0; i < len(members.Content); i += 2 {
		name, node := members.Content[i].Value, members.Content[i+1]
		want, exists := declaration.Members[name]
		if !exists {
			return e.New(e.CodePinSetMismatch, "undeclared upstream embedded component %q", name)
		}
		var got EmbeddedVersion
		if node.Kind == yaml.ScalarNode && node.Tag == "!!str" {
			got.Version = node.Value
		} else if node.Kind == yaml.MappingNode {
			for j := 0; j < len(node.Content); j += 2 {
				key, value := node.Content[j].Value, node.Content[j+1]
				if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
					return e.New(e.CodePinSetMismatch, "embedded %q field %q must be a string", name, key)
				}
				switch key {
				case "version":
					got.Version = value.Value
				case "repo":
					got.Repo = value.Value
				case "commit":
					got.Commit = value.Value
				default:
					return e.New(e.CodePinSetMismatch, "embedded %q has an unknown field %q", name, key)
				}
			}
		} else {
			return e.New(e.CodePinSetMismatch, "embedded %q has an unsupported version record", name)
		}
		if got != want {
			return e.New(e.CodePinSetMismatch, "embedded component %q disagrees with its pinned upstream version/source", name)
		}
	}
	return nil
}

func embeddedNames(members map[string]EmbeddedVersion) []string {
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
