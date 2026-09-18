package ir

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/aicr"
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
		{"acquisition", pullGraph(), `{"nodes":[{"artifactPath":"tool","component":"tool","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","id":"pull-tool","kind":"pull","network":"default","outputType":"raw","platform":"linux/amd64","version":"1.0.0"},{"id":"verify","kind":"verify","needs":["pull-tool"]}],"platform":"linux/amd64","schemaVersion":"nkd.dev/ir/v3"}`, "sha256:ea1255c812ec755a277af977e0f20fb8cc85ea6bf354fec1f85cf6d12255e22f"},
		{"source", sample(), `{"nodes":[{"artifactPath":"out/kubelet","builderType":"oci-hermetic","command":["make"],"component":"kubelet","env":[{"name":"CGO_ENABLED","value":"0"},{"name":"GOAMD64","value":"v1"},{"name":"GOARCH","value":"amd64"},{"name":"GOFLAGS","value":"-trimpath -buildvcs=false"},{"name":"GOOS","value":"linux"},{"name":"GOTOOLCHAIN","value":"local"},{"name":"LC_ALL","value":"C"},{"name":"SOURCE_DATE_EPOCH","value":"1"},{"name":"TZ","value":"UTC"}],"id":"build-kubelet","kind":"build","needs":["fetch-kubernetes-abc"],"network":"none","outputType":"host-package","platform":"linux/amd64","sourceCommit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","sourceDest":"kubernetes-aaaaaaaaaaaa","sourceReadOnly":true,"sourceTreeHash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","toolchain":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","vendorSpecDigest":"sha256:c2dd0219416b6812e02aaea22f467499ac3d7ae99ae2532f6f1c909262798b64","version":"1.32.0","workdir":"/src","writeDir":"out"},{"commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","dest":"kubernetes-aaaaaaaaaaaa","id":"fetch-kubernetes-abc","kind":"fetch","network":"default","treeHash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","vendorImage":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","vendorSpecDigest":"sha256:c2dd0219416b6812e02aaea22f467499ac3d7ae99ae2532f6f1c909262798b64"},{"id":"verify","kind":"verify","needs":["build-kubelet"]}],"platform":"linux/amd64","schemaVersion":"nkd.dev/ir/v3"}`, "sha256:d0c6c4d1ac8c4b5a71df00128b5b5546c4a0e6f5f6c6a8e7f274496de1401c9f"},
		{"aicr", aicrGraph(), `{"aicr":{"accelerator":"h100","bundle":{"deployer":"helm"},"generatorDigest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","intent":"training","os":"ubuntu","profileDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","service":"eks"},"nodes":[{"id":"verify","kind":"verify"}],"platform":"linux/amd64","schemaVersion":"nkd.dev/ir/v3"}`, "sha256:e067c32e7f41bd46583f45016aaa2017dec66531df39a652a76d05290d3e10e7"},
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

func aicrGraph() Graph {
	return Graph{SchemaVersion: SchemaVersion, Platform: "linux/amd64", AICR: &AICRActivation{
		Service: "eks", Accelerator: "h100", OS: "ubuntu", Intent: "training",
		Bundle:        aicr.BundleInputs{Deployer: "helm"},
		ProfileDigest: "sha256:" + strings.Repeat("a", 64), GeneratorDigest: "sha256:" + strings.Repeat("b", 64),
	}, Nodes: []Node{{ID: "verify", Kind: KindVerify}}}
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
		{"mixed AICR and ordinary producer", func(g *Graph) { g.AICR = aicrGraph().AICR }, e.CodeInvalidCatalog},
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

func TestTargetAndAICRInputsAffectBuildIdentity(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Graph)
	}{
		{"target", func(g *Graph) { g.Platform = "linux/arm64" }},
		{"service", func(g *Graph) { g.AICR.Service = "gke" }},
		{"accelerator", func(g *Graph) { g.AICR.Accelerator = "h200" }},
		{"os", func(g *Graph) { g.AICR.OS = "rhel" }},
		{"intent", func(g *Graph) { g.AICR.Intent = "inference" }},
		{"platform", func(g *Graph) { g.AICR.Platform = "dynamo" }},
		{"profile pin", func(g *Graph) { g.AICR.ProfileDigest = "sha256:" + strings.Repeat("c", 64) }},
		{"generator pin", func(g *Graph) { g.AICR.GeneratorDigest = "sha256:" + strings.Repeat("d", 64) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := aicrGraph()
			want, err := base.Digest()
			if err != nil {
				t.Fatal(err)
			}
			changed := aicrGraph()
			test.mutate(&changed)
			got, err := changed.Digest()
			if err != nil {
				t.Fatal(err)
			}
			if got == want {
				t.Fatal("changed target or AICR content retained build identity")
			}
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
