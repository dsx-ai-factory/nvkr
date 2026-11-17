// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

const githubRuntimeInputDirectory = ".nvkr/runtime-inputs"

const githubRuntimeExportDirectory = ".nvkr/runtime-export"

// StageGitHubRuntimeInputs prepares the three-file same-Run carrier. Its Plan is
// packed canonical wire data; the manifest describes the reconstructed bundle.
func StageGitHubRuntimeInputs(ctx context.Context, plan *Plan, root string) error {
	bundle, err := checkedGitHubRuntimeInputs(ctx, plan, root)
	if err != nil {
		return err
	}
	_, manifest, err := ManifestFor(bundle)
	if err != nil {
		return err
	}
	packed, err := canonical.Marshal(plan)
	if err != nil {
		return err
	}
	if err := ensureGeneratedDirectory(root, ".nvkr"); err != nil {
		return err
	}
	directory := filepath.Join(root, githubRuntimeExportDirectory)
	if err := os.Mkdir(directory, 0o755); err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "runtime export requires a fresh staging directory")
	}
	files := map[string][]byte{"plan.json": packed, "manifest.json": manifest,
		"action-data.json": bundle.Files[githubActionDataPathFor(plan)]}
	for _, name := range sortedFileNames(files) {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "runtime export canceled")
		}
		out, err := os.OpenFile(filepath.Join(directory, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return e.Wrap(e.CodePipelineDrift, err, "creating runtime export")
		}
		_, writeErr := copyContext(ctx, out, bytes.NewReader(files[name]))
		closeErr := out.Close()
		if writeErr != nil || closeErr != nil {
			return e.New(e.CodePipelineDrift, "writing runtime export failed")
		}
	}
	return nil
}

// InstallGitHubRuntimeInputs admits the exact three-file, same-Run handoff in
// the fixed staging directory before changing the committed bundle. Transport
// authentication remains the caller's responsibility. The existing generator
// owner preserves its lock, per-file atomic replacement and manifest-last
// semantics; this is not an atomic replacement of the complete directory.
func InstallGitHubRuntimeInputs(ctx context.Context, baseline *Plan, root string) error {
	if err := admitGitHubRuntimeInputs(ctx, baseline, root); err != nil {
		return err
	}
	baselineBundle, err := checkCoreGitHubBundle(ctx, baseline, root)
	if err != nil {
		return err
	}
	staged, err := readGitHubRuntimeInputSet(ctx, root)
	if err != nil {
		return err
	}
	var fresh Plan
	if err := canonical.UnmarshalExact(staged["plan.json"], &fresh); err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "decoding staged runtime Plan")
	}
	if err := admitGitHubRuntimeInputs(ctx, &fresh, root); err != nil {
		return err
	}
	if fresh.Environment.GitHubRuntimeSource != nil && fresh.RuntimeSourceCommit == "" {
		return e.New(e.CodeUnpinnedSource, "staged runtime source inputs omit the frozen commit")
	}
	if len(fresh.Environment.GitHubRuntimeSources) != 0 && fresh.RuntimeInputs == nil {
		return e.New(e.CodeUnpinnedSource, "staged inputs omit the frozen source snapshot")
	}
	if fresh.EnvironmentDigest != baseline.EnvironmentDigest || fresh.DefinitionPath != baseline.DefinitionPath || fresh.EnvironmentPath != baseline.EnvironmentPath {
		return e.New(e.CodePipelineDrift, "staged runtime inputs change the selected namespace, Environment or authored paths")
	}
	if err := checkGitHubRuntimeAuthoredInputs(ctx, &fresh, root); err != nil {
		return err
	}
	bundle, err := RenderGitHub(ctx, &fresh)
	if err != nil {
		return err
	}
	_, manifest, err := ManifestFor(bundle)
	if err != nil {
		return err
	}
	packed, err := canonical.Marshal(&fresh)
	if err != nil {
		return err
	}
	expected := map[string][]byte{"plan.json": packed,
		"action-data.json": bundle.Files[githubActionDataPathFor(&fresh)], "manifest.json": manifest}
	for _, name := range []string{"plan.json", "action-data.json", "manifest.json"} {
		if !bytes.Equal(staged[name], expected[name]) {
			return e.New(e.CodePipelineDrift, "staged runtime input %s differs from the complete generated bundle", name)
		}
	}
	// Reuse the checked baseline and rendered candidate. The generated-file owner
	// still checks replacements and publishes the manifest last. No staged pathname
	// is moved over an existing member.
	if err := prepareGitHubRuntimeBundle(ctx, baseline, &fresh, baselineBundle, bundle, root, false); err != nil {
		return err
	}
	if err := checkGitHubRuntimeInputBindings(ctx, &fresh, root); err != nil {
		return err
	}
	return CheckGeneratedBundle(ctx, root, bundle)
}

