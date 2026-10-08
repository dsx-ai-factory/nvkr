// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// This admits one diagnostic artifact, not image/profile runtime compatibility.
func TestLaptopProfilesArtifactContract(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	path := filepath.Join("definitions", "nke-laptop-arm64", "definition-profiles.json")
	for _, tc := range []struct {
		name, environment string
		foreign           bool
		wantCode          e.Code
	}{
		{"Docker", "environment-local-docker.json", false, ""},
		{"Podman", "environment-local.json", false, ""},
		{"foreign target", "environment-local-docker.json", true, e.CodeNoMatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := definition.LoadDefinition(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			envPath := filepath.Join(filepath.Dir(path), tc.environment)
			env, err := definition.LoadEnvironment(t.Context(), envPath)
			if err != nil {
				t.Fatal(err)
			}
			if tc.foreign {
				doc.Outputs[0].Platform = "linux/amd64"
			}
			plan, err := CompileDocuments(t.Context(), path, doc, envPath, env, "contract-test")
			if tc.wantCode != "" {
				assertFactoryErrorCode(t, err, tc.wantCode)
				if plan != nil {
					t.Fatal("rejected inputs returned a Plan")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := plan.Validate(); err != nil {
				t.Fatal(err)
			}
			if len(plan.Builds) != 1 || len(plan.LifecycleOutputs) != 1 || plan.LifecycleOutputs[0].Name != "controller-profiles" || plan.LifecycleOutputs[0].Kind != "oci" {
				t.Fatal("profiles must remain a separate single-output selection")
			}
			fetches, producers := 0, 0
			for _, node := range plan.Builds[0].Graph.Nodes {
				switch node.Kind {
				case ir.KindPull:
					t.Fatal("profiles must not fall back to an acquired fleet image")
				case ir.KindFetch:
					fetches++
					if node.Commit != "8fb2d53368823bd386b4fbd55b4ab6d3c0931345" || node.TreeHash != "36569cf4ec0e2360ee023cd7e53da65b96ce770d" {
						t.Fatal("profiles must use the staged-installer source")
					}
				case ir.KindBuild:
					producers++
					if node.BuilderType != ir.BuilderBuildKit || node.OutputType != "oci-image" || node.WriteDir != "bin" {
						t.Fatalf("unexpected producer: %+v", node)
					}
					command := strings.Join(node.Command, " ")
					for _, required := range []string{
						"sha256sum -c -", "231af133082e6591e55318b1ad23e9a094dc600833bdc81168f1417bcba9e87a",
						"861964b7d4bbc6d4c82c8e77891cf1cd3ceb344666d344708b54e4492f910d09",
						"sed '98d'", "FROM alpine@sha256:", "nico-vms-cp.yaml", "nico-vms-worker.yaml",
						"nke-local-26-04-arm64-plain", "ubuntu_version: \"26.04\"", "variant: local-plain-tpm",
						"http://172.30.0.1:18080/nke-node-image-arm64.qcow2", "console=ttyAMA0,115200",
						"--opt platform=linux/arm64", "--opt force-network-mode=none", "nke-local-profiles-arm64.oci.tar",
					} {
						if !strings.Contains(command, required) {
							t.Fatalf("missing bound producer instruction %q", required)
						}
					}
					for _, forbidden := range []string{"luks.key=", "set luks-key", "\nmake ", "\ndocker push", "buildctl-daemonless.sh build --frontend dockerfile.v0 --local context=/src/."} {
						if strings.Contains(command, forbidden) {
							t.Fatalf("unexpected build scope %q", forbidden)
						}
					}
				}
			}
			if fetches != 1 || producers != 1 {
				t.Fatalf("fetches=%d producers=%d", fetches, producers)
			}
			for _, node := range plan.Graph.Nodes {
				if node.Kind == "publish" || node.Kind == "sign" {
					t.Fatal("local profiles do not authorize publication or signing")
				}
			}
		})
	}
}
