// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"context"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const maxNestedSelections = 2000

// DownloadGitHubNested restores exactly a Plan-selected set of nested archives.
// It admits the entire fresh provider index, all exact request/member bindings
// and nonoverlapping absent destination roots before the first network request.
// Both merged locator files and provider-created one-artifact directories are
// accepted; missing, extra, duplicate or nested index entries fail closed.
func DownloadGitHubNested(ctx context.Context, request Request, index, destination string, selections map[string]NestedArchive, options Options) error {
	return newBackend().downloadGitHubNested(ctx, request, index, destination, selections, options)
}

func (b *backend) downloadGitHubNested(ctx context.Context, request Request, index, destination string, selections map[string]NestedArchive, options Options) error {
	if ctx == nil || index == "" || destination == "" {
		return e.New(e.CodeUsage, "nested CI download requires context, index and destination")
	}
	admission := request
	if admission.Name == "" {
		admission.Name = "private-ci-index"
	}
	ctx, cancel, options, err := admit(ctx, admission, options)
	if err != nil {
		return err
	}
	defer cancel()
	clean, names, roots, err := admitNestedSelections(request, selections)
	if err != nil {
		return err
	}
	locators, err := readNestedIndex(ctx, request, index, clean)
	if err != nil {
		return err
	}
	if err := checkNestedDestinations(ctx, destination, roots); err != nil {
		return err
	}
	for _, name := range names {
		selected := request
		selected.Name = name
		if err := b.downloadNested(ctx, selected, locators[name], destination, clean[name], options); err != nil {
			return err
		}
	}
	return nil
}

func admitNestedSelections(request Request, selections map[string]NestedArchive) (map[string]NestedArchive, []string, []string, error) {
	if len(selections) == 0 || len(selections) > maxNestedSelections {
		return nil, nil, nil, e.New(e.CodeUsage, "nested CI download requires a bounded nonempty selection")
	}
	clean := make(map[string]NestedArchive, len(selections))
	names := make([]string, 0, len(selections))
	var roots []string
	members := make(map[string]bool, len(selections))
	for name, selection := range selections {
		selected := request
		selected.Name = name
		if err := selected.validate(); err != nil {
			return nil, nil, nil, err
		}
		if request.Name != "" && request.Name != name {
			return nil, nil, nil, e.New(e.CodeVerifyFailed, "nested CI selection differs from the requested artifact")
		}
		value, err := admitNestedArchive(selection)
		if err != nil {
			return nil, nil, nil, err
		}
		if members[value.Member] || len(roots)+len(value.Allowed) > 4096 {
			return nil, nil, nil, e.New(e.CodeUsage, "nested CI selection repeats members or exceeds its root bound")
		}
		members[value.Member] = true
		clean[name] = value
		names = append(names, name)
		roots = append(roots, value.Allowed...)
	}
	all, err := admitNestedArchive(NestedArchive{Member: "index", Allowed: roots})
	if err != nil {
		return nil, nil, nil, err
	}
	slices.Sort(names)
	return clean, names, all.Allowed, nil
}

