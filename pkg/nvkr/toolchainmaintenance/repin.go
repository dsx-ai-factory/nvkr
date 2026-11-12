// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package toolchainmaintenance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"sigs.k8s.io/yaml"
)

// Repin verifies registry bytes again with the independent reader, then stages
// authored edits for review. It does not edit generated files, source locks,
// approval records or historical evidence. The workflow must regenerate and
// check all bundles before publishing its review branch.
func Repin(ctx context.Context, root, run string) ([]string, error) {
	record, err := Verify(ctx, root, run)
	if err != nil {
		return nil, err
	}
	return repin(ctx, root, record, true)
}

type edit struct{ before, after []byte }
type catalogPair struct{ before, after *catalog.Catalog }

// This is the authored owner of the maintained generated producer overlays.
// Other policies, especially subject-bound exceptions, are never repin inputs.
const producerOverlayPolicy = "inputs/policies/ovn-producer-overlays.json"
const localToolchainBindings = "toolchains/nvkr-packer/local-bindings.json"

func repin(ctx context.Context, root string, record Record, stage bool) ([]string, error) {
	files, err := command(ctx, root, "git", "ls-files", "-z", "--", "inputs/catalogs", "inputs/environments", "inputs/experiments", "docs/examples", producerOverlayPolicy, localToolchainBindings)
	if err != nil {
		return nil, err
	}
	edits := map[string]edit{}
	localIDs := map[string]string{}
	fileReplacements := map[string]map[string]string{}
	var registries, overlays []string
	for _, name := range strings.Split(strings.TrimSuffix(string(files), "\x00"), "\x00") {
		if !(strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".json")) || strings.HasSuffix(name, "/pins.lock.yaml") {
			continue
		}
		raw, err := read(ctx, filepath.Join(root, name))
		if err != nil {
			return nil, err
		}
		var value any
		if err := yaml.UnmarshalStrict(raw, &value); err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "read authored input %s", name)
		}
		document, _ := value.(map[string]any)
		if name == localToolchainBindings {
			if _, ok := value.([]any); !ok {
				return nil, e.New(e.CodeInvalidCatalog, "local toolchain bindings must be an array")
			}
		} else if document == nil {
			return nil, e.New(e.CodeInvalidCatalog, "authored input %s must be an object", name)
		}
		if name == producerOverlayPolicy && document["apiVersion"] != "nvkr.dev/producer-overlay-maintenance/v1" {
			return nil, e.New(e.CodeInvalidCatalog, "unsupported producer overlay maintenance policy")
		}
		replacements := map[string]string{}
		if err := replacementsFor(value, "", record, replacements); err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "update %s", name)
		}
		fileReplacements[name] = replacements
		edits[name] = edit{raw, raw}
		if filepath.Base(name) == "registry.yaml" {
			registries = append(registries, name)
		}
		if document["kind"] == catalog.OverlayKind {
			overlays = append(overlays, name)
			overlay, err := catalog.DecodeOverlay(raw)
			if err != nil {
				return nil, err
			}
			for _, substitution := range overlay.ToolchainSubstitutions {
				if !strings.HasPrefix(substitution.From, "sha256:") || !strings.HasPrefix(substitution.To, Registry+"/packer-native@sha256:") {
					continue
				}
				image, err := selectedImage(record, substitution.Arch, "packer")
				if err != nil {
					return nil, err
				}
				if err := addReplacement(localIDs, substitution.From, image.ImageID); err != nil {
					return nil, err
				}
			}
		}
	}
	// Only explicit native substitutions bind bare local IDs to an architecture
	// and candidate image. Update their producers and frozen copies together;
	// the catalog owner authenticates every predecessor/substitution below before
	// any file is staged. Unbound local IDs and excluded policies stay untouched.
	for name, change := range edits {
		replacements := fileReplacements[name]
		for old, updated := range localIDs {
			if err := addReplacement(replacements, old, updated); err != nil {
				return nil, err
			}
		}
		var pairs []string
		for _, old := range slices.Sorted(maps.Keys(replacements)) {
			pairs = append(pairs, old, replacements[old])
		}
		// Replace once against the original bytes so new pins cannot be
		// reinterpreted as old local IDs by a subsequent replacement pass.
		change.after = []byte(strings.NewReplacer(pairs...).Replace(string(change.before)))
		edits[name] = change
	}
	// Authenticate each predecessor using the catalog owner, then derive its new
	// digest through the same owner. Never hash marshaled YAML or patch a digest
	// based on its spelling alone.
	states := map[string]catalogPair{}
	for _, name := range registries {
		dir := filepath.Join(root, filepath.Dir(name))
		// Tutorial templates with no lock are authored examples, not executable
		// catalogs. They may be ignored only when this refresh leaves them alone.
		if _, err := os.Lstat(filepath.Join(dir, catalog.LockFile)); os.IsNotExist(err) && bytes.Equal(edits[name].before, edits[name].after) {
			continue
		}
		old, err := catalog.LoadContext(ctx, catalog.Dir(dir))
		if err != nil {
			return nil, err
		}
		provider := editedRegistry{base: catalog.Dir(dir).(catalog.ContextDataProvider), registry: name, edits: edits}
		if err := provider.rebindFragments(); err != nil {
			return nil, err
		}
		updated, err := catalog.LoadContext(ctx, provider)
		if err != nil {
			return nil, err
		}
		digest, err := old.SnapshotDigest()
		if err != nil {
			return nil, err
		}
		states[digest] = catalogPair{old, updated}
	}
	pending := append([]string(nil), overlays...)
	for len(pending) > 0 {
		var rest []string
		for _, name := range pending {
			change := edits[name]
			old, err := catalog.DecodeOverlay(change.before)
			if err != nil {
				return nil, err
			}
			pair, ok := states[old.RequiresCatalogDigest]
			if !ok {
				rest = append(rest, name)
				continue
			}
			before, err := catalog.ApplyStack(pair.before, old)
			if err != nil {
				return nil, err
			}
			newDigest, err := pair.after.SnapshotDigest()
			if err != nil {
				return nil, err
			}
			change.after = bytes.ReplaceAll(change.after, []byte(old.RequiresCatalogDigest), []byte(newDigest))
			updated, err := catalog.DecodeOverlay(change.after)
			if err != nil {
				return nil, err
			}
			after, err := catalog.ApplyStack(pair.after, updated)
			if err != nil {
				return nil, err
			}
			states[before.Digest] = catalogPair{before.Catalog, after.Catalog}
			edits[name] = change
		}
		if len(rest) == len(pending) {
			for _, name := range rest {
				if !bytes.Equal(edits[name].before, edits[name].after) {
					return nil, e.New(e.CodeUnverifiedRef, "cannot authenticate affected overlay predecessor: %s", name)
				}
			}
			break // Unrelated example/diagnostic stacks do not consume these pins.
		}
		pending = rest
	}
	var changed []string
	for _, name := range slices.Sorted(maps.Keys(edits)) {
		change := edits[name]
		if !bytes.Equal(change.before, change.after) {
			changed = append(changed, name)
		}
	}
	if !stage {
		return changed, nil
	}
	// These are proposed authored inputs in a disposable workflow checkout, not
	// durable release authority. On any write/generator failure the workflow must
	// discard the checkout and leave the existing review branch and pins alone.
	for _, name := range changed {
		if err := ctx.Err(); err != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "stage toolchain pins")
		}
		raw, err := read(ctx, filepath.Join(root, name))
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(raw, edits[name].before) {
			return nil, e.New(e.CodeWriteConflict, "authored input changed during repin: %s", name)
		}
	}
	for _, name := range changed {
		if err := os.WriteFile(filepath.Join(root, name), edits[name].after, 0644); err != nil {
			return nil, e.Wrap(e.CodeWriteConflict, err, "stage authored pin %s", name)
		}
	}
	return changed, nil
}

