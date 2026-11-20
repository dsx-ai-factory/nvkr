// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Compare downloaded runtime diagnostics. These descriptions grant no authority.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"slices"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type record map[string]any
type difference [2]any
type entryDifference struct {
	Path   string                `json:"path"`
	Fields map[string]difference `json:"fields"`
}
type report struct {
	Equal               bool                  `json:"equal"`
	IdentityDifferences map[string]difference `json:"identityDifferences"`
	EntryOrderEqual     bool                  `json:"entryOrderEqual"`
	ChangedEntryCount   int                   `json:"changedEntryCount"`
	Entries             []entryDifference     `json:"entries"`
	ReportedEntryLimit  int                   `json:"reportedEntryLimit"`
}
type diagnostics struct {
	production record
	order      []string
	entries    map[string]record
}

func readJSON(ctx context.Context, path string, value any) error {
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "reading runtime diagnostics")
	}
	file, info, err := securepath.OpenRegular(path)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "opening runtime diagnostic %s", path)
	}
	defer file.Close()
	const limit = 65 << 20
	if info.Size() > limit {
		return e.New(e.CodeVerifyFailed, "runtime diagnostic is not a bounded regular file: %s", path)
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(raw) > limit {
		return e.New(e.CodeVerifyFailed, "cannot read bounded runtime diagnostic: %s", path)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(value); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "decoding runtime diagnostic %s", path)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return e.New(e.CodeVerifyFailed, "runtime diagnostic has trailing data: %s", path)
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "reading runtime diagnostics")
	}
	return nil
}

func readDiagnostics(ctx context.Context, directory string) (diagnostics, error) {
	var result struct {
		RuntimeUse struct {
			Production record `json:"production"`
		} `json:"runtimeUse"`
	}
	var inventory struct {
		LayerDigest string   `json:"layerDigest"`
		Entries     []record `json:"entries"`
	}
	if err := readJSON(ctx, filepath.Join(directory, "disk-result.json"), &result); err != nil {
		return diagnostics{}, err
	}
	if err := readJSON(ctx, filepath.Join(directory, "runtime-tar-entries.json"), &inventory); err != nil {
		return diagnostics{}, err
	}
	d := diagnostics{production: result.RuntimeUse.Production, entries: make(map[string]record)}
	for _, field := range identityFields {
		if d.production[field] == nil {
			return d, e.New(e.CodeVerifyFailed, "runtime production omits %s", field)
		}
	}
	if inventory.LayerDigest == "" || inventory.LayerDigest != d.production["layerDigest"] || len(inventory.Entries) == 0 {
		return d, e.New(e.CodeVerifyFailed, "runtime inventory is empty or differs from completed use")
	}
	for _, entry := range inventory.Entries {
		path, ok := entry["path"].(string)
		if !ok || path == "" || d.entries[path] != nil {
			return d, e.New(e.CodeVerifyFailed, "runtime inventory has missing or duplicate entry paths")
		}
		d.entries[path] = entry
		d.order = append(d.order, path)
	}
	return d, nil
}

var identityFields = []string{"buildDigest", "platform", "component", "digest", "layerDigest", "layoutDigest", "size"}

func compare(ctx context.Context, first, second string) (report, error) {
	a, err := readDiagnostics(ctx, first)
	if err != nil {
		return report{}, err
	}
	b, err := readDiagnostics(ctx, second)
	if err != nil {
		return report{}, err
	}
	r := report{IdentityDifferences: map[string]difference{}, Entries: []entryDifference{}, ReportedEntryLimit: 100, EntryOrderEqual: slices.Equal(a.order, b.order)}
	for _, field := range identityFields {
		if !reflect.DeepEqual(a.production[field], b.production[field]) {
			r.IdentityDifferences[field] = difference{a.production[field], b.production[field]}
		}
	}
	paths := append(slices.Clone(a.order), b.order...)
	slices.Sort(paths)
	for _, path := range slices.Compact(paths) {
		fields := map[string]difference{}
		for _, entry := range []record{a.entries[path], b.entries[path]} {
			for key := range entry {
				left, right := a.entries[path][key], b.entries[path][key]
				if !reflect.DeepEqual(left, right) {
					fields[key] = difference{left, right}
				}
			}
		}
		if len(fields) != 0 {
			r.ChangedEntryCount++
			if len(r.Entries) < r.ReportedEntryLimit {
				r.Entries = append(r.Entries, entryDifference{Path: path, Fields: fields})
			}
		}
	}
	r.Equal = len(r.IdentityDifferences) == 0 && r.ChangedEntryCount == 0 && r.EntryOrderEqual
	return r, nil
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, e.New(e.CodeUsage, "usage: compare-runtime-evidence FIRST SECOND"))
		return 2
	}
	r, err := compare(ctx, args[0], args[1])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(r); err != nil {
		fmt.Fprintln(stderr, e.Wrap(e.CodeInternal, err, "writing runtime comparison"))
		return 2
	}
	if !r.Equal {
		return 1
	}
	return 0
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
