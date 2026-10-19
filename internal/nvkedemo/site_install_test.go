// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStackStateAdmissionPrecedesResources(t *testing.T) {
	for _, row := range []struct {
		path  string
		valid bool
	}{
		{"/srv/nvkr/demo", true}, {"/tmp-outside/demo", true},
		{"/tmp/demo", false}, {"/var/tmp/demo", false}, {"/private/tmp/demo", false},
		{filepath.Join(os.TempDir(), "demo"), false},
	} {
		t.Run(row.path, func(t *testing.T) {
			if err := admitStackStateRoot(row.path); (err == nil) != row.valid {
				t.Fatalf("state root admission: %v", err)
			}
		})
	}
}

func TestProviderProjectionRequiresSourceBindingAndNativeDigest(t *testing.T) {
	for _, scenario := range []string{"exact", "tag-only", "invalid-digest", "foreign-artifact", "foreign-image", "missing-manifest", "existing-kustomization", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			work := t.TempDir()
			dir := filepath.Join(work, "capnico")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			reference := "172.18.0.3:5000/nvkr-local/provider@sha256:" + strings.Repeat("a", 64)
			binding := "image: ghcr.io/dsx-ai-factory/cluster-api-provider-nico/controller:v0.0.45\nartifact: capnico-controller\n"
			switch scenario {
			case "tag-only":
				reference = "172.18.0.3:5000/nvkr-local/provider:mutable"
			case "invalid-digest":
				reference = "172.18.0.3:5000/nvkr-local/provider@sha256:invalid"
			case "foreign-artifact":
				binding = strings.Replace(binding, "capnico-controller", "other", 1)
			case "foreign-image":
				binding = strings.Replace(binding, ":v0.0.45", ":v9.9.9", 1)
			}
			if err := privateWrite(filepath.Join(dir, "release.yaml"), []byte(binding)); err != nil {
				t.Fatal(err)
			}
			if scenario != "missing-manifest" {
				if err := privateWrite(filepath.Join(dir, "capnico.yaml"), []byte("source manifest\n")); err != nil {
					t.Fatal(err)
				}
			}
			projection := filepath.Join(dir, "kustomization.yaml")
			if scenario == "existing-kustomization" {
				if err := privateWrite(projection, []byte("foreign projection")); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if scenario == "canceled" {
				cancel()
			}
			err := projectProviderImage(ctx, work, reference)
			if (err == nil) != (scenario == "exact") {
				t.Fatalf("projection %s: %v", scenario, err)
			}
			raw, readErr := os.ReadFile(projection)
			if scenario == "exact" {
				var got struct {
					Resources []string
					Images    []struct{ Name, NewName, Digest string }
				}
				if readErr != nil || json.Unmarshal(raw, &got) != nil || len(got.Resources) != 1 || got.Resources[0] != "capnico.yaml" || len(got.Images) != 1 || got.Images[0].NewName+"@"+got.Images[0].Digest != reference || got.Images[0].Name != "ghcr.io/dsx-ai-factory/cluster-api-provider-nico/controller" {
					t.Fatal("provider projection lost source manifest or selected native identity")
				}
			} else if scenario == "existing-kustomization" {
				if readErr != nil || string(raw) != "foreign projection" {
					t.Fatal("overwrote existing projection")
				}
			} else if !os.IsNotExist(readErr) {
				t.Fatal("published rejected projection")
			}
		})
	}
}

func TestStackNodeOnlyRemovesOwnedSchedulingTaint(t *testing.T) {
	for _, scenario := range []string{"untainted", "tainted", "foreign-node", "multiple-nodes", "unreadable", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			work := privateTestDir(t)
			script := "#!/bin/sh\n[ \"$1 $2\" = '--kubeconfig /owned/kubeconfig' ] || exit 90\nshift 2\ncase \"$1\" in\n get) [ \"$2 $3 $4\" = 'nodes -o json' ] || exit 91; printf '%s' \"$NODE_RESPONSE\"; exit \"$NODE_EXIT\" ;;\n taint) [ \"$2 $3 $4\" = 'node owned-control-plane node-role.kubernetes.io/control-plane:NoSchedule-' ] || exit 92; printf touched > \"$TAINT_RECEIPT\" ;;\n *) exit 93 ;;\nesac\n"
			if err := os.WriteFile(filepath.Join(work, "kubectl"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			response := `{"items":[{"metadata":{"name":"owned-control-plane"},"spec":{"taints":[]}}]}`
			exit := "0"
			switch scenario {
			case "tainted":
				response = strings.Replace(response, `"taints":[]`, `"taints":[{"key":"node-role.kubernetes.io/control-plane","effect":"NoSchedule"}]`, 1)
			case "foreign-node":
				response = strings.Replace(response, "owned-control-plane", "foreign-control-plane", 1)
			case "multiple-nodes":
				response = `{"items":[{},{}]}`
			case "unreadable":
				exit = "1"
			}
			receipt := filepath.Join(work, "taint-receipt")
			session := ManagementSession{Name: "owned", Work: work, Tools: work, Runner: Runner{Work: work, Env: []string{"SITE_KUBECONFIG=/owned/kubeconfig", "NODE_RESPONSE=" + response, "NODE_EXIT=" + exit, "TAINT_RECEIPT=" + receipt}}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if scenario == "canceled" {
				cancel()
			}
			err := prepareStackNode(ctx, session)
			if (err == nil) != (scenario == "tainted" || scenario == "untainted") {
				t.Fatalf("scheduling %s: %v", scenario, err)
			}
			_, readErr := os.Stat(receipt)
			if (readErr == nil) != (scenario == "tainted") {
				t.Fatal("changed scheduling outside the owned tainted node")
			}
		})
	}
}
