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
	SchemaVersion string      `json:"schemaVersion"`
	Root          string      `json:"root"`
	Path          string      `json:"path"`
	Kind          string      `json:"kind"`
	Summary       string      `json:"summary"`
	References    []Reference `json:"references"`
	Consumers     []Reference `json:"consumers"`
	Scope         string      `json:"scope"`
}

type document struct {
	kind    string
	raw     []byte
	refs    []Reference
	summary string
	overlay *catalog.Overlay
}

type stackState struct {
	base     string
	overlays []string
	digest   string
}

type scan struct {
	states []stackState
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
	if err := s.walk(target); err != nil {
		return Report{}, err
	}
	selected := s.docs[target]
	if selected == nil {
		return Report{}, e.New(e.CodeInvalidCatalog, "input %q has a schema not supported by explain", target)
	}
	for _, name := range s.paths {
		if err := ctx.Err(); err != nil {
			return Report{}, e.Wrap(e.CodeTimeout, err, "input explanation canceled")
		}
		if err := s.admit(name); err != nil {
			return Report{}, fmt.Errorf("explaining %s: %w", name, err)
		}
	}
	if err := s.bindOverlays(); err != nil {
		return Report{}, err
	}
	result := Report{SchemaVersion: "nvkr.dev/input-explanation/v1", Root: filepath.ToSlash(abs), Path: target, Kind: selected.kind, Summary: selected.summary,
		References: selected.refs, Consumers: []Reference{}, Scope: "Reverse references cover supported authored kinds only within this root; other schemas and .git, .nvkr and node_modules directories are excluded. Recorded local declarations are not authenticated release evidence; upstream pin sets are not re-derived."}
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

func (s *scan) walk(target string) error {
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "opening scan root")
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(target)
	if os.IsNotExist(err) {
		return e.New(e.CodeInvalidCatalog, "input %q was not found within --root", target)
	}
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "inspecting target %q", target)
	}
	if !info.Mode().IsRegular() {
		return e.New(e.CodeInvalidCatalog, "input %q is not a regular file", target)
	}
	if ext := path.Ext(target); ext != ".yaml" && ext != ".yml" && ext != ".json" {
		return e.New(e.CodeInvalidCatalog, "input %q is not a YAML/JSON file", target)
	}
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
					return e.Wrap(e.CodeInvalidCatalog, err, "reading schema of %s (choose a narrower input root if this is not an authored document)", name)
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

				if header.SchemaVersion == lifecycle.QualificationPolicySchema {
					kind = "QualificationPolicy"
				}
				if header.SchemaVersion == lifecycle.LocalQualificationPolicySchema {
					kind = "LocalQualificationPolicy"
				}
				if !strings.HasPrefix(header.APIVersion, "nvkr.dev/") && !strings.HasPrefix(header.SchemaVersion, "nvkr.dev/") {
					continue
				}
				// Dispatch only supported kinds; unrelated generated or
				// maintenance records do not become authored graph inputs.
				switch kind {
				case definition.DefinitionKind, definition.EnvironmentKind, definition.CompositeKind,
					"SourceBuildRegistry", catalog.OverlayKind, "CatalogFragment", "PinLock",
					"QualificationPolicy", "LocalQualificationPolicy":
				default:
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
	registry := path.Join(base, catalog.RegistryFile)
	doc := s.docs[registry]
	if doc == nil {
		return catalog.StackDescription{}, e.New(e.CodeInvalidCatalog, "catalog %s is missing from scan root", registry)
	}
	var header struct {
		Imports []struct {
			Path string `json:"path"`
		} `json:"imports"`
	}
	if err := yaml.Unmarshal(doc.raw, &header); err != nil {
		return catalog.StackDescription{}, e.Wrap(e.CodeInvalidCatalog, err, "reading catalog import paths")
	}
	for _, imp := range header.Imports {
		ref, err := s.relative(registry, imp.Path)
		if err != nil {
			return catalog.StackDescription{}, err
		}
		if s.docs[ref] == nil {
			return catalog.StackDescription{}, e.New(e.CodeInvalidCatalog, "fragment %s is missing from scanned root", ref)
		}
	}
	description, err := catalog.DescribeStackContext(s.ctx, s.root, base, overlays)
	if err != nil {
		return catalog.StackDescription{}, err
	}
	// Retain intermediate states so an unselected overlay can bind to a
	// predecessor already present inside an explicitly selected longer chain.
	for i := 1; i < len(overlays); i++ {
		if _, err := s.stack(base, overlays[:i]); err != nil {
			return catalog.StackDescription{}, err
		}
	}
	s.stacks[key] = description
	s.states = append(s.states, stackState{base: base, overlays: slices.Clone(overlays), digest: description.Digest})
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
			if output.Derived != nil {
				continue // Source-less outputs describe siblings, not catalog producers.
			}
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
		if path.Base(name) != catalog.RegistryFile {
			return e.New(e.CodeInvalidCatalog, "catalog entrypoint must be %s", catalog.RegistryFile)
		}
		stack, err := s.stack(path.Dir(name), nil)
		if err != nil {
			return err
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
			}{{"fragment sources (local -> fragment)", imp.Sources}, {"fragment components (local -> fragment)", imp.Components}, {"fragment source bindings (fragment -> local)", imp.SourceNames}} {
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
		fragment, err := catalog.DescribeFragmentContext(s.ctx, s.root, name)
		if err != nil {
			return err
		}
		doc.summary = fmt.Sprintf("Fragment declares %d sources and %d components; importing registries own local names and pins.", len(fragment.Sources), len(fragment.Components))
	case "PinLock":
		if path.Base(name) != catalog.LockFile {
			return e.New(e.CodeInvalidCatalog, "generated lock must be named %s", catalog.LockFile)
		}
		if _, err := s.stack(path.Dir(name), nil); err != nil {
			return err
		}
		doc.summary = fmt.Sprintf("Machine-managed catalog pins. From the printed scan root, generate with nvkr catalog pin --dir %q SOURCE or update with nvkr catalog bump --dir %q SOURCE.", path.Dir(name), path.Dir(name))
		doc.refs = append(doc.refs, Reference{Path: path.Join(path.Dir(name), catalog.RegistryFile), Relation: "owning catalog"})
	case "LocalQualificationPolicy":
		if _, err := lifecycle.DecodeLocalQualificationPolicy(doc.raw); err != nil {
			return err
		}
		doc.summary = "Independent local qualification policy binds exact input identities and public verification keys; it does not authenticate a Run."
	case "QualificationPolicy":
		if _, err := lifecycle.DecodeQualificationPolicy(doc.raw); err != nil {
			return err
		}
		doc.summary = "Independent qualification verification policy; workflow and signer selections describe trust requirements, not verified evidence."
	default:
		return e.New(e.CodeInvalidCatalog, "unknown authored schema or kind %q in %s", doc.kind, name)
	}
	return nil
}

