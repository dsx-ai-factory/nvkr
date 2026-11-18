// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package inputinspect describes authored inputs without executing producers or
// authenticating release evidence. Reverse references are scoped to one root.
package inputinspect

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"sigs.k8s.io/yaml"
)

const maxEntries = 4096

// Reference is a descriptive local edge. Paths are relative to Report.Root.
type Reference struct {
	Path     string            `json:"path"`
	Relation string            `json:"relation"`
	Digest   string            `json:"digest,omitempty"`
	Mappings map[string]string `json:"mappings,omitempty"`
}

type Report struct {
	Root       string      `json:"root"`
	Path       string      `json:"path"`
	Kind       string      `json:"kind"`
	Summary    string      `json:"summary"`
	References []Reference `json:"references"`
	Consumers  []Reference `json:"consumers"`
	Scope      string      `json:"scope"`
}

type document struct {
	kind    string
	raw     []byte
	refs    []Reference
	summary string
	overlay *catalog.Overlay
}

type scan struct {
	ctx    context.Context
	root   string
	docs   map[string]*document
	paths  []string
	stacks map[string]catalog.StackDescription
}

// Explain validates the scanned authored graph before returning a description.
// References must stay within root; choosing a wider root is explicit. It never
// follows network references, derives upstream pin sets, or writes files.
func Explain(ctx context.Context, root, file string) (Report, error) {
	if ctx == nil || root == "" || file == "" {
		return Report{}, e.New(e.CodeUsage, "explain requires a context, --root and one file")
	}
	if err := ctx.Err(); err != nil {
		return Report{}, e.Wrap(e.CodeTimeout, err, "input explanation canceled")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return Report{}, e.Wrap(e.CodeUsage, err, "resolving input root")
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return Report{}, e.Wrap(e.CodeInvalidCatalog, err, "opening input root")
	}
	target, err := filepath.Abs(file)
	if err != nil {
		return Report{}, e.Wrap(e.CodeUsage, err, "resolving input file")
	}
	// Canonicalize the parent, but never follow a target symlink.
	parent, err := filepath.EvalSymlinks(filepath.Dir(target))
	if err != nil {
		return Report{}, e.Wrap(e.CodeInvalidCatalog, err, "opening input parent")
	}
	target, err = filepath.Rel(abs, filepath.Join(parent, filepath.Base(target)))
	if err != nil || !filepath.IsLocal(target) {
		return Report{}, e.New(e.CodeUsage, "input must be inside --root %q", abs)
	}
	target = filepath.ToSlash(target)
	s := &scan{ctx: ctx, root: abs, docs: map[string]*document{}, stacks: map[string]catalog.StackDescription{}}
	if err := s.walk(); err != nil {
		return Report{}, err
	}
	selected := s.docs[target]
	if selected == nil {
		return Report{}, e.New(e.CodeInvalidCatalog, "input %q has an unknown or unsupported authored schema", target)
	}
	for _, name := range s.paths {
		if err := s.admit(name); err != nil {
			return Report{}, fmt.Errorf("explaining %s: %w", name, err)
		}
	}
	if err := s.bindOverlays(); err != nil {
		return Report{}, err
	}
	result := Report{Root: filepath.ToSlash(abs), Path: target, Kind: selected.kind, Summary: selected.summary,
		References: selected.refs, Consumers: []Reference{}, Scope: "Reverse references cover supported authored kinds only within this root; maintenance and validator records are excluded. Recorded local declarations are not authenticated release evidence; upstream pin sets are not re-derived."}
	if result.References == nil {
		result.References = []Reference{}
	}
	for _, name := range s.paths {
		for _, ref := range s.docs[name].refs {
			if ref.Path == target {
				ref.Path = name
				result.Consumers = append(result.Consumers, ref)
			}
		}
	}
	return result, nil
}

