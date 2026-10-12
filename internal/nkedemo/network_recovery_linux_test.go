// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package nkedemo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
)

// This native integration fixture needs a separate process/network namespace:
// RecoverNetworkGuard acquires the real per-UID lock and checks its fixed disk
// service port. It never changes host networking; firewall commands are fake.
func TestRecoveredKindSimulatorJournalCheckpointsExactBinding(t *testing.T) {
	if runtime.GOARCH != "arm64" || os.Getenv("NKD_TEST_NETWORK_RECOVERY") != "1" {
		t.Skip("opt-in isolated Linux ARM64 recovery fixture")
	}
	for _, gateway := range []string{"172.18.0.1", "172.18.0.2"} {
		t.Run(gateway, func(t *testing.T) {
			original := networkFixture(t)
			original.kindGateway = gateway
			row := kindSimulatorRule(original.Owner, original.KindCIDR, gateway)
			original.rules = []networkRule{row}
			raw, err := canonical.Marshal(original.snapshot())
			if err != nil {
				t.Fatal(err)
			}
			if err := networkWriteExclusive(filepath.Join(original.Work, networkJournal), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			tools := privateTestDir(t)
			for _, name := range networkTools {
				if err := os.WriteFile(filepath.Join(tools, name), []byte("#!/bin/sh\nexit 99\n"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			recovered, err := RecoverNetworkGuard(t.Context(), Runner{Work: original.Work, Env: []string{"PATH=" + tools}})
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.releaseHandles()
			if recovered.kindGateway != gateway || len(recovered.rules) != 1 || !slices.Equal(recovered.rules[0].Args, row.Args) {
				t.Fatal("recovery discarded its authenticated Kind route binding")
			}
			recovered.cleanupCheckpoint(t.Context())
			if len(recovered.persistenceFailures) != 0 {
				t.Fatal("recovered guard could not persist its original gateway", recovered.persistenceFailures)
			}
			state, err := readNetworkState(original.Work)
			if err != nil || state.KindGateway != gateway {
				t.Fatal("checkpoint changed or lost the original gateway", err)
			}
			present, deletes := true, 0
			recovered.capture = func(_ context.Context, args []string, privileged bool) ([]byte, error) {
				if !privileged || len(args) < 7 || args[0] != "iptables" || args[4] != row.Table || args[6] != row.Chain || !slices.Equal(args[7:], row.Args) {
					t.Fatalf("recovered guard changed its exact withdrawal closure: %v", args)
				}
				switch args[5] {
				case "-C":
					if !present {
						return nil, &CommandError{ExitCode: 1, Cause: errors.New("absent")}
					}
				case "-D":
					present, deletes = false, deletes+1
				default:
					t.Fatalf("unexpected recovered firewall verb: %v", args)
				}
				return nil, nil
			}
			if err := recovered.withdrawRule(t.Context(), row); err != nil || present || deletes != 1 {
				t.Fatal("recovered exact rule did not withdraw once", err)
			}
			recovered.cleanupCheckpoint(t.Context())
			if len(recovered.persistenceFailures) != 0 {
				t.Fatal("withdrawal checkpoint lost the gateway binding", recovered.persistenceFailures)
			}
		})
	}
}
