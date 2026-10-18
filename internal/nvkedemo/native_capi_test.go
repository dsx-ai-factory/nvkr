// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
)

func TestNativeCAPIAdmitsOnlyReviewedSourcePolicy(t *testing.T) {
	const source = "69ef9924c4bf0487beba8c3613ae6e0a0260d8f3\n"
	const digest = "ae8980d7b31d9f2f9c355dd7d873854eaba8cfe94f5075ec61dfa9bdbdb5a0f9"
	for _, tc := range []struct {
		name, commit, digest string
		accepted             bool
	}{
		{"reviewed", source, digest, true},
		{"historical source", "b6154b292a934dbdff7aa17596cbf2d7ef5f0f85\n", digest, false},
		{"missing commit", "", digest, false},
		{"changed commit", strings.Repeat("a", 40) + "\n", digest, false},
		{"ambiguous commit", source + source, digest, false},
		{"noncanonical marker", strings.TrimSpace(source), digest, false},
		{"missing versions", source, "", false},
		{"changed versions", source, strings.Repeat("a", 64), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			version, err := reviewedNativeCAPIProvider(tc.commit, tc.digest)
			if tc.accepted {
				if err != nil || version != "v1.13.6" {
					t.Fatalf("selected provider=%q error=%v", version, err)
				}
				return
			}
			if err == nil || version != "" {
				t.Fatalf("unreviewed source granted provider=%q error=%v", version, err)
			}
		})
	}
}

func TestNativeCAPISourceFailurePrecedesCommandsAndOutput(t *testing.T) {
	for _, name := range []string{"missing commit", "oversize commit", "missing versions", "changed versions", "oversize versions", "symlink versions", "directory versions", "canceled"} {
		t.Run(name, func(t *testing.T) {
			installer, out := t.TempDir(), t.TempDir()
			commit := []byte("69ef9924c4bf0487beba8c3613ae6e0a0260d8f3\n")
			if name == "oversize commit" {
				commit = append(commit, '\n')
			}
			if name != "missing commit" {
				if err := os.WriteFile(filepath.Join(installer, "SOURCE_COMMIT"), commit, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Mkdir(filepath.Join(installer, "make"), 0o700); err != nil {
				t.Fatal(err)
			}
			versionsPath := filepath.Join(installer, "make", "tool-versions.mk")
			switch name {
			case "missing versions":
			case "symlink versions":
				if err := os.Symlink(filepath.Join(installer, "SOURCE_COMMIT"), versionsPath); err != nil {
					t.Fatal(err)
				}
			case "directory versions":
				if err := os.Mkdir(versionsPath, 0o700); err != nil {
					t.Fatal(err)
				}
			default:
				raw := []byte("CAPI_VERSION ?= v1.99.0\n")
				if name == "oversize versions" {
					raw = make([]byte, (64<<10)+1)
				}
				if err := os.WriteFile(versionsPath, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if name == "canceled" {
				cancel()
			}
			attempt := nativeSiteAttempt{assets: NativeAssets{Installer: installer}}
			err := attempt.deployFromHost(ctx, Runner{Work: out, Env: NativeEnvironment()}, "/absent/nvkectl", "/absent/tools", out, "/absent/sitespec")
			if err == nil {
				t.Fatal("source failure reached deployment")
			}
			entries, readErr := os.ReadDir(out)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("source failure produced command logs or deployment output: %v; %v", entries, readErr)
			}
		})
	}
}

func TestNativeCAPIInitSelectsAllThreeProvidersAndWaits(t *testing.T) {
	version, err := reviewedNativeCAPIProvider("69ef9924c4bf0487beba8c3613ae6e0a0260d8f3\n", "ae8980d7b31d9f2f9c355dd7d873854eaba8cfe94f5075ec61dfa9bdbdb5a0f9")
	if err != nil {
		t.Fatal(err)
	}
	got := nativeCAPIInitArgs("/selected tools", "/private site.kubeconfig", version)
	want := []string{"/selected tools/clusterctl", "init", "--kubeconfig", "/private site.kubeconfig",
		"--core", "cluster-api:v1.13.6", "--bootstrap", "kubeadm:v1.13.6", "--control-plane", "kubeadm:v1.13.6", "--wait-providers"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unmatched source provider selection: got %q want %q", got, want)
	}
}

func TestNativeHostClusterctlMatchesAuthenticatedCAPIRelease(t *testing.T) {
	versions, err := os.ReadFile("testdata/native-b615-tool-versions.mk")
	if err != nil {
		t.Fatal(err)
	}
	version, err := reviewedNativeCAPIProvider("69ef9924c4bf0487beba8c3613ae6e0a0260d8f3\n", plainSHA(versions))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join("..", "..", "inputs", "catalogs", "tools", "native")
	c, err := catalog.LoadContext(t.Context(), catalog.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"clusterctl", "clusterctl-linux"} {
		if c.Components[id].Tag != version || !strings.Contains(c.Tools[id].URL, "/"+version+"/") {
			t.Fatalf("%s differs from authenticated CAPI %s", id, version)
		}
	}
}