func (s *scan) walk() error {
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "opening scan root")
	}
	defer func() { _ = root.Close() }()
	entries := 0
	var bytes int64
	var walk func(string, int) error
	walk = func(dir string, depth int) error {
		if depth > 16 {
			return e.New(e.CodeInvalidCatalog, "input scan exceeds depth 16")
		}
		f, err := root.Open(dir)
		if err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err, "opening input directory")
		}
		defer f.Close()
		for {
			if err := s.ctx.Err(); err != nil {
				return e.Wrap(e.CodeTimeout, err, "input scan canceled")
			}
			batch, err := f.ReadDir(128)
			if err != nil && err != io.EOF {
				return e.Wrap(e.CodeInvalidCatalog, err, "listing inputs")
			}
			for _, item := range batch {
				entries++
				if entries > maxEntries {
					return e.New(e.CodeInvalidCatalog, "input scan exceeds %d entries; choose a narrower --root", maxEntries)
				}
				name := path.Join(dir, item.Name())
				if item.Name() == ".git" || item.Name() == ".nvkr" || item.Name() == "node_modules" {
					continue
				}
				if item.Type()&os.ModeSymlink != 0 {
					return e.New(e.CodeInvalidCatalog, "input scan refuses symlink %q", name)
				}
				if item.IsDir() {
					if err := walk(name, depth+1); err != nil {
						return err
					}
					continue
				}
				if ext := path.Ext(name); ext != ".yaml" && ext != ".yml" && ext != ".json" {
					continue
				}
				raw, err := definition.RootReader(s.root)(s.ctx, name)
				if err != nil {
					return err
				}
				bytes += int64(len(raw))
				if bytes > 32<<20 {
					return e.New(e.CodeInvalidCatalog, "input scan exceeds 32 MiB")
				}
				var value any
				if err := yaml.Unmarshal(raw, &value); err != nil {
					return e.Wrap(e.CodeInvalidCatalog, err, "reading schema of %s", name)
				}
				object, ok := value.(map[string]any)
				if !ok {
					continue
				}
				header := struct{ APIVersion, Kind, SchemaVersion string }{}
				header.APIVersion, _ = object["apiVersion"].(string)
				header.Kind, _ = object["kind"].(string)
				header.SchemaVersion, _ = object["schemaVersion"].(string)
				kind := header.Kind
				if header.APIVersion == "nvkr.dev/lock/v1" {
					kind = "GeneratedLock"
				}
				if header.SchemaVersion == lifecycle.QualificationPolicySchema {
					kind = "QualificationPolicy"
				}
				if !strings.HasPrefix(header.APIVersion, "nvkr.dev/") && !strings.HasPrefix(header.SchemaVersion, "nvkr.dev/") {
					continue
				}
				// These separately owned maintenance/validation records are not
				// any of explain's supported authored kinds. They are not parsed
				// as Definitions, catalogs or verification policies.
				switch header.APIVersion {
				case "nvkr.dev/catalog-maintenance/v1", "nvkr.dev/producer-overlay-maintenance/v1", "nvkr.dev/toolchain-refresh/v1", "nvkr.dev/validator-catalog/v1", "nvkr.dev/validator/v1":
					continue
				}
				s.docs[name] = &document{kind: kind, raw: raw}
				s.paths = append(s.paths, name)
			}
			if err == io.EOF {
				break
			}
		}
		return nil
	}
	if err := walk(".", 0); err != nil {
		return err
	}
	slices.Sort(s.paths)
	return nil
}

func (s *scan) relative(from, ref string) (string, error) {
	if ref == "" || filepath.IsAbs(ref) {
		return "", e.New(e.CodeInvalidCatalog, "input %s has empty or absolute reference %q", from, ref)
	}
	resolved := path.Clean(path.Join(path.Dir(from), ref))
	if !filepath.IsLocal(resolved) {
		return "", e.New(e.CodeInvalidCatalog, "reference %q from %s leaves --root; select a root containing the complete input closure", ref, from)
	}
	return resolved, nil
}