func readGitHubRuntimeInputSet(ctx context.Context, root string) (map[string][]byte, error) {
	if err := validateGeneratedDirectoryChain(root, githubRuntimeInputDirectory); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "staged runtime input directory has unsafe parents")
	}
	name := filepath.Join(root, filepath.FromSlash(githubRuntimeInputDirectory))
	directory, before, err := securepath.OpenDirectory(name)
	if err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "holding staged runtime input directory")
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(4)
	if (err != nil && err != io.EOF) || len(entries) != 3 {
		return nil, e.New(e.CodePipelineDrift, "staged runtime inputs require exactly three regular files")
	}
	limits := map[string]int64{"plan.json": MaxPlanBytes, "action-data.json": maxGeneratedFileBytes, "manifest.json": maxGeneratedFileBytes}
	result := make(map[string][]byte, len(limits))
	for _, entry := range entries {
		limit, expected := limits[entry.Name()]
		if !expected || entry.Type() != 0 {
			return nil, e.New(e.CodePipelineDrift, "staged runtime inputs contain an unexpected member")
		}
		raw, err := readGitHubRuntimeInput(ctx, directory, entry.Name(), limit)
		if err != nil {
			return nil, classifyContext(ctx, err)
		}
		result[entry.Name()] = raw
	}
	after, statErr := directory.Stat()
	current, pathErr := os.Lstat(name)
	if statErr != nil || pathErr != nil || !securepath.SameMetadata(before, after) || !current.IsDir() || !os.SameFile(before, current) {
		return nil, e.New(e.CodePipelineDrift, "staged runtime input directory changed during admission")
	}
	if err := directory.Close(); err != nil {
		return nil, e.New(e.CodePipelineDrift, "closing staged runtime input directory failed")
	}
	return result, nil
}

func readGitHubRuntimeInput(ctx context.Context, directory *os.File, name string, limit int64) ([]byte, error) {
	source, before, err := securepath.OpenChild(directory, name, false)
	if err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "opening staged runtime input")
	}
	defer func() { _ = source.Close() }()
	if before.Size() < 0 || before.Size() > limit {
		return nil, e.New(e.CodePipelineDrift, "staged runtime input exceeds its size bound")
	}
	held, err := heldfile.FromReaderContext(ctx, source, heldfile.Options{Label: "staged runtime input", MaxBytes: limit})
	if err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "holding staged runtime input")
	}
	defer func() { _ = held.Close() }()
	after, statErr := source.Stat()
	closeErr := source.Close()
	if statErr != nil || closeErr != nil || !securepath.SameMetadata(before, after) {
		return nil, e.New(e.CodePipelineDrift, "staged runtime input changed during admission")
	}
	raw, readErr := held.ReadAllContext(ctx, limit)
	closeErr = held.Close()
	if readErr != nil || closeErr != nil {
		return nil, e.New(e.CodePipelineDrift, "reading staged runtime input failed")
	}
	return raw, nil
}
