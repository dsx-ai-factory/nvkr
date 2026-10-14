// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDebianLifecycleExportRejectsRestoredRunBeforeWriter(t *testing.T) {
	plan, original, _ := debianRunOwnerFixture(t)
	for _, mode := range []string{"decoded", "foreign shallow alias", "missing terminal"} {
		t.Run(mode, func(t *testing.T) {
			result := &LocalRunResult{}
			want := e.CodeProvenanceMissing
			if mode == "foreign shallow alias" {
				result.debian = original.debian
				want = e.CodeVerifyFailed
			}
			if mode == "missing terminal" {
				result = original
			}
			var destination bytes.Buffer
			observed, document, predicate, err := result.ExportDebianLifecyclePayload(t.Context(), plan, plan.LifecycleOutputs[0], &destination)
			if factoryErrorCode(err) != want || observed.Digest != "" || len(document) != 0 || len(predicate) != 0 || destination.Len() != 0 {
				t.Fatal("description reached export", err)
			}
		})
	}
}

func TestDebianSchedulerMetadataCannotFollowForeignParent(t *testing.T) {
	for _, mode := range []string{"write", "reset"} {
		t.Run(mode, func(t *testing.T) {
			workspace := t.TempDir()
			foreign := t.TempDir()
			if err := os.Mkdir(filepath.Join(foreign, "sbom"), 0700); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(foreign, "sbom", "sentinel")
			if err := os.WriteFile(sentinel, []byte("preserve foreign content"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(foreign, filepath.Join(workspace, "agent")); err != nil {
				t.Fatal(err)
			}
			state := &debianRunState{workspace: workspace}
			if err := state.openDirectory(); err != nil {
				t.Fatal(err)
			}
			defer state.directory.Close()
			var err error
			if mode == "write" {
				err = state.writeOwnedFile(t.Context(), filepath.Join(workspace, "agent", "sbom", "new.json"), []byte("metadata"), 0444)
			} else {
				plan := &Plan{Graph: ArtifactGraph{Artifacts: []Artifact{{ID: "sbom/agent", Path: ".nvkr/factory/agent/sbom"}}}}
				err = state.resetNodeOutputs(plan, Node{Outputs: []string{"sbom/agent"}})
			}
			if factoryErrorCode(err) != e.CodeImmutableViolation {
				t.Fatal("foreign parent was followed", err)
			}
			raw, readErr := os.ReadFile(sentinel)
			if readErr != nil || string(raw) != "preserve foreign content" {
				t.Fatal("foreign sentinel changed", readErr)
			}
			entries, readErr := os.ReadDir(filepath.Dir(sentinel))
			if readErr != nil || len(entries) != 1 {
				t.Fatal("foreign writes escaped", readErr)
			}
		})
	}
}
