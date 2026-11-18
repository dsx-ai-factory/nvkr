// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestGitHubSourceSeedsRejectUnknownPlatforms(t *testing.T) {
	for _, platform := range []string{"", "darwin/arm64", "linux/unknown"} {
		t.Run(platform, func(t *testing.T) {
			node := ir.Node{ID: "fetch", Kind: ir.KindFetch, Repo: "https://example.invalid/source", Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40)}
			plan := &Plan{Builds: []BuildPlan{{Name: "a", Graph: &ir.Graph{Platform: platform, Nodes: []ir.Node{node}}}}}
			_, err := githubSourceSeeds(plan, []Node{{ID: "a/fetch", Kind: NodeFetch, BuildTarget: "a", IRNode: "fetch"}})
			if factoryErrorCode(err) != e.CodeEngineCapability {
				t.Fatalf("platform %q: %v", platform, err)
			}
		})
	}
}

func TestGitHubSourceSeedsRequireExactPins(t *testing.T) {
	for _, scenario := range []string{"same closure", "different vendor", "different repository", "different commit", "different tree"} {
		t.Run(scenario, func(t *testing.T) {
			first := ir.Node{ID: "fetch", Kind: ir.KindFetch, Repo: "https://example.invalid/source", Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40)}
			second := first
			want := true
			switch scenario {
			case "different vendor":
				second.VendorCmd = []string{"different-vendor"}
				second.VendorImage = "registry.example/vendor@sha256:" + strings.Repeat("c", 64)
			case "different repository":
				second.Repo, want = "https://example.invalid/other", false
			case "different commit":
				second.Commit, want = strings.Repeat("c", 40), false
			case "different tree":
				second.TreeHash, want = strings.Repeat("c", 40), false
			}
			plan := &Plan{Builds: []BuildPlan{{Name: "a", Graph: &ir.Graph{Platform: "linux/amd64", Nodes: []ir.Node{first}}}, {Name: "b", Graph: &ir.Graph{Platform: "linux/amd64", Nodes: []ir.Node{second}}}}}
			nodes := []Node{{ID: "a/fetch", Kind: NodeFetch, BuildTarget: "a", IRNode: "fetch"}, {ID: "b/fetch", Kind: NodeFetch, BuildTarget: "b", IRNode: "fetch"}}
			seeds, err := githubSourceSeeds(plan, nodes)
			if err != nil {
				t.Fatal(err)
			}
			seed, shared := seeds["b/fetch"]
			if shared != want || shared && seed.ID != "a/fetch" {
				t.Fatalf("seed = %+v, shared = %t, want %t", seed, shared, want)
			}
		})
	}
}

func TestGitHubSourceSeedsSelectNativePreparedOwner(t *testing.T) {
	for _, platforms := range [][]string{{"linux/amd64", "linux/arm64", "linux/arm64"}, {"linux/arm64", "linux/amd64", "linux/amd64"}} {
		t.Run(platforms[0], func(t *testing.T) {
			instruction := ir.Node{ID: "fetch", Kind: ir.KindFetch, Repo: "https://example.invalid/source",
				Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40),
				VendorCmd: []string{"vendor"}, VendorImage: "registry.example/vendor@sha256:" + strings.Repeat("c", 64)}
			plan := &Plan{}
			var nodes []Node
			for i, name := range []string{"a", "b", "c"} {
				plan.Builds = append(plan.Builds, BuildPlan{Name: name, Graph: &ir.Graph{Platform: platforms[i], Nodes: []ir.Node{instruction}}})
				nodes = append(nodes, Node{ID: name + "/fetch", Kind: NodeFetch, BuildTarget: name, IRNode: "fetch"})
			}
			seeds, err := githubSourceSeeds(plan, nodes)
			if err != nil {
				t.Fatal(err)
			}
			if len(seeds) != 2 || seeds["b/fetch"].ID != "a/fetch" || seeds["c/fetch"].ID != "b/fetch" {
				t.Fatalf("native prepared owner selection = %+v", seeds)
			}
		})
	}
}

