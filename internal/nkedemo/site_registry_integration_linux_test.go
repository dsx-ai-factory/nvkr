// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package nkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This fixture owns port 5000 in a separate network namespace. Docker identity
// and node execution are fake; the bounded registry HTTP request is real. It
// proves start -> inspect -> HTTP readiness -> node probe -> returned endpoint,
// not operation of a real Kind node or publication to a real registry.
func TestRegistryStartOrdersReadinessBeforeReturningUploadEndpoint(t *testing.T) {
	if os.Getenv("NKD_TEST_REGISTRY_START") != "1" {
		t.Skip("opt-in isolated Linux registry-start fixture")
	}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	var network netip.Prefix
	for _, value := range addresses {
		candidate, err := netip.ParsePrefix(value.String())
		if err == nil && candidate.Addr().Is4() && candidate.Addr().IsPrivate() {
			if network.IsValid() {
				t.Fatal("fixture requires one isolated private IPv4 interface")
			}
			network = candidate
		}
	}
	if !network.IsValid() {
		t.Fatal("fixture requires an isolated private IPv4 interface")
	}
	host := net.JoinHostPort(network.Addr().String(), "5000")
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact", true: "node probe fails"}[failed], func(t *testing.T) {
			work := privateTestDir(t)
			trace, ready := filepath.Join(work, "commands"), filepath.Join(work, "http-ready")
			listener, err := net.Listen("tcp4", host)
			if err != nil {
				t.Fatal("isolated fixture port unavailable", err)
			}
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v2/" {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
					t.Error("could not record fixture HTTP readiness", err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				_, _ = w.Write([]byte("{}"))
			}))
			if err := server.Listener.Close(); err != nil {
				_ = listener.Close()
				t.Fatal(err)
			}
			server.Listener = listener
			server.Start()
			defer server.Close()
			var rows []dockerContainer
			if err := json.Unmarshal([]byte(`[{"Id":"owned-id","Name":"/private-registry","Image":"accepted","Config":{"Labels":{"dev.nkd.demo.owner":"private-owner"}},"State":{"Running":true},"Mounts":[{"Type":"tmpfs"}],"NetworkSettings":{"Networks":{"kind":{"NetworkID":"kind-id"}}}}]`), &rows); err != nil {
				t.Fatal(err)
			}
			binding := rows[0].NetworkSettings.Networks["kind"]
			binding.IPAddress = network.Addr().String()
			rows[0].NetworkSettings.Networks["kind"] = binding
			inspection := filepath.Join(work, "inspection.json")
			if err := privateJSON(inspection, rows); err != nil {
				t.Fatal(err)
			}
			// The fake exec refuses to run before the real HTTP request. It
			// validates the complete existing node curl vector, not just a verb.
			script := "#!/bin/sh\nset -eu\nprintf '%s\\n' \"$1\" >> \"$TRACE\"\ncase \"$1\" in\n run) printf 'owned-id\\n';;\n inspect) test \"$2\" = owned-id; cat \"$INSPECTION\";;\n exec) test -f \"$READY\"; test \"$2\" = owned-node; shift 2; test \"$*\" = \"curl --fail --silent --show-error --max-time 10 http://$HOST/v2/\"; test \"$NODE_FAIL\" = 0 || exit 17; printf '{}';;\n *) exit 42;;\nesac\n"
			if err := os.WriteFile(filepath.Join(work, "docker"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			status := "0"
			if failed {
				status = "1"
			}
			runner := Runner{Work: work, Env: []string{"PATH=" + work + ":/usr/bin:/bin", "TRACE=" + trace, "INSPECTION=" + inspection, "READY=" + ready, "HOST=" + host, "NODE_FAIL=" + status}}
			gateway := network.Masked().Addr().Next()
			if gateway == network.Addr() {
				gateway = networkLast(network.Masked()).Prev()
			}
			materials := &SiteMaterials{RegistryID: "accepted", KindNetworkID: "kind-id", KindCIDR: network.Masked().String(), Gateway: gateway.String()}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			returned, err := startSiteRegistry(ctx, runner, "private-registry", "private-owner", "owned-node", materials)
			if !failed && (err != nil || returned != host) {
				t.Fatal("exact registry did not return its probed endpoint", err)
			}
			if failed {
				var command *CommandError
				if returned != "" || !errors.As(err, &command) || command.Phase != "node-registry-connectivity" || command.ExitCode != 17 {
					t.Fatal("failed node route returned an upload endpoint or lost its exact failure", err)
				}
			}
			raw, err := os.ReadFile(trace)
			if err != nil || string(raw) != "run\ninspect\nexec\n" || strings.Contains(string(raw), "oras") {
				t.Fatal("registry start reordered commands or reached an upload", err)
			}
		})
	}
}
