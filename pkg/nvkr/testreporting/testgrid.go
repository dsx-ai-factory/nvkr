// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package testreporting

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
)

// TestGridDestination has no inferred bucket, dashboard, tab or retention policy.
// Prefix follows the existing publisher's exact groups/group/dashboard/tab/build
// hierarchy. Backend registration and service credentials remain operator owned.
type TestGridDestination struct {
	Bucket            string `json:"bucket"`
	Prefix            string `json:"prefix"`
	ObservationDigest string `json:"observationDigest"`
	Namespace         string `json:"namespace"`
	SourcePath        string `json:"sourcePath"`
}
type ProjectedFile struct {
	Name  string `json:"name"`
	Bytes []byte `json:"-"`
}
type TestGridProjection struct {
	Bucket    string          `json:"bucket"`
	Prefix    string          `json:"prefix"`
	Files     []ProjectedFile `json:"-"`
	Published bool            `json:"published"`
}
type started struct {
	Timestamp int64             `json:"timestamp"`
	Metadata  map[string]string `json:"metadata"`
}
type finished struct {
	Timestamp int64             `json:"timestamp"`
	Passed    bool              `json:"passed"`
	Result    string            `json:"result"`
	Metadata  map[string]string `json:"metadata"`
}

func (d TestGridDestination) Validate() error {
	if !text(d.Bucket) || strings.ContainsAny(d.Bucket, "/\\:@ ") || !safePath(d.Prefix) || !canonical.IsDigest(d.ObservationDigest) || !text(d.Namespace) || !safePath(d.SourcePath) {
		return e.New(e.CodeUsage, "explicit TestGrid bucket, source, observation and safe prefix required")
	}
	parts := strings.Split(d.Prefix, "/")
	if len(parts) != 5 || parts[0] != "groups" {
		return e.New(e.CodeUsage, "TestGrid prefix must explicitly select groups/group/dashboard/tab/build")
	}
	return nil
}
func observedTime(o testobservation.Observation, name, phase string) (int64, error) {
	for _, fact := range o.Environment.Facts {
		if fact.Name == name && fact.Phase == phase {
			value, err := strconv.ParseInt(fact.Value, 10, 64)
			if err != nil || value <= 0 || strconv.FormatInt(value, 10) != fact.Value {
				return 0, failure("observed test timestamp is not canonical positive Unix seconds")
			}
			return value, nil
		}
	}
	return 0, e.New(e.CodeGateUnmet, "TestGrid projection requires covered %s environment fact for %s; no timestamp is fabricated", phase, name)
}

