package ir

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// These vectors were calculated independently using integer-only sorted JSON and
// SHA-256 over UTF8("nkd.build") + NUL + the exact logical bytes.
func TestBuildIdentityCanonicalVectors(t *testing.T) {
	for _, test := range []struct {
		name            string
		graph           Graph
		logical, digest string
	}{
		{"bound-image", Graph{SchemaVersion: SchemaVersion, Platform: "linux/amd64", Nodes: []Node{
			{ID: "pull-tool", Kind: KindPull, Component: "tool", Artifact: "registry.example/tool@sha256:" + strings.Repeat("b", 64),
				Digest: "sha256:" + strings.Repeat("a", 64), ImageParentDigest: "sha256:" + strings.Repeat("b", 64),
				Platform: "linux/amd64", Network: NetworkDefault, OutputType: "oci-image", Version: "1.0.0"},
			{ID: "verify", Kind: KindVerify, Needs: []string{"pull-tool"}},
		}}, `{"nodes":[{"component":"tool","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","id":"pull-tool","imageParentDigest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","kind":"pull","network":"default","outputType":"oci-image","platform":"linux/amd64","version":"1.0.0"},{"id":"verify","kind":"verify","needs":["pull-tool"]}],"platform":"linux/amd64","schemaVersion":"nkd.dev/ir/v4"}`, "sha256:9ab2019f619f98c660a3997d2cc5f1e9a68f0968af425093462ce5c5dc736ddc"},
		{"acquisition", pullGraph(), `{"nodes":[{"artifactPath":"tool","component":"tool","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","id":"pull-tool","kind":"pull","network":"default","outputType":"raw","platform":"linux/amd64","version":"1.0.0"},{"id":"verify","kind":"verify","needs":["pull-tool"]}],"platform":"linux/amd64","schemaVersion":"nkd.dev/ir/v4"}`, "sha256:0de705e964c9f4a8fa9fbfcad89fe201e3555a21bb05a8c7c717d2f5dabe85ab"},
		{"source", sample(), `{"nodes":[{"artifactPath":"out/kubelet","builderType":"oci-hermetic","command":["make"],"component":"kubelet","env":[{"name":"CGO_ENABLED","value":"0"},{"name":"GOAMD64","value":"v1"},{"name":"GOARCH","value":"amd64"},{"name":"GOFLAGS","value":"-trimpath -buildvcs=false"},{"name":"GOOS","value":"linux"},{"name":"GOTOOLCHAIN","value":"local"},{"name":"LC_ALL","value":"C"},{"name":"SOURCE_DATE_EPOCH","value":"1"},{"name":"TZ","value":"UTC"}],"id":"build-kubelet","kind":"build","needs":["fetch-kubernetes-abc"],"network":"none","outputType":"host-package","platform":"linux/amd64","sourceCommit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","sourceDest":"kubernetes-aaaaaaaaaaaa","sourceReadOnly":true,"sourceTreeHash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","toolchain":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","vendorSpecDigest":"sha256:c2dd0219416b6812e02aaea22f467499ac3d7ae99ae2532f6f1c909262798b64","version":"1.32.0","workdir":"/src","writeDir":"out"},{"commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","dest":"kubernetes-aaaaaaaaaaaa","id":"fetch-kubernetes-abc","kind":"fetch","network":"default","treeHash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","vendorImage":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","vendorSpecDigest":"sha256:c2dd0219416b6812e02aaea22f467499ac3d7ae99ae2532f6f1c909262798b64"},{"id":"verify","kind":"verify","needs":["build-kubelet"]}],"platform":"linux/amd64","schemaVersion":"nkd.dev/ir/v4"}`, "sha256:abd54ac28ccf60630ec215710569782b63ec01b2209d805ad76b960506ebd9f5"},
		{"vm-image", vmGraph(t), `{"nodes":[{"artifactPath":"node.qcow2","builderType":"vm-image","component":"image","disk":{"encryption":"none","format":"qcow2"},"id":"build-image","kind":"build","materials":[{"binding":"agent-package","component":"agent","producer":"pull-agent"}],"needs":["fetch-image","pull-agent"],"network":"none","outputType":"disk-image","packer":{"acceleratorVariable":"accelerator","architectureVariable":"architecture","buildName":"image.qemu.node","materialManifestVariable":"material_manifest","outputDirectoryVariable":"output_directory","packerVersion":"1.14.2","projectPath":"os-image/packer","variables":[{"name":"image_variant","value":"standard"}]},"platform":"linux/arm64","sourceCommit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","sourceDest":"source-image","sourceTreeHash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","toolchain":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","toolchainPlatform":"linux/amd64","vendorSpecDigest":"sha256:2bef7aee55536ae48fb6fb1bab0f38530ffd04cda898a538d0d2156a9fde037e","version":"1.0.0","workdir":"/src"},{"commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","dest":"source-image","id":"fetch-image","kind":"fetch","network":"default","treeHash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","vendorImage":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","vendorSpecDigest":"sha256:2bef7aee55536ae48fb6fb1bab0f38530ffd04cda898a538d0d2156a9fde037e"},{"artifactPath":"agent","component":"agent","digest":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","id":"pull-agent","kind":"pull","network":"default","outputType":"raw","platform":"linux/arm64","version":"1.0.0"},{"id":"verify","kind":"verify","needs":["build-image","pull-agent"]}],"platform":"linux/arm64","schemaVersion":"nkd.dev/ir/v4"}`, "sha256:13e2c2189065e9f3715cb664babab8b7b875e1926f117cc63af31845aee173eb"},
	} {
		t.Run(test.name, func(t *testing.T) {
			logical, err := test.graph.DigestView()
			if err != nil {
				t.Fatal(err)
			}
			raw, err := canonical.Marshal(logical)
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != test.logical {
				t.Fatalf("logical bytes = %s, want %s", raw, test.logical)
			}
			got, err := test.graph.Digest()
			if err != nil {
				t.Fatal(err)
			}
			if got != test.digest {
				t.Fatalf("build digest = %s, want %s", got, test.digest)
			}
			// Independent historical-prefix reference only. The production codec
			// must never mint a new object under the retired family.
			historical := sha256.Sum256([]byte("nkd.recipe\x00" + test.logical))
			if fmt.Sprintf("sha256:%x", historical) == got {
				t.Fatal("new build identity reused the retired recipe domain")
			}
			retired, err := canonical.Digest(canonical.KindLegacyRecipe, logical)
			assertIRCode(t, err, e.CodeUnsupported)
			if retired != "" {
				t.Fatal("retired recipe domain minted an identity")
			}
		})
	}
}

