// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph_test

import (
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func wliCode(err error) e.Code {
	if coded, ok := err.(*e.Error); ok {
		return coded.Code
	}
	return ""
}

// The DSX IMDS NodeAttestor agent selection lowers exactly two single-platform
// executable outputs onto one pinned root-module source. These checks bind the
// resolved graphs to that contract; they do not execute a producer.
func TestWLIIMDSAgentGraphs(t *testing.T) {
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir("../../../inputs/catalogs/nvidia-workload-identity"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := definition.LoadDefinition(t.Context(), "../../../inputs/definitions/nvidia-workload-identity/imds-agent.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Outputs) != 2 {
		t.Fatalf("definition declares %d outputs, want the two agent executables", len(doc.Outputs))
	}
	const commit = "cd76f1d5ab4a887b9577ff601ce7cda52d5003b4"
	const tree = "b9c85948e417d42d7529d2306f1b80db61c629ae"
	const amd64Image = "docker.io/library/golang@sha256:78773c59d6d4ecedc0845b1a8e5379e7bf0e0be62f4ea796e689bbc97dab3ceb"
	const arm64Image = "docker.io/library/golang@sha256:7117c9e4e2c0becd23d1dbede3aea73ba91d084847c68e12d7e1fa3d9b3abf20"
	for _, output := range doc.Outputs {
		t.Run(output.Name, func(t *testing.T) {
			arch := strings.TrimPrefix(output.Platform, "linux/")
			wantImage := amd64Image
			if arch == "arm64" {
				wantImage = arm64Image
			}
			resolved, err := buildgraph.Resolve(cat, buildgraph.Selection{
				Components: output.Components, Platform: output.Platform, OutputFormat: output.Format,
			})
			if err != nil {
				t.Fatal(err)
			}
			graph, err := buildgraph.Compile(resolved)
			if err != nil {
				t.Fatal(err)
			}
			if graph.Platform != output.Platform || len(graph.Nodes) != 3 {
				t.Fatalf("graph %q has %d nodes, want one fetch, build and verify", graph.Platform, len(graph.Nodes))
			}
			var fetch, build, verify *ir.Node
			for i := range graph.Nodes {
				switch graph.Nodes[i].Kind {
				case ir.KindFetch:
					fetch = &graph.Nodes[i]
				case ir.KindBuild:
					build = &graph.Nodes[i]
				case ir.KindVerify:
					verify = &graph.Nodes[i]
				}
			}
			if fetch == nil || build == nil || verify == nil {
				t.Fatalf("graph kinds are %s/%s/%s", graph.Nodes[0].Kind, graph.Nodes[1].Kind, graph.Nodes[2].Kind)
			}
			if fetch.Repo != "https://github.com/dsx-ai-factory/nvidia-workload-identity" ||
				fetch.Commit != commit || fetch.TreeHash != tree {
				t.Fatalf("fetch node lost the exact v1.15.0 root-source identity: %+v", fetch)
			}
			if fetch.VendorImage != wantImage {
				t.Fatalf("vendor image %q does not match the %s producer toolchain", fetch.VendorImage, arch)
			}
			vendor := strings.Join(fetch.VendorCmd, "\n")
			for _, required := range []string{"go mod download", "go mod verify", "go mod vendor", `test "$before" = "$after"`} {
				if !strings.Contains(vendor, required) {
					t.Fatalf("vendor command omits %q", required)
				}
			}
			if build.Toolchain != wantImage ||
				build.SourceCommit != commit || build.SourceTreeHash != tree || !build.SourceReadOnly {
				t.Fatalf("build node lost its source or toolchain binding: %+v", build)
			}
			if build.OutputType != "host-package" || build.WriteDir != "nvkr-output" ||
				build.ArtifactPath != "nvkr-output/dsx-imds-node-attestor-agent" || build.Version != "1.15.0" {
				t.Fatalf("build node lost its host-package contract: %+v", build)
			}
			command := strings.Join(build.Command, "\n")
			if !strings.Contains(command, "./cmd/dsx-imds-node-attestor-agent") || strings.Contains(command, "dsx-imds-node-attestor-server") {
				t.Fatal("build command does not compile exactly the agent package")
			}
			for _, required := range []string{"GOENV=off", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "-mod=vendor", "-trimpath", "-buildvcs=false"} {
				if !strings.Contains(command, required) {
					t.Fatalf("build command omits the offline flag %q", required)
				}
			}
			env := map[string]string{}
			for _, variable := range build.Env {
				env[variable.Name] = variable.Value
			}
			wantEnv := map[string]string{
				"CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": arch, "GOTOOLCHAIN": "local",
			}
			for name, value := range wantEnv {
				if env[name] != value {
					t.Fatalf("build env %s = %q, want %q", name, env[name], value)
				}
			}
			if build.Needs == nil || len(build.Needs) != 1 || build.Needs[0] != fetch.ID || len(verify.Needs) != 1 || verify.Needs[0] != build.ID {
				t.Fatal("graph dependencies do not chain fetch into build into verify")
			}
		})
	}
}

func TestWLIIMDSAgentGraphRejectsMismatchedSelections(t *testing.T) {
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir("../../../inputs/catalogs/nvidia-workload-identity"))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		selection buildgraph.Selection
		code      e.Code
	}{
		{"undeclared platform", buildgraph.Selection{Components: []string{"dsx-imds-node-attestor-agent"}, Platform: "linux/riscv64", OutputFormat: definition.OutputExecutable}, e.CodeInvalidCatalog},
		{"unknown producer", buildgraph.Selection{Components: []string{"dsx-imds-node-attestor-server"}, Platform: "linux/amd64"}, e.CodeInvalidCatalog},
		{"duplicate producer", buildgraph.Selection{Components: []string{"dsx-imds-node-attestor-agent", "dsx-imds-node-attestor-agent"}, Platform: "linux/amd64", OutputFormat: definition.OutputExecutable}, e.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolved, err := buildgraph.Resolve(cat, test.selection)
			if resolved != nil || wliCode(err) != test.code {
				t.Fatalf("Resolve = %+v, %v; want no result and %s", resolved, err, test.code)
			}
		})
	}
}
