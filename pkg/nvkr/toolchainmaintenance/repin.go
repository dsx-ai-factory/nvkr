// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package toolchainmaintenance

import (
	"bytes"
	"context"
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

func repin(ctx context.Context, root string, record Record, stage bool) ([]string, error) {
	files, err := command(ctx, root, "git", "ls-files", "-z", "--", "inputs/catalogs", "inputs/environments", "inputs/experiments", "docs/examples")
	if err != nil {
		return nil, err
	}
	edits := map[string]edit{}
	var registries, overlays []string
	for _, name := range strings.Split(strings.TrimSuffix(string(files), "\x00"), "\x00") {
		if !(strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".json")) || strings.HasSuffix(name, "/pins.lock.yaml") {
			continue
		}
		raw, err := read(ctx, filepath.Join(root, name))
		if err != nil {
			return nil, err
		}
		var document map[string]any
		if err := yaml.UnmarshalStrict(raw, &document); err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "read authored input %s", name)
		}
		replacements := map[string]string{}
		if err := replacementsFor(document, "", record, replacements); err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "update %s", name)
		}
		changed := append([]byte(nil), raw...)
		var pairs []string
		for _, old := range slices.Sorted(maps.Keys(replacements)) {
			pairs = append(pairs, old, replacements[old])
		}
		if len(pairs) != 0 {
			// Preserve formatting while updating all exact occurrences within this
			// authored file, including comments and descriptive text. The resulting
			// review diff includes those edits; discovery alone selects the pins.
			changed = []byte(strings.NewReplacer(pairs...).Replace(string(raw)))
		}
		edits[name] = edit{raw, changed}
		if filepath.Base(name) == "registry.yaml" {
			registries = append(registries, name)
		}
		if document["kind"] == catalog.OverlayKind {
			overlays = append(overlays, name)
		}
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
		provider := editedRegistry{base: catalog.Dir(dir).(catalog.ContextDataProvider), raw: edits[name].after}
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
	base catalog.ContextDataProvider
	raw  []byte
}

func (p editedRegistry) Name() string                     { return "toolchain-maintenance:" + p.base.Name() }
func (p editedRegistry) ReadFile(string) ([]byte, error)  { return nil, fmt.Errorf("context required") }
func (p editedRegistry) ReadDir(string) ([]string, error) { return nil, fmt.Errorf("context required") }
func (p editedRegistry) ReadFileContext(ctx context.Context, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if name == catalog.RegistryFile {
		return append([]byte(nil), p.raw...), nil
	}
	return p.base.ReadFileContext(ctx, name)
}
func (p editedRegistry) ReadDirContext(ctx context.Context, name string) ([]string, error) {
	return p.base.ReadDirContext(ctx, name)
}