func pullGraph() Graph {
	return Graph{SchemaVersion: SchemaVersion, Platform: "linux/amd64", Nodes: []Node{
		validRawPullNode(), {ID: "verify", Kind: KindVerify, Needs: []string{"pull-tool"}},
	}}
}

func TestBuildGraphRequiresExactTargetAndProducerKind(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Graph)
		code   e.Code
	}{
		{"old schema", func(g *Graph) { g.SchemaVersion = "nkd.dev/ir/v1" }, e.CodeUnsupported},
		{"former direct-selection schema", func(g *Graph) { g.SchemaVersion = "nkd.dev/ir/v2" }, e.CodeUnsupported},
		{"missing platform", func(g *Graph) { g.Platform = "" }, e.CodeInvalidCatalog},
		{"wildcard platform", func(g *Graph) { g.Platform = "linux/any" }, e.CodeInvalidCatalog},
		{"different producer platform", func(g *Graph) { g.Platform = "linux/arm64" }, e.CodeInvalidCatalog},
		{"empty ordinary build", func(g *Graph) { g.Nodes = []Node{{ID: "verify", Kind: KindVerify}} }, e.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			graph := pullGraph()
			test.mutate(&graph)
			assertIRCode(t, graph.Validate(), test.code)
			_, err := graph.Digest()
			assertIRCode(t, err, test.code)
			_, err = graph.CanonicalJSON()
			assertIRCode(t, err, test.code)
		})
	}
}

func TestBuildGraphRejectsRemovedAuthoringFields(t *testing.T) {
	graph := pullGraph()
	raw, err := graph.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"recipeId":"removed",`, `"criteria":{"arch":"amd64"},`, `"category":"os",`, `"outputName":"label",`} {
		t.Run(field, func(t *testing.T) {
			changed := []byte("{" + field + string(raw[1:]))
			var decoded Graph
			assertIRCode(t, canonical.UnmarshalExact(changed, &decoded), e.CodeVerifyFailed)
		})
	}
}

func TestSelectedProducerInputsAffectBuildIdentity(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Graph)
	}{
		{"command", func(g *Graph) { g.Nodes[0].Command = []string{"make", "all"} }},
		{"toolchain pin", func(g *Graph) { g.Nodes[0].Toolchain = "golang@sha256:" + strings.Repeat("e", 64) }},
		{"source tree", func(g *Graph) {
			g.Nodes[1].TreeHash = strings.Repeat("c", 40)
			g.Nodes[0].SourceTreeHash = g.Nodes[1].TreeHash
		}},
		{"builder", func(g *Graph) {
			g.Nodes[0].BuilderType = BuilderNix
			g.Nodes[0].FlakeRef = "github:example/locked"
			g.Nodes[0].FlakeOutput = "package"
			g.Nodes[0].FlakeLockDigest = "sha256:" + strings.Repeat("e", 64)
		}},
		{"membership", func(g *Graph) {
			g.Nodes = append(g.Nodes, validRawPullNode())
			g.Nodes[2].Needs = append(g.Nodes[2].Needs, "pull-tool")
			g.Normalize()
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := sample()
			want, err := base.Digest()
			if err != nil {
				t.Fatal(err)
			}
			changed := sample()
			test.mutate(&changed)
			got, err := changed.Digest()
			if err != nil {
				t.Fatal(err)
			}
			if got == want {
				t.Fatal("changed selected producer retained build identity")
			}
		})
	}
	for _, mutate := range []func(*Graph){
		func(g *Graph) { g.Nodes[0].Digest = "sha256:" + strings.Repeat("b", 64) },
		func(g *Graph) { g.Platform = "linux/arm64"; g.Nodes[0].Platform = g.Platform },
	} {
		base := pullGraph()
		want, err := base.Digest()
		if err != nil {
			t.Fatal(err)
		}
		changed := pullGraph()
		mutate(&changed)
		got, err := changed.Digest()
		if err != nil {
			t.Fatal(err)
		}
		if got == want {
			t.Fatal("changed acquisition pin or target retained build identity")
		}
	}
}