func readNestedIndex(ctx context.Context, request Request, index string, selections map[string]NestedArchive) (map[string]Locator, error) {
	directory, before, err := securepath.OpenDirectory(index)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "holding nested CI locator index")
	}
	defer directory.Close()
	entries, err := directory.ReadDir(len(selections) + 1)
	if err != nil && err != io.EOF {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "reading nested CI locator index")
	}
	if len(entries) > len(selections) {
		return nil, e.New(e.CodeVerifyFailed, "nested CI index contains extra entries")
	}
	locators := make(map[string]Locator, len(selections))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "reading nested CI index canceled")
		}
		name := strings.TrimSuffix(entry.Name(), locatorSuffix)
		if entry.IsDir() {
			name = entry.Name()
		}
		selection, wanted := selections[name]
		if !wanted || (!entry.IsDir() && (entry.Type() != 0 || entry.Name() != name+locatorSuffix)) {
			return nil, e.New(e.CodeVerifyFailed, "nested CI index contains an unselected or non-locator entry")
		}
		if _, duplicate := locators[name]; duplicate {
			return nil, e.New(e.CodeVerifyFailed, "nested CI index repeats a selected artifact")
		}
		selected := request
		selected.Name = name
		var locator Locator
		if entry.IsDir() {
			locator, err = readNestedArtifactDirectory(ctx, directory, name, selected)
		} else {
			locator, err = readNestedLocator(ctx, directory, entry.Name(), selected)
		}
		if err != nil {
			return nil, err
		}
		if err := matchNestedLocator(locator, selection); err != nil {
			return nil, err
		}
		locators[name] = locator
	}
	after, err := directory.Stat()
	if err != nil || !securepath.SameMetadata(before, after) {
		return nil, e.New(e.CodeVerifyFailed, "nested CI locator index changed while reading")
	}
	if err := directory.Close(); err != nil {
		return nil, safeIOError(ctx, e.CodeVerifyFailed, "closing nested CI locator index")
	}
	if len(locators) != len(selections) {
		return nil, e.New(e.CodeReleaseIncomplete, "nested CI index is missing selected artifact locators")
	}
	return locators, nil
}

func readNestedArtifactDirectory(ctx context.Context, parent *os.File, name string, request Request) (Locator, error) {
	directory, before, err := securepath.OpenChild(parent, name, true)
	if err != nil {
		return Locator{}, e.Wrap(e.CodeVerifyFailed, err, "holding nested CI artifact directory")
	}
	defer directory.Close()
	entries, err := directory.ReadDir(2)
	if err != nil && err != io.EOF {
		return Locator{}, e.Wrap(e.CodeVerifyFailed, err, "reading nested CI artifact directory")
	}
	if len(entries) != 1 || entries[0].Type() != 0 || entries[0].Name() != name+locatorSuffix {
		return Locator{}, e.New(e.CodeVerifyFailed, "nested CI artifact directory is not one exact locator")
	}
	locator, err := readNestedLocator(ctx, directory, entries[0].Name(), request)
	if err != nil {
		return Locator{}, err
	}
	after, err := directory.Stat()
	if err != nil || !securepath.SameMetadata(before, after) {
		return Locator{}, e.New(e.CodeVerifyFailed, "nested CI artifact directory changed while reading")
	}
	if err := directory.Close(); err != nil {
		return Locator{}, safeIOError(ctx, e.CodeVerifyFailed, "closing nested CI artifact directory")
	}
	return locator, nil
}

func readNestedLocator(ctx context.Context, directory *os.File, name string, request Request) (Locator, error) {
	file, before, err := securepath.OpenChild(directory, name, false)
	if err != nil {
		return Locator{}, e.Wrap(e.CodeVerifyFailed, err, "holding nested CI locator")
	}
	defer file.Close()
	if before.Size() <= 0 || before.Size() > MaxLocatorBytes {
		return Locator{}, e.New(e.CodeVerifyFailed, "nested CI locator exceeds its metadata bound")
	}
	raw, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, r: file}, MaxLocatorBytes+1))
	if err != nil {
		return Locator{}, safeIOError(ctx, e.CodeVerifyFailed, "reading nested CI locator")
	}
	after, statErr := file.Stat()
	if statErr != nil || !securepath.SameMetadata(before, after) || int64(len(raw)) != before.Size() {
		return Locator{}, e.New(e.CodeVerifyFailed, "nested CI locator changed while reading")
	}
	if err := file.Close(); err != nil {
		return Locator{}, safeIOError(ctx, e.CodeVerifyFailed, "closing nested CI locator")
	}
	locator, err := DecodeLocator(raw)
	if err != nil {
		return Locator{}, err
	}
	if err := locator.Validate(request); err != nil {
		return Locator{}, err
	}
	return locator, nil
}
