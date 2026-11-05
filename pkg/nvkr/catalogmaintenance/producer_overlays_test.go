// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func producerOverlayFixture(t *testing.T) (string, ProducerOverlayInputs) {
	t.Helper()
	t.Chdir(t.TempDir())
	for i, directory := range []string{"base", "nvidia", "upstream"} {
		directory = "inputs/catalogs/" + directory
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		writeJSON(t, filepath.Join(directory, catalog.RegistryFile), map[string]any{
			"apiVersion": "nvkr.dev/catalog/v3", "kind": "SourceBuildRegistry",
			"sources": map[string]any{"source": map[string]any{"repo": "https://source.example.invalid/" + directory, "toolchainImage": "busybox@sha256:" + strings.Repeat("a", 64)}},
			"components": map[string]any{"producer": map[string]any{
				"source": "source", "fromSource": true, "platforms": []string{"linux/amd64"},
				"buildCommand": []string{"sh", "-c", `printf '%s' "$NVKR_SOURCE_COMMIT" > bin/tool`}, "artifactPath": "bin/tool", "writeDir": "bin", "outputType": "host-package",
				"sourceMetadata": map[string]string{"NVKR_SOURCE_COMMIT": "{{.Commit}}"},
			}},
		})
		writeJSON(t, filepath.Join(directory, catalog.LockFile), catalog.PinLock{
			APIVersion: "nvkr.dev/lock/v1", Kind: "PinLock", Pins: map[string]catalog.Pin{
				"source": {Tag: "v1.0.0", Commit: strings.Repeat(string(rune('a'+i)), 40), TreeHash: strings.Repeat("e", 40), SourceDateEpoch: 1700000000},
			},
		})
	}
	input := ProducerOverlayInputs{APIVersion: producerOverlaySchema, Overlays: []ProducerOverlayInput{
		{Name: "nvidia", Destination: "inputs/catalogs/nvidia.json", Base: "inputs/catalogs/base", Producers: []ProducerCopy{{Catalog: "inputs/catalogs/nvidia", Components: []string{"producer"}}}},
		{Name: "upstream", Destination: "inputs/catalogs/upstream.json", Base: "inputs/catalogs/base", Predecessors: []string{"inputs/catalogs/nvidia.json"}, Producers: []ProducerCopy{{Catalog: "inputs/catalogs/upstream", Components: []string{"producer"}}}},
	}}
	base, err := catalog.LoadContext(t.Context(), catalog.Dir(input.Overlays[0].Base))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := base.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	component := base.Components["producer"]
	// Adoption requires existing, named overlay destinations. Render checks their
	// type/name before replacing bytes, so source registries cannot be overwritten.
	for _, entry := range input.Overlays {
		writeJSON(t, entry.Destination, catalog.Overlay{APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind, Name: entry.Name, RequiresCatalogDigest: digest, Components: map[string]catalog.ComponentReplacement{"producer": {Component: &component}}})
	}
	const filename = "overlays.json"
	writeJSON(t, filename, input)
	if err := GenerateProducerOverlays(t.Context(), filename, false); err != nil {
		t.Fatal(err)
	}
	return filename, input
}

