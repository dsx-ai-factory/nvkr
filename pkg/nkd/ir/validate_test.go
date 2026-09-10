package ir

import (
	"bytes"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
)

func TestGraphAuthorityRejectsUnsafeCallerComposedSemantics(t *testing.T) {
	for name, mutate := range map[string]func(*Graph){
		"tagged toolchain": func(g *Graph) { g.Nodes[0].Toolchain = "ubuntu:latest" },
		"networked build":  func(g *Graph) { g.Nodes[0].Network = NetworkDefault },
		"writable source":  func(g *Graph) { g.Nodes[0].SourceReadOnly = false },
		"escaping output":  func(g *Graph) { g.Nodes[0].ArtifactPath = "../host" },
		"ambient env": func(g *Graph) {
			g.Nodes[0].Env = append(g.Nodes[0].Env, EnvVar{Name: "AWS_SECRET_ACCESS_KEY", Value: "secret"})
			g.Normalize()
		},
		"changed source pin":    func(g *Graph) { g.Nodes[0].SourceCommit = strings.Repeat("f", 40) },
		"uncovered producer":    func(g *Graph) { g.Nodes[2].Needs = nil },
		"forbidden fetch field": func(g *Graph) { g.Nodes[1].Component = "smuggled" },
		"noncanonical order":    func(g *Graph) { g.Nodes[0], g.Nodes[1] = g.Nodes[1], g.Nodes[0] },
	} {
		t.Run(name, func(t *testing.T) {
			graph := sample()
			mutate(&graph)
			if err := graph.Validate(); err == nil {
				t.Fatal("unsafe graph passed semantic validation")
			}
			if _, err := graph.Digest(); err == nil {
				t.Fatal("unsafe graph gained a recipe digest")
			}
			if _, err := graph.CanonicalJSON(); err == nil {
				t.Fatal("unsafe graph gained executable canonical JSON")
			}
			if _, err := graph.MarshalDeterministic(); err == nil {
				t.Fatal("unsafe graph gained deterministic YAML")
			}
		})
	}
}

func TestGraphDigestRemainsTransportIndependentAfterFullValidation(t *testing.T) {
	online := sample()
	onlineDigest, err := online.Digest()
	if err != nil {
		t.Fatal(err)
	}
	offline := sample()
	offline.Nodes[1].Repo = "mirror.local/git/" + offline.Nodes[1].TreeHash
	offline.Nodes[1].VendorImage = "mirror.local/toolchain@sha256:" + strings.Repeat("d", 64)
	offline.Nodes[0].Toolchain = "mirror.local/toolchain@sha256:" + strings.Repeat("d", 64)
	if err := offline.Validate(); err != nil {
		t.Fatalf("relocated full graph must remain executable: %v", err)
	}
	offlineDigest, err := offline.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if onlineDigest != offlineDigest {
		t.Fatalf("transport relocation changed content identity: %s -> %s", onlineDigest, offlineDigest)
	}
	onlineView, err := online.DigestView()
	if err != nil {
		t.Fatal(err)
	}
	offlineView, err := offline.DigestView()
	if err != nil {
		t.Fatal(err)
	}
	onlineIdentityBytes, err := canonical.Marshal(onlineView)
	if err != nil {
		t.Fatal(err)
	}
	offlineIdentityBytes, err := canonical.Marshal(offlineView)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(onlineIdentityBytes, offlineIdentityBytes) {
		t.Fatalf("transport relocation changed explicit logical graph bytes:\n online %s\noffline %s",
			onlineIdentityBytes, offlineIdentityBytes)
	}
	onlineBytes, _ := online.CanonicalJSON()
	offlineBytes, _ := offline.CanonicalJSON()
	if string(onlineBytes) == string(offlineBytes) {
		t.Fatal("full execution graph failed to retain relocated transport bindings")
	}
}

func TestDigestViewRejectsUnsafeFullGraphBeforeProjection(t *testing.T) {
	graph := sample()
	graph.Nodes[0].Toolchain = "ubuntu:latest"
	if _, err := graph.DigestView(); err == nil {
		t.Fatal("unsafe full graph reached the logical identity projection")
	}
}

func TestGraphValidateRejectsDuplicateDependenciesAndEnvironment(t *testing.T) {
	graph := sample()
	graph.Nodes[2].Needs = []string{"build-kubelet", "build-kubelet"}
	if err := graph.Validate(); err == nil {
		t.Fatal("duplicate terminal dependency passed validation")
	}
	graph = sample()
	graph.Nodes[0].Env = append(graph.Nodes[0].Env, graph.Nodes[0].Env[len(graph.Nodes[0].Env)-1])
	if err := graph.Validate(); err == nil {
		t.Fatal("duplicate build environment passed validation")
	}
}
