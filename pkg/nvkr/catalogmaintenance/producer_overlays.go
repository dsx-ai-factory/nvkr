// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/documentpath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"sigs.k8s.io/yaml"
)

const producerOverlaySchema = "nvkr.dev/producer-overlay-maintenance/v1"

// ProducerOverlayInputs explicitly owns generated producer copies. All paths are
// repository-relative. Entries are ordered; generated predecessors must precede
// their consumers. Frozen replacements remain authored complete producers.
type ProducerOverlayInputs struct {
	APIVersion string                 `json:"apiVersion"`
	Overlays   []ProducerOverlayInput `json:"overlays"`
}

type ProducerOverlayInput struct {
	Name         string                                  `json:"name"`
	Destination  string                                  `json:"destination"`
	Base         string                                  `json:"base"`
	Predecessors []string                                `json:"predecessors,omitempty"`
	Producers    []ProducerCopy                          `json:"producers,omitempty"`
	Frozen       map[string]catalog.ComponentReplacement `json:"frozen,omitempty"`
}

// ProducerCopy copies complete named producers, without renaming or deep merge.
type ProducerCopy struct {
	Catalog    string   `json:"catalog"`
	Components []string `json:"components"`
}

type overlayFile struct {
	path              string
	before, candidate []byte
}

// GenerateProducerOverlays regenerates an explicitly reviewed set. Check mode
// opens no writer guard and writes nothing. Refresh checks the old set before
// resolving pins, then regenerates before generating any dependent Run bundle.
// Files and the source lock are separate transactions: after interruption rerun
// this owner and every affected Run generator, then check the complete candidate.
func GenerateProducerOverlays(ctx context.Context, filename string, check bool) error {
	files, err := renderProducerOverlays(ctx, filename)
	if err != nil {
		return err
	}
	if check {
		return checkOverlayFiles(files)
	}
	if err := writeOverlayFiles(ctx, files); err != nil {
		return err
	}
	// Re-read the inputs through their owners: changed source/config bytes cannot
	// be reported as a successfully reproduced candidate.
	files, err = renderProducerOverlays(ctx, filename)
	if err != nil {
		return err
	}
	return checkOverlayFiles(files)
}

func checkOverlayFiles(files []overlayFile) error {
	for _, file := range files {
		if !bytes.Equal(file.before, file.candidate) {
			return e.New(e.CodePipelineDrift, "generated producer overlay %q differs; regenerate from its maintenance inputs", file.path)
		}
	}
	return nil
}

func loadProducerOverlayInputs(ctx context.Context, filename string) (ProducerOverlayInputs, error) {
	var input ProducerOverlayInputs
	if ctx == nil {
		return input, e.New(e.CodeUsage, "producer overlay maintenance requires a context")
	}
	if err := ctx.Err(); err != nil {
		return input, contextError(ctx, err)
	}
	if !safeRelative(filename) {
		return input, e.New(e.CodeUsage, "producer overlay inputs must be repository-relative")
	}
	raw, err := readOverlayInput(ctx, filename)
	if err != nil {
		return input, err
	}
	if _, err := documentpath.Decode(raw); err != nil {
		return input, e.Wrap(e.CodeInvalidCatalog, err, "decoding producer overlay inputs")
	}
	if err := yaml.UnmarshalStrict(raw, &input); err != nil {
		return input, e.Wrap(e.CodeInvalidCatalog, err, "decoding producer overlay inputs")
	}
	if input.APIVersion != producerOverlaySchema || len(input.Overlays) == 0 || len(input.Overlays) > 64 {
		return input, e.New(e.CodeInvalidCatalog, "producer overlay maintenance requires the supported schema and 1–64 entries")
	}
	destinations := map[string]int{}
	names := map[string]bool{}
	for i, entry := range input.Overlays {
		if !safeName.MatchString(entry.Name) || names[entry.Name] || !safeRelative(entry.Base) ||
			!safeRelative(entry.Destination) || !strings.HasPrefix(entry.Destination, "inputs/catalogs/") ||
			(path.Ext(entry.Destination) != ".json" && path.Ext(entry.Destination) != ".yaml") || entry.Destination == filename {
			return input, e.New(e.CodeInvalidCatalog, "invalid producer overlay entry %q", entry.Name)
		}
		if _, duplicate := destinations[entry.Destination]; duplicate {
			return input, e.New(e.CodeInvalidCatalog, "repeated producer overlay destination %q", entry.Destination)
		}
		destinations[entry.Destination], names[entry.Name] = i, true
	}
	for i, entry := range input.Overlays {
		if len(entry.Predecessors) > 64 || len(entry.Producers) > 64 || len(entry.Frozen) > 128 {
			return input, e.New(e.CodeInvalidCatalog, "producer overlay %q exceeds maintenance limits", entry.Name)
		}
		seen := map[string]bool{}
		for _, predecessor := range entry.Predecessors {
			j, generated := destinations[predecessor]
			if !safeRelative(predecessor) || seen[predecessor] || (generated && j >= i) {
				return input, e.New(e.CodeInvalidCatalog, "producer overlay %q has repeated or incorrectly ordered predecessor %q", entry.Name, predecessor)
			}
			seen[predecessor] = true
		}
		selected := map[string]bool{}
		for name := range entry.Frozen {
			selected[name] = true
		}
		for _, source := range entry.Producers {
			if !safeRelative(source.Catalog) || len(source.Components) == 0 || len(source.Components) > 128 {
				return input, e.New(e.CodeInvalidCatalog, "producer overlay %q requires an explicit bounded producer selection", entry.Name)
			}
			for _, name := range source.Components {
				if !safeName.MatchString(name) || selected[name] {
					return input, e.New(e.CodeInvalidCatalog, "producer overlay %q ambiguously selects producer %q", entry.Name, name)
				}
				selected[name] = true
			}
		}
		if len(selected) == 0 {
			return input, e.New(e.CodeInvalidCatalog, "producer overlay %q selects no producers", entry.Name)
		}
	}
	return input, nil
}