func TestProducerOverlayPinPropagation(t *testing.T) {
	filename, input := producerOverlayFixture(t)
	before := map[string][]byte{}
	for _, entry := range input.Overlays {
		before[entry.Destination] = readFixture(t, entry.Destination)
	}
	upstreamBefore, err := catalog.LoadOverlayContext(t.Context(), input.Overlays[1].Destination)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := catalog.LoadLockContext(t.Context(), "inputs/catalogs/nvidia")
	if err != nil {
		t.Fatal(err)
	}
	pin := lock.Pins["source"]
	pin.Tag, pin.Commit, pin.TreeHash, pin.SourceDateEpoch = "v1.0.1", strings.Repeat("d", 40), strings.Repeat("f", 40), pin.SourceDateEpoch+1
	lock.Pins["source"] = pin
	if err := catalog.WriteLockContext(t.Context(), "inputs/catalogs/nvidia", lock); err != nil {
		t.Fatal(err)
	}
	assertCode(t, GenerateProducerOverlays(t.Context(), filename, true), e.CodePipelineDrift)
	for path, raw := range before {
		if !bytes.Equal(raw, readFixture(t, path)) {
			t.Fatal("check mode changed a producer overlay")
		}
	}
	if err := GenerateProducerOverlays(t.Context(), filename, false); err != nil {
		t.Fatal(err)
	}
	nvidia, err := catalog.LoadOverlayContext(t.Context(), input.Overlays[0].Destination)
	if err != nil {
		t.Fatal(err)
	}
	if nvidia.Components["producer"].Component.Commit != pin.Commit {
		t.Fatal("source pin did not propagate to producer copy")
	}
	upstream, err := catalog.LoadOverlayContext(t.Context(), input.Overlays[1].Destination)
	if err != nil {
		t.Fatal(err)
	}
	if upstream.RequiresCatalogDigest == upstreamBefore.RequiresCatalogDigest || !reflect.DeepEqual(upstream.Components, upstreamBefore.Components) {
		t.Fatal("upstream predecessor must advance while its selected producer remains unchanged")
	}
	for _, check := range []bool{true, false} {
		before := readFixture(t, input.Overlays[1].Destination)
		stat, err := os.Stat(input.Overlays[1].Destination)
		if err != nil {
			t.Fatal(err)
		}
		if err := GenerateProducerOverlays(t.Context(), filename, check); err != nil {
			t.Fatal(err)
		}
		after, err := os.Stat(input.Overlays[1].Destination)
		if err != nil || !stat.ModTime().Equal(after.ModTime()) || !bytes.Equal(before, readFixture(t, input.Overlays[1].Destination)) {
			t.Fatalf("unchanged regeneration/check wrote output: %v", err)
		}
	}
}

func TestProducerOverlayAdmission(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ProducerOverlayInputs)
	}{
		{"missing producer", func(in *ProducerOverlayInputs) { in.Overlays[0].Producers[0].Components = []string{"missing"} }},
		{"ambiguous producer", func(in *ProducerOverlayInputs) {
			in.Overlays[0].Producers = append(in.Overlays[0].Producers, in.Overlays[0].Producers[0])
		}},
		{"wrong order", func(in *ProducerOverlayInputs) { in.Overlays[0], in.Overlays[1] = in.Overlays[1], in.Overlays[0] }},
		{"duplicate destination", func(in *ProducerOverlayInputs) { in.Overlays[1].Destination = in.Overlays[0].Destination }},
		{"path escape", func(in *ProducerOverlayInputs) { in.Overlays[0].Destination = "../outside.json" }},
		{"overwrite source registry", func(in *ProducerOverlayInputs) { in.Overlays[0].Destination = "inputs/catalogs/base/registry.yaml" }},
		{"wrong schema", func(in *ProducerOverlayInputs) { in.APIVersion += "-future" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filename, input := producerOverlayFixture(t)
			before := readFixture(t, input.Overlays[0].Destination)
			tc.edit(&input)
			writeJSON(t, filename, input)
			assertCode(t, GenerateProducerOverlays(t.Context(), filename, false), e.CodeInvalidCatalog)
			if !bytes.Equal(before, readFixture(t, "inputs/catalogs/nvidia.json")) {
				t.Fatal("admission failure wrote a partial candidate")
			}
		})
	}
}