// replacementsFor recognizes image architecture from the existing authored
// shapes. An unknown placement or hash collision fails rather than guessing.
func replacementsFor(value any, arch string, record Record, replacements map[string]string) error {
	switch value := value.(type) {
	case []any:
		for _, child := range value {
			if err := replacementsFor(child, arch, record, replacements); err != nil {
				return err
			}
		}
	case map[string]any:
		if declared, ok := value["arch"].(string); ok {
			arch = declared
		}
		if image, ok := value["image"].(string); ok && strings.HasPrefix(image, Registry+"/inspector-native@") {
			im, err := selectedImage(record, arch, "inspector")
			if err != nil {
				return err
			}
			for _, field := range inspectorFields() {
				if field == "helperDigest" {
					continue
				}
				old, ok := value[field].(string)
				if !ok {
					return e.New(e.CodeVerifyFailed, "inspector profile lacks %s", field)
				}
				if err := addReplacement(replacements, old, im.Executables[field]); err != nil {
					return err
				}
			}
		}
		for _, key := range slices.Sorted(maps.Keys(value)) {
			childArch := arch
			if key == "amd64" || key == "arm64" {
				childArch = key
			}
			if key == "linux/amd64" || key == "linux/arm64" {
				childArch = strings.TrimPrefix(key, "linux/")
			}
			if err := replacementsFor(value[key], childArch, record, replacements); err != nil {
				return err
			}
		}
	case string:
		if strings.HasPrefix(value, Registry+"/") {
			kind := ""
			for _, k := range []string{"packer", "inspector"} {
				if strings.HasPrefix(value, Registry+"/"+k+"-native@sha256:") {
					kind = k
				}
			}
			im, err := selectedImage(record, arch, kind)
			if err != nil {
				return err
			}
			return addReplacement(replacements, value, im.Reference())
		}
	}
	return nil
}