func (s *scan) stack(base string, overlays []string) (catalog.StackDescription, error) {
	key := fmt.Sprintf("%q", append([]string{base}, overlays...))
	if found, ok := s.stacks[key]; ok {
		return found, nil
	}
	absolute := make([]string, len(overlays))
	for i, overlay := range overlays {
		absolute[i] = filepath.Join(s.root, overlay)
	}
	description, err := catalog.DescribeStackContext(s.ctx, filepath.Join(s.root, base), absolute)
	if err != nil {
		return catalog.StackDescription{}, err
	}
	s.stacks[key] = description
	return description, nil
}

func (s *scan) selection(name string, spec definition.Catalog, label string, components []string) error {
	base, err := s.relative(name, spec.Base)
	if err != nil {
		return err
	}
	registry := path.Join(base, catalog.RegistryFile)
	if s.docs[registry] == nil {
		return e.New(e.CodeInvalidCatalog, "catalog %s is missing from scanned root", registry)
	}
	overlays := make([]string, len(spec.Overlays))
	for i, ref := range spec.Overlays {
		overlays[i], err = s.relative(name, ref)
		if err != nil {
			return err
		}
		if s.docs[overlays[i]] == nil {
			return e.New(e.CodeInvalidCatalog, "overlay %s is missing from scanned root", overlays[i])
		}
	}
	stack, err := s.stack(base, overlays)
	if err != nil {
		return err
	}
	for _, component := range components {
		if !slices.Contains(stack.Components, component) {
			return e.New(e.CodeInvalidCatalog, "%s selects unknown component %q", label, component)
		}
	}
	doc := s.docs[name]
	doc.refs = append(doc.refs, Reference{Path: registry, Relation: label + " catalog"})
	previous := registry
	for i, overlay := range overlays {
		applied := stack.Overlays[i]
		doc.refs = append(doc.refs, Reference{Path: overlay, Relation: fmt.Sprintf("%s overlay %d", label, i+1), Digest: applied.OverlayDigest})
		edge := Reference{Path: previous, Relation: "predecessor", Digest: applied.InputDigest}
		if !slices.ContainsFunc(s.docs[overlay].refs, func(ref Reference) bool {
			return ref.Path == edge.Path && ref.Relation == edge.Relation && ref.Digest == edge.Digest
		}) {
			s.docs[overlay].refs = append(s.docs[overlay].refs, edge)
		}
		previous = overlay
	}
	return nil
}

