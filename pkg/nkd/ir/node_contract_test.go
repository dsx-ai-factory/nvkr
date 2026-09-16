package ir

import (
	"errors"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
)

func TestBuildNodeRejectsInvalidExecutionFields(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Node)
		code   e.Code
	}{
		{"wrong component identity", func(n *Node) { n.Component = "other" }, e.CodeInvalidCatalog},
		{"no source dependency", func(n *Node) { n.Needs = nil }, e.CodeInvalidCatalog},
		{"unsupported platform", func(n *Node) { n.Platform = "windows/amd64" }, e.CodeInvalidCatalog},
		{"empty command", func(n *Node) { n.Command = nil }, e.CodeInvalidCatalog},
		{"command NUL", func(n *Node) { n.Command = []string{"make", "bad\x00arg"} }, e.CodeInvalidCatalog},
		{"workdir escape", func(n *Node) { n.Workdir = "/tmp" }, e.CodeInvalidCatalog},
		{"artifact outside private output", func(n *Node) { n.ArtifactPath = "other/file" }, e.CodeInvalidCatalog},
		{"missing commit", func(n *Node) { n.SourceCommit = "" }, e.CodeUnpinnedSource},
		{"unknown output", func(n *Node) { n.OutputType = "unknown" }, e.CodeInvalidCatalog},
		{"invalid version", func(n *Node) { n.Version = " " }, e.CodeInvalidCatalog},
		{"unknown builder", func(n *Node) { n.BuilderType = "unknown" }, e.CodeInvalidCatalog},
		{"OCI with flake", func(n *Node) { n.FlakeRef = "github:example/flake" }, e.CodeInvalidCatalog},
		{"unpinned Nix", func(n *Node) { n.BuilderType = BuilderNix }, e.CodeUnpinnedSource},
		{"invalid patch path", func(n *Node) { n.Patches = []PatchRef{{Name: "../escape"}} }, e.CodeInvalidCatalog},
		{"missing patch pin", func(n *Node) { n.Patches = []PatchRef{{Name: "fix.patch"}} }, e.CodeUnpinnedSource},
		{"changed patch bytes", func(n *Node) {
			n.Patches = []PatchRef{{Name: "fix.patch", SHA256: "sha256:" + strings.Repeat("a", 64), Content: "different"}}
		}, e.CodeUnpinnedSource},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := sample().Nodes[0]
			tc.change(&n)
			assertIRCode(t, n.Validate(), tc.code)
		})
	}
}

func TestExecutionRequirementsAreAdmittedOnlyOnBuildNodes(t *testing.T) {
	requirements := &execution.Requirements{Resources: &execution.ResourceRequest{MemoryMiB: 4096}}
	for _, index := range []int{1, 2} {
		node := sample().Nodes[index]
		node.Requirements = execution.Clone(requirements)
		if err := node.Validate(); err == nil || !strings.Contains(err.Error(), "forbidden field requirements") {
			t.Fatalf("%s node accepted an unprojectable execution requirement: %v", node.Kind, err)
		}
	}

	build := sample().Nodes[0]
	build.Requirements = execution.Clone(requirements)
	if err := build.Validate(); err != nil {
		t.Fatalf("build node rejected generic execution requirements: %v", err)
	}
}

func TestBuildEnvironmentRejectsDriftFromPinnedExecution(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		code        e.Code
	}{
		{"TZ", "local", e.CodeInvalidCatalog}, {"GOARCH", "arm64", e.CodeInvalidCatalog},
		{"CGO_ENABLED", "2", e.CodeInvalidCatalog}, {"SOURCE_DATE_EPOCH", "0", e.CodeUnpinnedSource},
		{"SOURCE_DATE_EPOCH", "01", e.CodeUnpinnedSource}, {"GOAMD64", "v3", e.CodeInvalidCatalog},
		{"CC", " ", e.CodeInvalidCatalog}, {"CC", "bad\x00value", e.CodeInvalidCatalog},
	} {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			g := sample()
			n := &g.Nodes[0]
			found := false
			for i := range n.Env {
				if n.Env[i].Name == tc.name {
					n.Env[i].Value = tc.value
					found = true
				}
			}
			if !found {
				n.Env = append(n.Env, EnvVar{Name: tc.name, Value: tc.value})
			}
			g.Normalize()
			assertIRCode(t, n.Validate(), tc.code)
		})
	}
}

func TestPullNodeRequiresExactAcquisitionShape(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Node)
		code   e.Code
	}{
		{"raw", nil, ""},
		{"unknown platform", func(n *Node) { n.Platform = "linux/riscv64" }, e.CodeInvalidCatalog},
		{"network disabled", func(n *Node) { n.Network = NetworkNone }, e.CodeInvalidCatalog},
		{"missing digest", func(n *Node) { n.Digest = "" }, e.CodeUnpinnedSource},
		{"missing version", func(n *Node) { n.Version = "" }, e.CodeInvalidCatalog},
		{"wrong node id", func(n *Node) { n.ID = "pull-other" }, e.CodeInvalidCatalog},
		{"has dependency", func(n *Node) { n.Needs = []string{"fetch-source"} }, e.CodeInvalidCatalog},
		{"unknown output type", func(n *Node) { n.OutputType = "disk-image" }, e.CodeInvalidCatalog},
		{"raw with environment", func(n *Node) { n.Env = []EnvVar{{Name: "SOURCE_DATE_EPOCH", Value: "1"}} }, e.CodeInvalidCatalog},
		{"OCI with file path", func(n *Node) { n.OutputType = "oci-image" }, e.CodeInvalidCatalog},
		{"host package missing epoch", func(n *Node) { n.OutputType = "host-package" }, e.CodeUnpinnedSource},
		{"host package escaping path", func(n *Node) { n.OutputType = "host-package"; n.ArtifactPath = "../escape" }, e.CodeInvalidCatalog},
		{"host package forbidden environment", func(n *Node) { n.OutputType = "host-package"; n.Env = []EnvVar{{Name: "HOME", Value: "/tmp"}} }, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := Node{ID: "pull-tool", Kind: KindPull, Component: "tool", Artifact: "https://example.invalid/tool", Digest: "sha256:" + strings.Repeat("a", 64), Platform: "linux/amd64", Network: NetworkDefault, Version: "1.0.0", OutputType: "raw", ArtifactPath: "tool"}
			if tc.change != nil {
				tc.change(&n)
			}
			err := n.Validate()
			if tc.code != "" {
				assertIRCode(t, err, tc.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func assertIRCode(t *testing.T, err error, want e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != want {
		t.Fatalf("error = %v, want %s", err, want)
	}
}