func selectedImage(record Record, arch, kind string) (Image, error) {
	for _, im := range record.Images {
		if im.Arch == arch && im.Kind == kind {
			return im, nil
		}
	}
	return Image{}, e.New(e.CodeInvalidCatalog, "ambiguous native image placement: %s/%s", arch, kind)
}

func addReplacement(replacements map[string]string, old, updated string) error {
	if old == "" || updated == "" {
		return e.New(e.CodeUnpinnedInput, "empty pin replacement")
	}
	if prior, ok := replacements[old]; ok && prior != updated {
		return e.New(e.CodeAmbiguous, "one old pin maps to multiple native subjects")
	}
	replacements[old] = updated
	return nil
}

type editedRegistry struct {
	base     catalog.ContextDataProvider
	registry string
	edits    map[string]edit
}

func (p editedRegistry) Name() string                     { return "toolchain-maintenance:" + p.base.Name() }
func (p editedRegistry) ReadFile(string) ([]byte, error)  { return nil, fmt.Errorf("context required") }
func (p editedRegistry) ReadDir(string) ([]string, error) { return nil, fmt.Errorf("context required") }
func (p editedRegistry) ReadFileContext(ctx context.Context, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if name == catalog.RegistryFile {
		return append([]byte(nil), p.edits[p.registry].after...), nil
	}
	return p.base.ReadFileContext(ctx, name)
}
func (p editedRegistry) ReadDirContext(ctx context.Context, name string) ([]string, error) {
	return p.base.ReadDirContext(ctx, name)
}

// fragmentEdit resolves only tracked authored inputs captured by this proposal.
// It never falls back to disk: changed declarations and bindings must be checked
// together before any proposed bytes are staged.
func (p editedRegistry) fragmentEdit(name string) (edit, error) {
	if err := catalog.ValidateFragmentPath(name); err != nil {
		return edit{}, err
	}
	key := filepath.ToSlash(filepath.Join(filepath.Dir(p.registry), filepath.FromSlash(name)))
	if key == ".." || strings.HasPrefix(key, "../") || filepath.IsAbs(key) {
		return edit{}, e.New(e.CodeInvalidCatalog, "fragment escapes refresh root: %s", name)
	}
	change, ok := p.edits[key]
	if !ok {
		return edit{}, e.New(e.CodeInvalidCatalog, "fragment is not a tracked refresh input: %s", key)
	}
	return change, nil
}

func (p editedRegistry) ReadFragmentContext(ctx context.Context, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	change, err := p.fragmentEdit(name)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), change.after...), nil
}

// The predecessor catalog has already been authenticated by the catalog owner.
// Hashes here bind exact fragment bytes, never structured catalog identity.
func (p editedRegistry) rebindFragments() error {
	change := p.edits[p.registry]
	var document struct {
		Imports []struct {
			Path   string
			SHA256 string
		}
	}
	if err := yaml.Unmarshal(change.before, &document); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "read fragment bindings")
	}
	replacements := map[string]string{}
	for _, imp := range document.Imports {
		fragment, err := p.fragmentEdit(imp.Path)
		if err != nil {
			return err
		}
		if fmt.Sprintf("%x", sha256.Sum256(fragment.before)) != imp.SHA256 {
			return e.New(e.CodeWriteConflict, "fragment changed during refresh: %s", imp.Path)
		}
		if err := addReplacement(replacements, imp.SHA256, fmt.Sprintf("%x", sha256.Sum256(fragment.after))); err != nil {
			return err
		}
	}
	var pairs []string
	for _, old := range slices.Sorted(maps.Keys(replacements)) {
		pairs = append(pairs, old, replacements[old])
	}
	if len(pairs) > 0 {
		change.after = []byte(strings.NewReplacer(pairs...).Replace(string(change.after)))
	}
	p.edits[p.registry] = change
	return nil
}
