//go:build integration

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package gitlabdocker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The failure reporter may emit static source text, never dynamic trace values.
// An absolute source path also guards the actual failure-path regression where
// a second root was incorrectly joined to an already absolute inventory entry.
func TestFailureDiagnostics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner.go")
	must(t, os.WriteFile(path, []byte(`package fixture
func rejected() { e.New(e.CodeVerifyFailed, "fixed static refusal"); e.Wrap(e.CodePipelineDrift, cause, "dynamic field %q is foreign", privateValue) }
`), 0600))
	for _, tc := range []struct {
		name, trace string
		want        []string
	}{
		{"fixed", "[NVKR-E-VERIFY-FAILED] fixed static refusal", []string{"owner.go: CodeVerifyFailed: fixed static refusal"}},
		{"dynamic secret", `[NVKR-E-PIPELINE-DRIFT] dynamic field "synthetic-private-token" is foreign`, []string{"owner.go: CodePipelineDrift: dynamic field "}},
		{"unrecognized private text", "synthetic-private-token", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sourceOwnedFailureMessages(t.Context(), []string{path}, []byte(tc.trace))
			must(t, err)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("source-owned diagnostics %q, want %q", got, tc.want)
			}
			if strings.Contains(strings.Join(got, ""), "synthetic-private-token") {
				t.Fatal("diagnostic disclosed a dynamic trace value")
			}
		})
	}
	t.Run("caller cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := sourceOwnedFailureMessages(ctx, []string{path}, nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled diagnostic read returned %v", err)
		}
	})
}

func TestContainerCleanupInventory(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      []string
		invalid   bool
	}{
		{"already absent", "", nil, false},
		{"unrelated container", "\"unrelated\"\n", nil, false},
		{"lost seed removal acknowledgement", "\"fixture-seed\"\n", []string{"fixture-seed"}, false},
		{"invalid JSON", "not-json", nil, true},
		{"missing name", "null\n", nil, true},
		{"empty name", "\"\"\n", nil, true},
		{"ambiguous names", "\"fixture-seed,alias\"\n", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := remainingKnownContainers([]byte(tc.raw), []string{"fixture-server", "fixture-seed"})
			if (err != nil) != tc.invalid || !slices.Equal(got, tc.want) {
				t.Fatalf("inventory result %q error %v; want %q invalid %t", got, err, tc.want, tc.invalid)
			}
		})
	}
}

func TestCleanupStopsCreatorsBeforeProducerSnapshot(t *testing.T) {
	creatorStopped, producerRemoved := false, false
	l := &lab{t: t, name: "fixture", runner: "fixture-runner", buildMount: "/owned/builds", containers: []string{"fixture-runner"}, volumes: []string{"fixture-builds"}}
	l.cleanupRun = func(_ context.Context, args ...string) ([]byte, error) {
		switch {
		case slices.Equal(args, []string{"network", "inspect", "--format", "{{range $id,$c := .Containers}}{{$id}} {{end}}", "fixture"}):
			return []byte("creator"), nil
		case slices.Equal(args, []string{"rm", "--force", "--volumes", "creator"}):
			// The creator starts one last bridge-network producer before it exits.
			creatorStopped = true
			return nil, nil
		case slices.Equal(args, []string{"ps", "--all", "--quiet", "--no-trunc"}):
			if !creatorStopped {
				t.Fatal("producer snapshot preceded creator shutdown")
			}
			return []byte("producer"), nil
		case slices.Equal(args, []string{"inspect", "--format", "{{json .Mounts}}", "producer"}):
			return []byte(`[{"Source":"/owned/builds/work"}]`), nil
		case slices.Equal(args, []string{"rm", "--force", "--volumes", "producer"}):
			producerRemoved = true
			return nil, nil
		case slices.Equal(args, []string{"volume", "rm", "fixture-builds"}):
			if !producerRemoved {
				t.Fatal("volume removal preceded last producer removal")
			}
		}
		return nil, nil
	}
	l.cleanup()
	if !creatorStopped || !producerRemoved {
		t.Fatal("cleanup lost a creator or its last producer")
	}
}

func TestCleanupContainerInspectionRace(t *testing.T) {
	inspectionErr := errors.New("synthetic inspection failure")
	for _, tc := range []struct {
		name, inventory string
		inventoryErr    error
		wantErr         bool
	}{
		{name: "unrelated auto-removed container", inventory: "other-full-id\n"},
		{name: "container still exists", inventory: "selected-full-id\n", wantErr: true},
		{name: "unknown current inventory", inventoryErr: errors.New("synthetic inventory failure"), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			l := &lab{cleanupRun: func(_ context.Context, args ...string) ([]byte, error) {
				calls++
				if calls == 1 && slices.Equal(args, []string{"inspect", "--format", "{{json .Mounts}}", "selected-full-id"}) {
					return nil, inspectionErr
				}
				if calls == 2 && slices.Equal(args, []string{"ps", "--all", "--quiet", "--no-trunc"}) {
					return []byte(tc.inventory), tc.inventoryErr
				}
				t.Fatalf("unexpected cleanup command %v", args)
				return nil, nil
			}}
			raw, err := l.cleanupMounts(t.Context(), "selected-full-id")
			if calls != 2 || raw != nil || (err != nil) != tc.wantErr {
				t.Fatalf("inspection race raw %q err %v calls %d", raw, err, calls)
			}
			if tc.wantErr && !errors.Is(err, inspectionErr) && !errors.Is(err, tc.inventoryErr) {
				t.Fatalf("cleanup lost the refusal cause: %v", err)
			}
		})
	}
}

func TestGitLabDiskPrerequisite(t *testing.T) {
	for _, tc := range []struct {
		name, facts string
		wantErr     bool
	}{
		{"exact minimum", "9175040 4096\n", false},
		{"below minimum", "9175039 4096\n", true},
		{"unknown", "", true},
		{"malformed", "not-stat-output", true},
		{"negative", "-1 4096", true},
		{"zero block size", "9175040 0", true},
		{"overflow", "18446744073709551615 4096", true},
		{"trailing facts", "9175040 4096 unexpected", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := requireGitLabDiskSpace([]byte(tc.facts)); (err != nil) != tc.wantErr {
				t.Fatalf("disk prerequisite error %v, want error %t", err, tc.wantErr)
			}
		})
	}
}