func (s *scan) admit(name string) error {
	doc := s.docs[name]
	file := filepath.Join(s.root, name)
	switch doc.kind {
	case definition.DefinitionKind:
		def, err := definition.DecodeDefinition(doc.raw)
		if err != nil {
			return err
		}
		doc.summary = fmt.Sprintf("Definition %q selects %d named outputs.", def.Metadata.Name, len(def.Outputs))
		if err := s.selection(name, def.Catalog, "default", nil); err != nil {
			return err
		}
		for _, output := range def.Outputs {
			spec := def.Catalog
			if output.Catalog != nil {
				spec = *output.Catalog
			}
			if err := s.selection(name, spec, "output "+output.Name, output.Components); err != nil {
				return err
			}
		}
		for _, tool := range def.InternalTools {
			spec := def.Catalog
			if tool.Catalog != nil {
				spec = *tool.Catalog
			}
			if err := s.selection(name, spec, "internal tool "+tool.Name, tool.Components); err != nil {
				return err
			}
		}
	case definition.EnvironmentKind:
		env, err := definition.DecodeEnvironment(doc.raw)
		if err != nil {
			return err
		}
		doc.summary = fmt.Sprintf("Environment %q selects execution and transport policy; signing and publication are explicit.", env.Metadata.Name)
	case definition.CompositeKind:
		composition, err := definition.LoadComposite(s.ctx, s.root, name)
		if err != nil {
			return err
		}
		doc.summary = fmt.Sprintf("Composite Definition %q joins %d complete digest-pinned Definitions.", composition.Composite().Metadata.Name, len(composition.Members()))
		for _, member := range composition.Members() {
			doc.refs = append(doc.refs, Reference{Path: member.Path, Relation: "member " + member.Name, Digest: member.Digest})
		}
	case "SourceBuildRegistry":
		stack, err := s.stack(path.Dir(name), nil)
		if err != nil {
			return err
		}
		if path.Base(name) != catalog.RegistryFile {
			return e.New(e.CodeInvalidCatalog, "catalog entrypoint must be %s", catalog.RegistryFile)
		}
		doc.summary = fmt.Sprintf("Catalog %q declares %d producers with recorded local pins.", stack.Name, len(stack.Components))
		doc.refs = append(doc.refs, Reference{Path: path.Join(path.Dir(name), catalog.LockFile), Relation: "recorded pins"})
		for _, imp := range stack.Imports {
			ref, err := s.relative(name, imp.Path)
			if err != nil {
				return err
			}
			if s.docs[ref] == nil {
				return e.New(e.CodeInvalidCatalog, "fragment %s is outside the scanned authored inputs", ref)
			}
			for _, mapping := range []struct {
				label  string
				values map[string]string
			}{{"fragment sources", imp.Sources}, {"fragment components", imp.Components}, {"fragment source bindings", imp.SourceNames}} {
				if len(mapping.values) > 0 {
					doc.refs = append(doc.refs, Reference{Path: ref, Relation: mapping.label, Digest: "sha256:" + imp.SHA256, Mappings: mapping.values})
				}
			}
		}
	case catalog.OverlayKind:
		overlay, err := catalog.DecodeOverlay(doc.raw)
		if err != nil {
			return err
		}
		doc.overlay = &overlay
		doc.summary = fmt.Sprintf("Overlay %q requires predecessor catalog %s.", overlay.Name, overlay.RequiresCatalogDigest)
	case "CatalogFragment":
		fragment, err := catalog.DescribeFragmentContext(s.ctx, file)
		if err != nil {
			return err
		}
		doc.summary = fmt.Sprintf("Fragment declares %d sources and %d components; importing registries own local names and pins.", len(fragment.Sources), len(fragment.Components))
	case "GeneratedLock":
		if path.Base(name) != catalog.LockFile {
			return e.New(e.CodeInvalidCatalog, "generated lock must be named %s", catalog.LockFile)
		}
		if _, err := s.stack(path.Dir(name), nil); err != nil {
			return err
		}
		doc.summary = fmt.Sprintf("Machine-managed catalog pins. Generate with nvkr catalog pin --dir %q SOURCE or update with nvkr catalog bump --dir %q SOURCE.", path.Dir(name), path.Dir(name))
		doc.refs = append(doc.refs, Reference{Path: path.Join(path.Dir(name), catalog.RegistryFile), Relation: "owning catalog"})
	case "QualificationPolicy":
		if _, err := lifecycle.LoadQualificationPolicy(s.ctx, file); err != nil {
			return err
		}
		doc.summary = "Independent qualification verification policy; workflow and signer selections describe trust requirements, not verified evidence."
	default:
		return e.New(e.CodeInvalidCatalog, "unknown authored schema or kind %q in %s", doc.kind, name)
	}
	return nil
}

func (s *scan) bindOverlays() error {
	for _, name := range s.paths {
		doc := s.docs[name]
		if doc.overlay == nil || len(doc.refs) > 0 {
			continue
		}
		var matches []string
		for _, candidate := range s.paths {
			if s.docs[candidate].kind != "SourceBuildRegistry" {
				continue
			}
			stack, err := s.stack(path.Dir(candidate), nil)
			if err != nil {
				return err
			}
			if stack.Digest == doc.overlay.RequiresCatalogDigest {
				matches = append(matches, candidate)
			}
		}
		if len(matches) != 1 {
			return e.New(e.CodeUnverifiedRef, "overlay %s predecessor %s has %d matching catalogs within root %s (missing or ambiguous); select an explicit Definition overlay chain", name, doc.overlay.RequiresCatalogDigest, len(matches), s.root)
		}
		if _, err := s.stack(path.Dir(matches[0]), []string{name}); err != nil {
			return err
		}
		doc.refs = append(doc.refs, Reference{Path: matches[0], Relation: "predecessor", Digest: doc.overlay.RequiresCatalogDigest})
	}
	return nil
}