func TestGitHubSharedSourcesKeepConsumerAdmission(t *testing.T) {
	plan, err := LoadPlanContext(t.Context(), "../../../.github/nvkr-dev/plan.json")
	if err != nil {
		t.Fatal(err)
	}
	before, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	ordered, _, err := githubSchedulingProjection(plan)
	if err != nil {
		t.Fatal(err)
	}
	seeds, err := githubSourceSeeds(plan, ordered)
	if err != nil {
		t.Fatal(err)
	}
	stages, jobs, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	placed := map[string]Node{}
	for _, stage := range stages {
		for _, node := range stage.Nodes {
			placed[node.ID] = node
		}
	}
	_, artifacts := planIndex(plan)
	seedOwners := map[string]bool{}
	for consumer, seed := range seeds {
		if jobs[consumer] != jobs[seed.ID] {
			seedOwners[seed.ID] = true
		}
	}
	carriers := 0
	for _, artifact := range artifacts {
		if artifact.Kind != "source-closure" {
			continue
		}
		wantCarrier := seedOwners[artifact.Producer]
		for _, consumer := range plan.Graph.Nodes {
			wantCarrier = wantCarrier || slices.Contains(consumer.Inputs, artifact.ID) && jobs[consumer.ID] != jobs[artifact.Producer]
		}
		got := githubArtifactNeedsCarrier(plan, artifact, jobs)
		if got != wantCarrier {
			t.Fatalf("source %s carrier=%t, want %t", artifact.ID, got, wantCarrier)
		}
		if got {
			carriers++
		}
	}
	type pin struct{ repo, commit, tree string }
	networkOwners := map[pin]int{}
	closureOwners := map[struct {
		pin
		vendor   string
		platform string
	}]int{}
	fetches := 0
	for _, node := range ordered {
		if node.Kind != NodeFetch || !irProjectedNode(node) {
			continue
		}
		fetches++
		_, instruction, err := plan.producerInstruction(node)
		if err != nil {
			t.Fatal(err)
		}
		key := pin{instruction.Repo, instruction.Commit, instruction.TreeHash}
		vendor, err := ir.VendorSpecIdentity(instruction.VendorCmd, instruction.VendorImage)
		if err != nil {
			t.Fatal(err)
		}
		execution, err := plan.GitHubExecutionFor(node)
		if err != nil {
			t.Fatal(err)
		}
		seed, shared := seeds[node.ID]
		if !shared {
			networkOwners[key]++
		} else {
			_, source, err := plan.producerInstruction(seed)
			if err != nil {
				t.Fatal(err)
			}
			if source.Repo != instruction.Repo || source.Commit != instruction.Commit || source.TreeHash != instruction.TreeHash {
				t.Fatal("source pin crossed a sharing boundary")
			}
			for _, output := range seed.Outputs {
				if !slices.Contains(placed[node.ID].Inputs, output) || jobs[node.ID] != jobs[seed.ID] && !githubArtifactNeedsCarrier(plan, artifacts[output], jobs) {
					t.Fatalf("consumer %s lost source seed carrier %s", node.ID, output)
				}
			}
			seedVendor, err := ir.VendorSpecIdentity(source.VendorCmd, source.VendorImage)
			if err != nil {
				t.Fatal(err)
			}
			seedExecution, err := plan.GitHubExecutionFor(seed)
			if err != nil {
				t.Fatal(err)
			}
			if seedVendor == vendor && seedExecution.Platform == execution.Platform {
				continue
			}
		}
		closureOwners[struct {
			pin
			vendor   string
			platform string
		}{key, vendor, execution.Platform}]++
	}
	for key, count := range networkOwners {
		if count != 1 {
			t.Fatalf("%+v has %d network owners", key, count)
		}
	}
	for key, count := range closureOwners {
		if count != 1 {
			t.Fatalf("%+v has %d vendor owners", key, count)
		}
	}
	if len(networkOwners) >= fetches || len(closureOwners) >= fetches {
		t.Fatal("fixture does not share source and vendor work")
	}
	// The maintained GPU-site Plan at b33c8d0e projects 197 operation jobs.
	// Source sharing must not erase the publication setup reduction by expanding
	// each multi-source consumer into one provider job per fetch.
	if len(stages) >= 197 || carriers >= fetches {
		t.Fatalf("sharing regressed maintained scheduling: %d jobs, %d source carriers for %d fetches", len(stages), carriers, fetches)
	}
	after, err := canonical.Marshal(plan)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("shared source projection changed Plan bytes: %v", err)
	}
	t.Logf("%d logical fetches, %d network owners, %d vendor closure owners", fetches, len(networkOwners), len(closureOwners))
	t.Logf("%d operation jobs, %d source-closure carriers", len(stages), carriers)
}

func TestGitHubSeededFetchesShareSourceStage(t *testing.T) {
	for _, scenario := range []string{"seeded joins root", "root joins seeded", "cycle", "different target", "different runner", "owner cannot join producer", "producer cannot join owner"} {
		t.Run(scenario, func(t *testing.T) {
			first := Node{ID: "a/fetch-one", Kind: NodeFetch, BuildTarget: "a", RunsOn: "ubuntu-latest"}
			next := Node{ID: "a/fetch-two", Kind: NodeFetch, BuildTarget: "a", RunsOn: "ubuntu-latest", Inputs: []string{"shared"}}
			grouping := authorityStages{plan: &Plan{}, artifacts: map[string]Artifact{"shared": {Producer: "seed"}}, needs: map[int]map[int]bool{}, sourceSeedOwners: map[string]bool{}}
			owners := map[string]int{"seed": 1}
			want := true
			switch scenario {
			case "root joins seeded":
				first.Inputs, next.Inputs = next.Inputs, nil
				grouping.needs[0] = map[int]bool{1: true}
			case "cycle":
				grouping.needs[1], want = map[int]bool{0: true}, false
			case "different target":
				next.BuildTarget, want = "b", false
			case "different runner":
				next.RunsOn, want = "ubuntu-24.04-arm", false
			case "producer cannot join owner":
				next.Kind, want = NodeBuild, false
				grouping.sourceSeedOwners[first.ID] = true
			case "owner cannot join producer":
				first.Kind, want = NodeAcquire, false
				grouping.sourceSeedOwners[next.ID] = true
			}
			_, got := grouping.joinSourceFetches(next, []githubStage{{Nodes: []Node{first}}}, owners)
			if got != want {
				t.Fatalf("joined=%t, want %t", got, want)
			}
			if (scenario == "owner cannot join producer" || scenario == "producer cannot join owner") && grouping.admits(githubStage{Nodes: []Node{first}}, next) {
				t.Fatal("source owner joined non-source work")
			}
		})
	}
}
