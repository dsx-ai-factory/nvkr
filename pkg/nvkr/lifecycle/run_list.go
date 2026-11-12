// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// MaxRunListEntries bounds how many entries one attempts root may hold for
// listing. A larger root is refused rather than listed in part.
const MaxRunListEntries = 1024

// Problems for attempts-root entries that are not attempt directories.
const (
	runEntrySymlink      = "entry-symlink"
	runEntryNotDirectory = "entry-not-directory"
	runEntryUnsafe       = "entry-unsafe"
)

// runProblemDescriptions explain each diagnostic. None of them says a Run was
// interrupted: stored metadata cannot tell whether an owner is still running.
var runProblemDescriptions = map[string]string{
	runRecordUnavailable: "no Run record; owner liveness and interruption are unknown",
	runRecordMarkerOnly:  "attempt marker without a Run record; owner liveness and interruption are unknown",
	runRecordUnsafe:      "Run record or marker is not a regular file without symlinks",
	runRecordInvalid:     "Run record is not admissible canonical metadata",
	runRecordMismatch:    "Run record names a different attempt than its directory",
	runEntrySymlink:      "entry is a symlink and was not followed",
	runEntryNotDirectory: "entry is not an attempt directory",
	runEntryUnsafe:       "entry could not be opened as a directory without symlinks",
}

// RunList is a detached, description-only inventory of one attempts root. It
// orders attempts by name and claims no chronology, liveness or payload state.
type RunList struct {
	SchemaVersion string              `json:"schemaVersion"`
	Authority     string              `json:"authority"`
	Runs          []ListedRun         `json:"runs"`
	Diagnostics   []RunListDiagnostic `json:"diagnostics"`
}

// ListedRun summarizes one admissible Run record. Record is the locator that
// run inspect --record accepts.
type ListedRun struct {
	Attempt           string `json:"attempt"`
	PlanDigest        string `json:"planDigest"`
	Lane              string `json:"lane"`
	Status            string `json:"status"`
	FailureCode       string `json:"failureCode,omitempty"`
	ObservedOutputs   int    `json:"observedOutputs"`
	RequiredOutputs   int    `json:"requiredOutputs"`
	CleanupIncomplete bool   `json:"cleanupIncomplete"`
	Cleanup           string `json:"cleanup,omitempty"`
	Record            string `json:"record"`
}

// RunListDiagnostic names an attempts-root entry without an admissible record.
type RunListDiagnostic struct {
	Entry       string `json:"entry"`
	Problem     string `json:"problem"`
	Code        string `json:"code"`
	Description string `json:"description"`
}

// ListRuns lists the attempt directories directly under attemptsRoot. It walks
// to the root and opens each entry and record through no-follow descriptors,
// inspects each record as InspectRun does, and never opens payloads, follows
// symlinks or descends below an attempt directory. When any entry has no
// admissible record the inventory is returned with a coded error, so a partial
// inventory is never mistaken for a complete one.
func ListRuns(ctx context.Context, attemptsRoot string) (RunList, error) {
	return listRuns(ctx, attemptsRoot, MaxRunListEntries)
}

func listRuns(ctx context.Context, attemptsRoot string, limit int) (RunList, error) {
	if ctx == nil {
		return RunList{}, e.New(e.CodeUsage, "Run listing requires a context")
	}
	if attemptsRoot == "" {
		return RunList{}, e.New(e.CodeUsage, "Run listing requires an attempts root")
	}
	if err := ctx.Err(); err != nil {
		return RunList{}, e.Wrap(e.CodeTimeout, err, "Run listing canceled")
	}
	root, err := openLabeledDirectory(ctx, attemptsRoot, "attempts root", "attempts root directories")
	if err != nil {
		return RunList{}, err
	}
	defer root.Close()
	entries, err := readRunEntries(ctx, root, limit)
	if err != nil {
		return RunList{}, err
	}
	list := RunList{SchemaVersion: "nvkr.dev/run-list/v1", Authority: "description-only", Runs: []ListedRun{}, Diagnostics: []RunListDiagnostic{}}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return RunList{}, e.Wrap(e.CodeTimeout, err, "Run listing canceled")
		}
		run, problem, err := listRunEntry(ctx, root, entry)
		if problem == "" && err != nil {
			return RunList{}, err
		}
		if problem != "" {
			list.Diagnostics = append(list.Diagnostics, RunListDiagnostic{
				Entry: entry.Name(), Problem: problem, Code: string(e.CodeVerifyFailed), Description: runProblemDescriptions[problem],
			})
			continue
		}
		run.Record = filepath.Join(attemptsRoot, entry.Name(), "lifecycle.json")
		list.Runs = append(list.Runs, run)
	}
	if len(list.Diagnostics) > 0 {
		return list, e.New(e.CodeVerifyFailed, "%d of %d attempts-root entries have no admissible Run record", len(list.Diagnostics), len(entries))
	}
	return list, nil
}

// readRunEntries reads at most limit entries and refuses a larger root before
// any record is inspected, sorted by name for a deterministic listing.
func readRunEntries(ctx context.Context, root *os.File, limit int) ([]os.DirEntry, error) {
	var entries []os.DirEntry
	for {
		if err := ctx.Err(); err != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "Run listing canceled")
		}
		batch, err := root.ReadDir(limit + 1 - len(entries))
		entries = append(entries, batch...)
		if len(entries) > limit {
			return nil, e.New(e.CodeVerifyFailed, "attempts root holds more than %d entries", limit)
		}
		if errors.Is(err, io.EOF) || (err == nil && len(batch) == 0) {
			break
		}
		if err != nil {
			return nil, e.New(e.CodeVerifyFailed, "cannot read the attempts root")
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

// listRunEntry inspects one entry. A problem names an entry without an
// admissible record; an error without a problem, such as cancellation, ends
// the listing.
func listRunEntry(ctx context.Context, root *os.File, entry os.DirEntry) (ListedRun, string, error) {
	switch {
	case entry.Type()&os.ModeSymlink != 0:
		return ListedRun{}, runEntrySymlink, nil
	case !entry.IsDir():
		return ListedRun{}, runEntryNotDirectory, nil
	}
	directory, _, err := securepath.OpenChild(root, entry.Name(), true)
	if err != nil {
		return ListedRun{}, runEntryUnsafe, nil
	}
	defer directory.Close()
	inspection, problem, err := inspectRunDirectory(ctx, directory)
	if err != nil {
		return ListedRun{}, problem, err
	}
	run := ListedRun{
		Attempt: inspection.Attempt, PlanDigest: inspection.PlanDigest, Lane: inspection.Lane, Status: inspection.Status,
		FailureCode: inspection.FailureCode, RequiredOutputs: len(inspection.Outputs), CleanupIncomplete: inspection.CleanupIncomplete,
	}
	for _, output := range inspection.Outputs {
		if output.State == "observed" {
			run.ObservedOutputs++
		}
	}
	if run.CleanupIncomplete {
		run.Cleanup = "attempt marker present; disk cleanup may be incomplete"
	}
	return run, "", nil
}