func TestProducerOverlayPartialPublicationRecovery(t *testing.T) {
	for _, tc := range []struct {
		name string
		code e.Code
	}{
		{"cancellation", e.CodeTimeout},
		{"rename error", e.CodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filename, _ := producerOverlayFixture(t)
			lock, err := catalog.LoadLockContext(t.Context(), "inputs/catalogs/nvidia")
			if err != nil {
				t.Fatal(err)
			}
			pin := lock.Pins["source"]
			pin.Commit = strings.Repeat("d", 40)
			lock.Pins["source"] = pin
			if err := catalog.WriteLockContext(t.Context(), "inputs/catalogs/nvidia", lock); err != nil {
				t.Fatal(err)
			}
			files, err := renderProducerOverlays(t.Context(), filename)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			renameErr := errors.New("synthetic rename failure")
			published := 0
			err = writeOverlayFilesWithRename(ctx, files, func(from, to string) error {
				if published == 1 {
					return renameErr
				}
				if err := os.Rename(from, to); err != nil {
					return err
				}
				published++
				if tc.code == e.CodeTimeout {
					cancel()
				}
				return nil
			})
			assertCode(t, err, tc.code)
			if tc.code == e.CodeInternal && !errors.Is(err, renameErr) {
				t.Fatalf("publication failure lost its cause: %v", err)
			}
			if !bytes.Equal(readFixture(t, files[0].path), files[0].candidate) || !bytes.Equal(readFixture(t, files[1].path), files[1].before) {
				t.Fatal("fixture did not interrupt the set after exactly one publication")
			}
			staged, err := filepath.Glob("inputs/catalogs/.producer-overlay-stage-*")
			if err != nil || len(staged) != 0 {
				t.Fatalf("interrupted regeneration left staged files: %v %v", staged, err)
			}
			assertCode(t, GenerateProducerOverlays(t.Context(), filename, true), e.CodePipelineDrift)
			if err := GenerateProducerOverlays(t.Context(), filename, false); err != nil {
				t.Fatal(err)
			}
			if err := GenerateProducerOverlays(t.Context(), filename, true); err != nil {
				t.Fatal(err)
			}
			for _, file := range files {
				if !bytes.Equal(readFixture(t, file.path), file.candidate) {
					t.Fatal("regeneration did not recover the complete candidate")
				}
			}
			staged, err = filepath.Glob("inputs/catalogs/.producer-overlay-stage-*")
			if err != nil || len(staged) != 0 {
				t.Fatalf("interrupted regeneration left staged files: %v %v", staged, err)
			}
		})
	}
}

func TestProducerOverlayStaleWriterAndCancellation(t *testing.T) {
	filename, input := producerOverlayFixture(t)
	files, err := renderProducerOverlays(t.Context(), filename)
	if err != nil {
		t.Fatal(err)
	}
	files[0].candidate = append(files[0].candidate, ' ')
	writeFixture(t, input.Overlays[0].Destination, append(files[0].before, '\n'))
	assertCode(t, writeOverlayFiles(t.Context(), files), e.CodeWriteConflict)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		code e.Code
	}{
		{"canceled", ctx, e.CodeTimeout},
		{"missing", nil, e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertCode(t, GenerateProducerOverlays(tc.ctx, filename, false), tc.code)
		})
	}
}

func TestProducerOverlayWriterGuard(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "contended", true: "symlink"}[symlink], func(t *testing.T) {
			filename, _ := producerOverlayFixture(t)
			files, err := renderProducerOverlays(t.Context(), filename)
			if err != nil {
				t.Fatal(err)
			}
			files[0].candidate = append(files[0].candidate, ' ')
			guard, err := os.OpenFile(producerOverlayGuard, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = guard.Close() })
			if symlink {
				err = os.Rename(producerOverlayGuard, "held-guard")
				if err == nil {
					err = os.Symlink("held-guard", producerOverlayGuard)
				}
			} else {
				err = syscall.Flock(int(guard.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			}
			if err != nil {
				t.Fatal(err)
			}
			assertCode(t, writeOverlayFiles(t.Context(), files), e.CodeWriteConflict)
			for _, file := range files {
				if !bytes.Equal(readFixture(t, file.path), file.before) {
					t.Fatal("rejected writer changed an overlay")
				}
			}
		})
	}
}

func TestRefreshProducerOverlayBaselineBeforePinMutation(t *testing.T) {
	filename, input := producerOverlayFixture(t)
	writeFixture(t, input.Overlays[0].Destination, append(readFixture(t, input.Overlays[0].Destination), '\n'))
	config := Config{APIVersion: SchemaVersion, Name: "test", Catalog: "inputs/catalogs/nvidia", Sources: []string{"source"}, Definition: "absent.yaml", Environment: "absent-env.yaml", Validation: "static", ProducerOverlays: filename}
	called := false
	_, err := refresh(t.Context(), config, "test", false, "", func(context.Context, catalog.SourcePinRequest) (catalog.SourcePinUpdate, error) {
		called = true
		return catalog.SourcePinUpdate{}, nil
	})
	assertCode(t, err, e.CodePipelineDrift)
	if called {
		t.Fatal("pin mutation ran before generated producer baseline admission")
	}
}

