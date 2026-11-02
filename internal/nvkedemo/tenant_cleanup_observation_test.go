// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTenantCleanupObservationsAreBoundedPrivateAndLatest(t *testing.T) {
	work := privateTestDir(t)
	writer := &recordedProgress{}
	runner := Runner{Work: work, Env: []string{"PATH=/usr/bin:/bin"}, Progress: writer, output: writer}
	var observations tenantCleanupObservations
	for i, phase := range []string{"inventory", "delete", "inventory", "inventory"} {
		command := "printf private-first; printf private-stderr >&2"
		if i == 3 {
			command = "head -c 263168 /dev/zero; exit 7"
		}
		_, err := observations.capture(t.Context(), runner, []string{"/bin/sh", "-c", command}, phase, i+1, time.Second)
		if i == 3 && CommandExitCode(err) != 7 || i != 3 && err != nil {
			t.Fatalf("command result changed: %v", err)
		}
	}
	if writer.Len() != 0 || len(writer.events) != 0 {
		t.Fatal("private cleanup reached public output")
	}
	if observations.Initial.Sequence != 1 || observations.Delete.Sequence != 2 || observations.Latest.Sequence != 4 || !observations.Latest.Truncated || len(observations.Latest.Output) != tenantCleanupObservationLimit {
		t.Fatalf("cleanup retention bounds changed: %+v", observations)
	}
	if !strings.Contains(string(observations.Initial.Output), "private-first") || !strings.Contains(string(observations.Initial.Output), "private-stderr") {
		t.Fatal("combined command diagnostics lost")
	}
	if err := observations.preserve(t.Context(), work); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(work)
	if err != nil || len(entries) != 1 {
		t.Fatalf("cleanup accumulated per-poll files: %v %v", entries, err)
	}
	path := filepath.Join(work, "tenant-cleanup-observations.json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 || info.Size() > 2<<20 {
		t.Fatalf("unsafe or unbounded cleanup output: %v %v", info, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded tenantCleanupObservations
	if err = json.Unmarshal(raw, &decoded); err != nil || decoded.Latest.Sequence != 4 || decoded.Latest.ExitCode != 7 {
		t.Fatalf("latest failed observation not retained: %+v %v", decoded.Latest, err)
	}
}

func TestTenantCleanupObservationPersistenceRejectsUnsafeStorage(t *testing.T) {
	for _, scenario := range []string{"missing", "nonprivate", "symlink", "existing", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			work := privateTestDir(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			path := filepath.Join(work, "tenant-cleanup-observations.json")
			switch scenario {
			case "missing":
				work = filepath.Join(work, "missing")
			case "nonprivate":
				if err := os.Chmod(work, 0o755); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				link := work + "-link"
				if err := os.Symlink(work, link); err != nil {
					t.Fatal(err)
				}
				work = link
			case "existing":
				if err := os.WriteFile(path, []byte("foreign-observation"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				cancel()
			}
			observations := tenantCleanupObservations{Latest: &tenantCleanupObservation{Output: []byte("private")}}
			if err := observations.preserve(ctx, work); err == nil {
				t.Fatal("unsafe cleanup storage accepted")
			}
			if scenario == "existing" {
				raw, err := os.ReadFile(path)
				if err != nil || string(raw) != "foreign-observation" {
					t.Fatal("cleanup replaced existing diagnostics")
				}
			}
		})
	}
}

func TestTenantCleanupUnavailableStorageDoesNotPreventDeletion(t *testing.T) {
	tools := privateTestDir(t)
	if err := os.Mkdir(filepath.Join(tools, "cli"), 0o700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(tools, "deleted")
	row := tenantRow{Name: "private-tenant", ID: "private-owned-id", NICo: TenantProvider{OrgID, SiteID, VPCID}}
	encoded, err := json.Marshal([]tenantRow{row})
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \"$1:$2\" in\nget:clusters) if [ -f \"$STATE\" ]; then printf '[]'; else printf '%s' \"$ROW\"; fi;;\ndelete:cluster) touch \"$STATE\"; printf private-delete-response >&2;;\n*) exit 42;;\nesac\n"
	if err = os.WriteFile(filepath.Join(tools, "cli", installerCLIName), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	writer := &recordedProgress{}
	selected := tenant{name: row.Name, id: row.ID, intent: true, session: ManagementSession{Work: tools, Runner: Runner{Work: filepath.Join(tools, "missing"), Env: []string{"PATH=/usr/bin:/bin", "STATE=" + state, "ROW=" + string(encoded)}, Progress: writer}}}
	// Also deny persistence, without changing the exact admitted CLI location.
	if err = os.Chmod(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	if err = selected.restore(t.Context()); err != nil {
		t.Fatal("diagnostic storage prevented deletion", err)
	}
	if _, err = os.Stat(state); err != nil {
		t.Fatal("owned delete was not invoked", err)
	}
	if strings.Contains(writer.String(), row.Name) || strings.Contains(writer.String(), row.ID) || strings.Contains(writer.String(), "private-delete-response") {
		t.Fatal("private cleanup leaked to public progress")
	}
	if _, err = os.Stat(filepath.Join(tools, "tenant-cleanup-observations.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unsafe workspace gained cleanup observations")
	}
}

func TestTenantCleanupCapturePreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var observations tenantCleanupObservations
	_, err := observations.capture(ctx, Runner{Env: []string{"PATH=/usr/bin:/bin"}}, []string{"/bin/sh", "-c", "exit 0"}, "delete", 1, time.Second)
	if !errors.Is(err, context.Canceled) || observations.Delete.ExitCode != -1 || observations.LastCommand.Status != "canceled" {
		t.Fatalf("cleanup changed owning command cancellation: %v", err)
	}
}

func TestTenantCleanupEmptyCanceledPollKeepsLastResponse(t *testing.T) {
	var observations tenantCleanupObservations
	runner := Runner{Env: []string{"PATH=/usr/bin:/bin"}}
	_, err := observations.capture(t.Context(), runner, []string{"/bin/sh", "-c", "printf private-last-inventory"}, "inventory", 1, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = observations.capture(ctx, runner, []string{"/bin/sh", "-c", "exit 0"}, "inventory", 2, time.Second)
	if !errors.Is(err, context.Canceled) || observations.LastCommand.Sequence != 2 || observations.LastCommand.Status != "canceled" || observations.Latest.Sequence != 1 || string(observations.Latest.Output) != "private-last-inventory" {
		t.Fatalf("final command erased last observed state: %+v %+v %v", observations.LastCommand, observations.Latest, err)
	}
}

func TestTenantDeletionWaitHonorsDeadlineCancellationAndOwnership(t *testing.T) {
	for _, scenario := range []string{"absent", "short-parent", "short-wait", "canceled", "foreign", "invalid-budget"} {
		t.Run(scenario, func(t *testing.T) {
			work := privateTestDir(t)
			if err := os.Mkdir(filepath.Join(work, "cli"), 0o700); err != nil {
				t.Fatal(err)
			}
			row := tenantRow{Name: "private-tenant", ID: "owned", NICo: TenantProvider{OrgID, SiteID, VPCID}}
			if scenario == "foreign" {
				row.ID = "foreign"
			}
			encoded, err := json.Marshal([]tenantRow{row})
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "absent" {
				encoded = []byte("[]")
			}
			script := "#!/bin/sh\n[ \"$1:$2\" = get:clusters ] || exit 42\nprintf '%s' \"$ROW\"\n"
			if err = os.WriteFile(filepath.Join(work, "cli", installerCLIName), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			selected := tenant{name: row.Name, id: "owned", cleaning: true, session: ManagementSession{Work: work, Runner: Runner{Env: []string{"PATH=/usr/bin:/bin", "ROW=" + string(encoded)}}}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			budget := tenantDeletionWaitTimeout
			switch scenario {
			case "short-parent":
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 100*time.Millisecond)
				defer stop()
			case "short-wait":
				budget = 100 * time.Millisecond
			case "canceled":
				cancel()
			case "invalid-budget":
				budget = 0
			}
			err = selected.waitDeleted(ctx, budget)
			switch scenario {
			case "absent":
				if err != nil {
					t.Fatal(err)
				}
			case "short-parent", "short-wait":
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("shorter deletion deadline was detached or reported successful", err)
				}
			case "canceled":
				if !errors.Is(err, context.Canceled) {
					t.Fatal("deletion wait detached its own cancellation", err)
				}
			case "foreign":
				if err == nil || !strings.Contains(err.Error(), "refusing adoption or removal") {
					t.Fatal("deletion wait accepted changed tenant identity", err)
				}
			case "invalid-budget":
				if err == nil || selected.sequence != 0 {
					t.Fatal("invalid wait budget performed work", err)
				}
			}
		})
	}
}
