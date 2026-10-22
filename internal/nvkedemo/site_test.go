// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func TestKindNetworkAdmission(t *testing.T) {
	for _, row := range []struct {
		name, gateway, subnet, driver string
		ok                            bool
	}{
		{"discovered-subnet", "172.29.0.1", "172.29.0.0/16", "bridge", true},
		{"invalid-gateway", "invalid", "172.29.0.0/16", "bridge", false},
		{"public-gateway", "8.8.8.8", "8.8.8.0/24", "bridge", false},
		{"outside-subnet", "172.29.0.1", "172.30.0.0/16", "bridge", false},
		{"no-ipv4", "fd00::1", "fd00::/64", "bridge", false},
		{"wrong-driver", "172.29.0.1", "172.29.0.0/16", "host", false},
		{"guest-overlap", "172.30.0.1", "172.30.0.0/24", "bridge", false},
		{"vip-overlap", "172.31.0.1", "172.31.0.0/24", "bridge", false},
		{"pod-overlap", "10.244.0.1", "10.244.0.0/16", "bridge", false},
		{"service-overlap", "10.96.0.1", "10.96.0.0/16", "bridge", false},
		{"noncanonical-subnet", "172.29.0.1", "172.29.0.1/16", "bridge", false},
	} {
		t.Run(row.name, func(t *testing.T) {
			root := privateTestDir(t)
			output, err := json.Marshal([]map[string]any{{"Id": "exact-kind-network", "Driver": row.driver, "IPAM": map[string]any{"Config": []map[string]string{{"Gateway": row.gateway, "Subnet": row.subnet}}}}})
			if err != nil {
				t.Fatal(err)
			}
			body := "#!/bin/sh\n[ \"$1:$2:$3\" = network:inspect:kind ] || exit 99\nprintf '%s' '" + string(output) + "'\n"
			if err = os.WriteFile(filepath.Join(root, "docker"), []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			actual, err := admitKindNetwork(t.Context(), Runner{Env: []string{"PATH=" + root}})
			if (err == nil) != row.ok {
				t.Fatalf("network=%+v error=%v", actual, err)
			}
			if row.ok && actual != (kindNetwork{Gateway: row.gateway, CIDR: row.subnet, ID: "exact-kind-network"}) {
				t.Fatalf("did not retain exact discovered network: %+v", actual)
			}
			if !row.ok && actual != (kindNetwork{}) {
				t.Fatal("invalid network returned an admitted gateway")
			}
		})
	}
}

func TestRuntimeValuesUseObservedNativeDigests(t *testing.T) {
	outputs := make(map[string]SelectedOutput)
	for i, name := range runtimeImages {
		outputs["runtime-"+name] = SelectedOutput{Observation: lifecycle.Artifact{Digest: "sha256:" + strings.Repeat(string(rune('a'+i%6)), 64)}}
	}
	outputs["postgresql"] = SelectedOutput{Observation: lifecycle.Artifact{Digest: "chart-pin"}}
	values := runtimeValues("172.18.0.1:15501", outputs)
	postgres := values["postgresql"].(postgresOverrides)
	if postgres.Image.Digest != outputs["runtime-postgresql"].Observation.Digest || outputs["postgresql"].Observation.Digest != "chart-pin" {
		t.Fatal("chart/image namespaces confused")
	}
	spire := values["spire"].(spireOverrides)
	if spire.Agent.SocketAlternate.Image.Tag != outputs["runtime-spire-bash"].Observation.Digest || spire.Agent.FSGroupFix.Image != spire.Agent.SocketAlternate.Image || spire.CSI.NodeDriverRegistrar.Image.Tag != outputs["runtime-csi-node-driver-registrar"].Observation.Digest {
		t.Fatal("SPIRE roles not pinned")
	}
	temporal := values["temporal"].(temporalOverrides)
	if temporal.Server.Image.Tag != "1.30.3@"+outputs["runtime-temporal-server"].Observation.Digest {
		t.Fatal("Temporal native digest missing")
	}
}