func TestRefreshProducerOverlayJourney(t *testing.T) {
	for _, failGeneration := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "failure after pin commit"}[failGeneration], func(t *testing.T) {
			config, promote := maintenanceFixture(t)
			if err := os.MkdirAll("inputs/catalogs", 0o700); err != nil {
				t.Fatal(err)
			}
			cat, err := catalog.LoadContext(t.Context(), catalog.Dir(config.Catalog))
			if err != nil {
				t.Fatal(err)
			}
			digest, err := cat.SnapshotDigest()
			if err != nil {
				t.Fatal(err)
			}
			component := cat.Components["tool"]
			const destination = "inputs/catalogs/copied.json"
			writeJSON(t, destination, catalog.Overlay{APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind, Name: "copied", RequiresCatalogDigest: digest, Components: map[string]catalog.ComponentReplacement{"tool": {Component: &component}}})
			config.ProducerOverlays = "overlays.json"
			input := ProducerOverlayInputs{APIVersion: producerOverlaySchema, Overlays: []ProducerOverlayInput{{Name: "copied", Destination: destination, Base: config.Catalog, Producers: []ProducerCopy{{Catalog: config.Catalog, Components: []string{"tool"}}}}}}
			writeJSON(t, config.ProducerOverlays, input)
			if err := GenerateProducerOverlays(t.Context(), config.ProducerOverlays, false); err != nil {
				t.Fatal(err)
			}
			definition := readFixture(t, config.Definition)
			definition = bytes.ReplaceAll(definition, []byte("catalog: {base: catalog}"), []byte("catalog: {base: catalog, overlays: ["+destination+"]}"))
			writeFixture(t, config.Definition, definition)
			if _, err := lifecycle.GenerateFiles(t.Context(), config.Definition, config.Environment, "test (deadbeef)", "github", ".", false); err != nil {
				t.Fatal(err)
			}
			before := readFixture(t, destination)
			beforePlan := readFixture(t, ".github/nvkr-run/plan.json")
			promote("1.1.0")
			preview, err := Refresh(t.Context(), config, "test (deadbeef)", true, "")
			assertCode(t, err, e.CodeDiff)
			if !preview.Changed || !bytes.Equal(before, readFixture(t, destination)) || !bytes.Equal(beforePlan, readFixture(t, ".github/nvkr-run/plan.json")) {
				t.Fatal("preview mutated generated files")
			}
			result, err := refresh(t.Context(), config, "test (deadbeef)", false, "", func(ctx context.Context, request catalog.SourcePinRequest) (catalog.SourcePinUpdate, error) {
				update, err := catalog.UpdateSourcePins(ctx, request)
				if err == nil && failGeneration {
					input.Overlays[0].Producers[0].Components = []string{"missing"}
					writeJSON(t, config.ProducerOverlays, input)
				}
				return update, err
			})
			if !result.Changed {
				t.Fatal("pin update was lost")
			}
			if failGeneration {
				assertCode(t, err, e.CodeInvalidCatalog)
				if !strings.Contains(err.Error(), "pins committed but producer overlay regeneration failed") || !bytes.Equal(beforePlan, readFixture(t, ".github/nvkr-run/plan.json")) {
					t.Fatal("failed regeneration lost recovery context or generated a candidate Plan")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(before, readFixture(t, destination)) || bytes.Equal(beforePlan, readFixture(t, ".github/nvkr-run/plan.json")) {
				t.Fatal("pin did not reach both producer overlay and dependent Run")
			}
			if _, err := lifecycle.GenerateFiles(t.Context(), config.Definition, config.Environment, "test (deadbeef)", "github", ".", true); err != nil {
				t.Fatal(err)
			}
		})
	}
}