func (s *scan) predecessorStates(name, digest string) []stackState {
	var found []stackState
	seen := map[string]bool{}
	for _, state := range s.states {
		if state.digest != digest || slices.Contains(state.overlays, name) {
			continue
		}
		owner := path.Join(state.base, catalog.RegistryFile)
		if len(state.overlays) > 0 {
			owner = state.overlays[len(state.overlays)-1]
		}
		if !seen[owner] {
			seen[owner] = true
			found = append(found, state)
		}
	}
	return found
}

func (s *scan) bindOverlays() error {
	pending := []string{}
	inferred := []string{}
	for _, name := range s.paths {
		if s.docs[name].overlay != nil && len(s.docs[name].refs) == 0 {
			pending = append(pending, name)
		}
	}
	for len(pending) > 0 {
		next := []string{}
		for _, name := range pending {
			if err := s.ctx.Err(); err != nil {
				return e.Wrap(e.CodeTimeout, err, "overlay explanation canceled")
			}
			doc := s.docs[name]
			matches := s.predecessorStates(name, doc.overlay.RequiresCatalogDigest)
			if len(matches) == 0 {
				next = append(next, name)
				continue
			}
			if len(matches) > 1 {
				return e.New(e.CodeUnverifiedRef, "overlay %s predecessor %s is ambiguous: %d matching input locations within root %s; select an explicit Definition overlay chain", name, doc.overlay.RequiresCatalogDigest, len(matches), s.root)
			}
			state := matches[0]
			if _, err := s.stack(state.base, append(slices.Clone(state.overlays), name)); err != nil {
				return err
			}
			owner := path.Join(state.base, catalog.RegistryFile)
			if len(state.overlays) > 0 {
				owner = state.overlays[len(state.overlays)-1]
			}
			doc.refs = append(doc.refs, Reference{Path: owner, Relation: "predecessor", Digest: doc.overlay.RequiresCatalogDigest})
			inferred = append(inferred, name)
		}
		if len(next) == len(pending) {
			name := next[0]
			return e.New(e.CodeUnverifiedRef, "overlay %s predecessor %s is unmatched within root %s; no catalog or acyclic overlay chain supplies it", name, s.docs[name].overlay.RequiresCatalogDigest, s.root)
		}
		pending = next
	}
	// A later inferred intermediate state may reveal an additional candidate.
	for _, name := range inferred {
		if len(s.predecessorStates(name, s.docs[name].overlay.RequiresCatalogDigest)) > 1 {
			return e.New(e.CodeUnverifiedRef, "overlay %s predecessor is ambiguous within root %s; select an explicit Definition overlay chain", name, s.root)
		}
	}
	return nil
}