func renderProducerOverlays(ctx context.Context, filename string) ([]overlayFile, error) {
	input, err := loadProducerOverlayInputs(ctx, filename)
	if err != nil {
		return nil, err
	}
	generated := map[string]catalog.Overlay{}
	loaded := map[string]*catalog.Catalog{}
	load := func(directory string) (*catalog.Catalog, error) {
		if cat, ok := loaded[directory]; ok {
			return cat, nil
		}
		if err := admitPath(directory); err != nil {
			return nil, err
		}
		cat, err := catalog.LoadContext(ctx, catalog.Dir(directory))
		if err == nil {
			loaded[directory] = cat
		}
		return cat, err
	}
	files := make([]overlayFile, 0, len(input.Overlays))
	for _, entry := range input.Overlays {
		base, err := load(entry.Base)
		if err != nil {
			return nil, err
		}
		var predecessors []catalog.Overlay
		for _, filename := range entry.Predecessors {
			previous, ok := generated[filename]
			if !ok {
				if err := admitPath(filename); err != nil {
					return nil, err
				}
				previous, err = catalog.LoadOverlayContext(ctx, filename)
				if err != nil {
					return nil, err
				}
			}
			predecessors = append(predecessors, previous)
		}
		stack, err := catalog.ApplyStack(base, predecessors...)
		if err != nil {
			return nil, err
		}
		overlay := catalog.Overlay{APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind,
			Name: entry.Name, RequiresCatalogDigest: stack.Digest, Components: maps.Clone(entry.Frozen)}
		if overlay.Components == nil {
			overlay.Components = map[string]catalog.ComponentReplacement{}
		}
		for _, source := range entry.Producers {
			cat, err := load(source.Catalog)
			if err != nil {
				return nil, err
			}
			for _, name := range slices.Sorted(slices.Values(source.Components)) {
				component, found := cat.Components[name]
				if !found {
					return nil, e.New(e.CodeInvalidCatalog, "producer overlay %q selects missing producer %q from %s", entry.Name, name, source.Catalog)
				}
				overlay.Components[name] = catalog.ComponentReplacement{Component: &component}
			}
		}
		if _, err := catalog.ApplyStack(stack.Catalog, overlay); err != nil {
			return nil, err
		}
		candidate, err := json.MarshalIndent(overlay, "", "  ")
		if err != nil {
			return nil, e.Wrap(e.CodeInternal, err, "encoding generated producer overlay")
		}
		candidate = append(candidate, '\n')
		if int64(len(candidate)) > catalog.OverlayMaxBytes {
			return nil, e.New(e.CodeInvalidCatalog, "generated producer overlay %q exceeds byte limit", entry.Name)
		}
		before, err := readOverlayInput(ctx, entry.Destination)
		if err != nil {
			return nil, err
		}
		prior, err := catalog.DecodeOverlay(before)
		if err != nil {
			return nil, err
		}
		if prior.Name != entry.Name {
			return nil, e.New(e.CodeInvalidCatalog, "producer overlay destination %q belongs to %q, not %q", entry.Destination, prior.Name, entry.Name)
		}
		generated[entry.Destination] = overlay
		files = append(files, overlayFile{path: entry.Destination, before: before, candidate: candidate})
	}
	return files, nil
}

func readOverlayInput(ctx context.Context, filename string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, contextError(ctx, err)
	}
	if err := admitPath(filename); err != nil {
		return nil, err
	}
	held, err := heldfile.OpenContext(ctx, filename, heldfile.Options{Label: "producer-overlay-maintenance", MaxBytes: catalog.OverlayMaxBytes})
	if err != nil {
		return nil, contextError(ctx, e.Wrap(e.CodeInvalidCatalog, err, "reading producer overlay input %q", filename))
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, catalog.OverlayMaxBytes)
	if err != nil {
		return nil, contextError(ctx, e.Wrap(e.CodeInvalidCatalog, err, "reading producer overlay input %q", filename))
	}
	return raw, nil
}

func admitProducerOverlayTransport(ctx context.Context, filename, repository string) error {
	input, err := loadProducerOverlayInputs(ctx, filename)
	if err != nil {
		return err
	}
	directories := map[string]bool{}
	for _, entry := range input.Overlays {
		directories[entry.Base] = true
		for _, source := range entry.Producers {
			directories[source.Catalog] = true
		}
	}
	for _, directory := range slices.Sorted(maps.Keys(directories)) {
		if err := admitPath(directory); err != nil {
			return err
		}
		sources, err := catalog.ReadSourcesContext(ctx, catalog.Dir(directory))
		if err != nil {
			return err
		}
		if err := admitSourceCheckoutSources(repository, sources, nil); err != nil {
			return err
		}
	}
	return nil
}