// TestGrid generates local started/finished/evidence JSON plus the exact original
// JUnit files. It copies no credentials and never asserts upload or UI acceptance.
// Multiple original files use deterministic junit.<name-hash>.xml transport labels,
// admitted by TestGrid's pinned JUnit basename matcher; no results are synthesized.
func (r *Report) TestGrid(ctx context.Context, destination TestGridDestination) (TestGridProjection, error) {
	if err := destination.Validate(); err != nil {
		return TestGridProjection{}, err
	}
	if r == nil {
		return TestGridProjection{}, failure("report unavailable")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	index, err := r.indexLocked(ctx)
	if err != nil {
		return TestGridProjection{}, err
	}
	selected, err := selectGridEntry(index, destination)
	if err != nil {
		return TestGridProjection{}, err
	}
	owner, err := r.gridOwner(*selected)
	if err != nil {
		return TestGridProjection{}, err
	}

	o := selected.Observation
	begin, err := observedTime(o, "testStartedUnix", "before")
	if err != nil {
		return TestGridProjection{}, err
	}
	end, err := observedTime(o, "testFinishedUnix", "after")
	if err != nil {
		return TestGridProjection{}, err
	}
	if end < begin {
		return TestGridProjection{}, failure("observed completion precedes start")
	}
	resultFiles := []testobservation.File{}
	for _, file := range o.Files {
		if file.Role == "junit" {
			resultFiles = append(resultFiles, file)
		}
	}
	files := []ProjectedFile{}
	bindings := []TestGridMember{}
	seen := map[string]bool{}
	for _, file := range resultFiles {
		projectedName := "artifacts/junit.xml"
		if len(resultFiles) > 1 {
			projectedName = fmt.Sprintf("artifacts/junit.%x.xml", sha256.Sum256([]byte(file.Name)))
		}
		if seen[projectedName] {
			return TestGridProjection{}, failure("colliding TestGrid filename transport labels")
		}
		seen[projectedName] = true
		raw, err := owner.Read(ctx, file.Name)
		if err != nil {
			return TestGridProjection{}, err
		}
		files = append(files, ProjectedFile{Name: projectedName, Bytes: raw})
		bindings = append(bindings, TestGridMember{Name: projectedName, CoveredName: file.Name, Digest: file.Digest, Size: file.Size})
	}

	metadata := map[string]string{"pipeline_id": strings.Split(destination.Prefix, "/")[4], "service_name": "nvkr", "test_suite": o.Suite.Name, "job_name": o.Suite.Name, "nvkr_observation_reference": selected.Reference, "nvkr_observation_digest": selected.ObservationDigest, "nvkr_signer_tier": selected.Tier, "nvkr_execution_status": o.Execution.Status, "nvkr_cleanup_status": o.Cleanup.Status, "nvkr_origin_status": o.Origin.Status, "nvkr_timestamp_scope": "producer-invocation-interval", "nvkr_result_reason": fmt.Sprintf("execution=%s; cleanup=%s; observed_cases=%d", o.Execution.Status, o.Cleanup.Status, o.Summary.Total)}
	startRaw, err := canonical.Marshal(started{Timestamp: begin, Metadata: metadata})
	if err != nil {
		return TestGridProjection{}, err
	}
	status := "FAILURE"
	if selected.Passing {
		status = "SUCCESS"
	}
	finishRaw, err := canonical.Marshal(finished{Timestamp: end, Passed: selected.Passing, Result: status, Metadata: metadata})
	if err != nil {
		return TestGridProjection{}, err
	}
	evidenceRaw, err := canonical.Marshal(testGridEvidence{Index: Index{SchemaVersion: SchemaVersion, Scope: index.Scope, Entries: []Entry{*selected}}, Files: bindings})
	if err != nil {
		return TestGridProjection{}, err
	}
	if _, err := owner.Description(ctx); err != nil {
		return TestGridProjection{}, err
	}
	if err := r.check(ctx); err != nil {
		return TestGridProjection{}, err
	}
	allFiles := []ProjectedFile{{Name: "started.json", Bytes: startRaw}}
	allFiles = append(allFiles, files...)
	allFiles = append(allFiles, ProjectedFile{Name: "artifacts/evidence.json", Bytes: evidenceRaw}, ProjectedFile{Name: "finished.json", Bytes: finishRaw})
	return TestGridProjection{Bucket: destination.Bucket, Prefix: destination.Prefix, Published: false, Files: allFiles}, nil
}

// TestGridMember maps a deterministic transport label to the exact authenticated
// inventory. A filename hash is only a collision-checked transport label.
type TestGridMember struct {
	Name        string `json:"name"`
	CoveredName string `json:"coveredName"`
	Digest      string `json:"digest"`
	Size        int64  `json:"size"`
}
type testGridEvidence struct {
	Index Index            `json:"index"`
	Files []TestGridMember `json:"files"`
}

func selectGridEntry(index Index, destination TestGridDestination) (*Entry, error) {
	var selected *Entry
	for i := range index.Entries {
		entry := &index.Entries[i]
		if entry.ObservationDigest == destination.ObservationDigest && entry.Namespace == destination.Namespace && entry.SourcePath == destination.SourcePath {
			if selected != nil {
				return nil, failure("ambiguous TestGrid source selection")
			}
			selected = entry
		}
	}
	if selected == nil {
		return nil, failure("TestGrid selection is outside verified report membership")
	}
	return selected, nil
}
func (r *Report) gridOwner(selected Entry) (receipt, error) {
	var owner receipt
	for _, held := range r.receipts {
		if held.input.Expected.ObservationDigest == selected.ObservationDigest && held.input.Admission.Namespace == selected.Namespace && held.input.Admission.SourcePath == selected.SourcePath && held.input.Locator.Reference == selected.Reference {
			owner = held.receipt
			break
		}
	}
	if owner == nil {
		return nil, failure("verified TestGrid source lifetime unavailable")
	}
	return owner, nil
}
