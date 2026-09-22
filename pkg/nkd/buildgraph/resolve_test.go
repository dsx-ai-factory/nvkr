package buildgraph

import (
	"context"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

const (
	testVMRepo      = "https://github.com/example/node-image"
	testVMCommit    = "1111111111111111111111111111111111111111"
	testVMTree      = "2222222222222222222222222222222222222222"
	testVMToolchain = "example.invalid/nkd-vm-builder@sha256:59b8183301af6dc358c9258d7b2ab0ee1a9363618552334fb3b160d454cbda72"
)

func testVMImageComponent(hosts []execution.HostPlatform, policy execution.CapabilityPolicy) catalog.Component {
	toolchains := make(map[string]string, len(hosts))
	for _, host := range hosts {
		toolchains[strings.TrimPrefix(string(host), "linux/")] = testVMToolchain
	}
	return catalog.Component{Platforms: []string{"linux/amd64", "linux/arm64"},
		FromSource: true, BuilderType: catalog.BuilderVMImage, OutputType: "disk-image",
		Disk: &catalog.DiskSpec{Format: "qcow2", Encryption: "none"},
		BuildRequirements: &execution.Requirements{
			Resources: &execution.ResourceRequest{
				CPUMillis: 1000, MemoryMiB: 1024, EphemeralStorageMiB: 4096,
			},
			AllowedHostPlatforms: hosts,
			Capabilities: []execution.CapabilityRequirement{{
				Name: execution.CapabilityKVM, Policy: policy,
			}},
			TimeoutSeconds: 3600,
		},
		Repo: testVMRepo, Commit: testVMCommit, TreeHash: testVMTree,
		ToolchainImage: testVMToolchain, ToolchainImages: toolchains,
		BuildCommand: []string{"make", "image"}, ArtifactPath: "out/image.qcow2", SourceDate: 1733939955,
	}
}

func kubernetesFixture(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.LoadContext(context.Background(), catalog.Dir("../../../testdata/catalogs/kubernetes-layered"))
	if err != nil {
		t.Fatalf("Kubernetes producer fixture must load and validate: %v", err)
	}
	return cat
}

func TestResolveVMImageKVMHostRoutes(t *testing.T) {
	makeCatalog := func(component catalog.Component) *catalog.Catalog {
		return &catalog.Catalog{
			Components: map[string]catalog.Component{"image": component},
		}
	}
	query := Selection{Components: []string{"image"}, Platform: "linux/arm64"}
	for _, test := range []struct {
		name     string
		hosts    []execution.HostPlatform
		policy   execution.CapabilityPolicy
		wantCode nkderr.Code
	}{
		{
			name:     "required cross-architecture",
			hosts:    []execution.HostPlatform{execution.HostPlatformLinuxAMD64},
			policy:   execution.CapabilityRequired,
			wantCode: nkderr.CodeInvalidCatalog,
		},
		{
			name: "required native",
			hosts: []execution.HostPlatform{
				execution.HostPlatformLinuxAMD64,
				execution.HostPlatformLinuxARM64,
			},
			policy: execution.CapabilityRequired,
		},
		{
			name:   "preferred cross-architecture",
			hosts:  []execution.HostPlatform{execution.HostPlatformLinuxAMD64},
			policy: execution.CapabilityPreferred,
		},
		{
			name:   "forbidden cross-architecture",
			hosts:  []execution.HostPlatform{execution.HostPlatformLinuxAMD64},
			policy: execution.CapabilityForbidden,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			component := testVMImageComponent(test.hosts, test.policy)
			resolved, err := Resolve(makeCatalog(component), query)
			if test.wantCode != "" {
				if code(err) != test.wantCode || resolved != nil {
					t.Fatalf("Resolve() = %#v, %v; want no result and %s", resolved, err, test.wantCode)
				}
				return
			}
			if err != nil || resolved == nil {
				t.Fatalf("Resolve() = %#v, %v; want success", resolved, err)
			}
		})
	}
}

func TestResolveRequiresToolchainForSelectedNativeArch(t *testing.T) {
	component := catalog.Component{Platforms: []string{"linux/amd64", "linux/arm64"},
		FromSource: true, Repo: "https://github.com/example/tool",
		Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40),
		ToolchainImages: map[string]string{"amd64": "sha256:" + strings.Repeat("c", 64)},
		BuildCommand:    []string{"make"}, ArtifactPath: "out/tool", OutputType: "host-package", SourceDate: 1,
	}
	cat := &catalog.Catalog{
		Components: map[string]catalog.Component{"tool": component},
	}
	if _, err := Resolve(cat, Selection{Components: []string{"tool"}, Platform: "linux/arm64"}); code(err) != nkderr.CodeUnpinnedSource {
		t.Fatalf("missing selected-arch toolchain must fail closed, got %v", err)
	}
	if _, err := Resolve(cat, Selection{Components: []string{"tool"}, Platform: "linux/amd64"}); err != nil {
		t.Fatalf("present selected-arch toolchain should resolve: %v", err)
	}
}

// The compiled IR shares one fetch across all core components, has a build node
// per component, and ends in a single verify node depending on every build.
func TestCompileGraphShape(t *testing.T) {
	cat := kubernetesFixture(t)
	res, err := Resolve(cat, Selection{Components: fixtureProducers(), Platform: "linux/amd64"})
	if err != nil {
		t.Fatal(err)
	}
	g := mustCompile(t, res)
	var fetch, build, verify int
	var verifyNeeds int
	for _, n := range g.Nodes {
		switch n.Kind {
		case ir.KindFetch:
			fetch++
		case ir.KindBuild:
			build++
			if n.Network != ir.NetworkNone {
				t.Errorf("build node %s not hermetic (network=%q)", n.ID, n.Network)
			}
		case ir.KindVerify:
			verify++
			verifyNeeds = len(n.Needs)
		}
	}
	// Eight sources => eight fetch nodes (the 8 kubernetes components incl. pause
	// share one fetch; etcd, runc, coredns, containerd, cni-plugins, crictl, helm
	// each have their own).
	if fetch != 8 {
		t.Errorf("expected 8 fetch nodes (one per source), got %d", fetch)
	}
	if build != 15 {
		t.Errorf("expected 15 build nodes, got %d", build)
	}
	if verify != 1 || verifyNeeds != 15 {
		t.Errorf("expected 1 verify node needing 15 builds, got verify=%d needs=%d", verify, verifyNeeds)
	}
}

func TestCompileSeparatesVendorClosuresForSameSourcePin(t *testing.T) {
	base := catalog.Component{Platforms: []string{"linux/amd64", "linux/arm64"},
		FromSource: true, Repo: "https://example.test/shared", Tag: "v1.0.0",
		Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40),
		ToolchainImage: "registry.example/toolchain@sha256:" + strings.Repeat("c", 64),
		OutputType:     "raw", BuildCommand: []string{"make"}, WriteDir: "out",
		ArtifactPath: "out/artifact", SourceDate: 1,
	}
	first, second := base, base
	first.VendorCommand = []string{"go", "mod", "vendor"}
	second.VendorCommand = []string{"make", "vendor"}
	resolved := &Resolved{
		Platform: "linux/amd64",
		Components: []catalog.NamedComponent{
			{Name: "alpha", Component: first},
			{Name: "beta", Component: second},
		},
	}
	graph := mustCompile(t, resolved)
	if err := graph.Validate(); err != nil {
		t.Fatalf("separate vendor closures must compile to valid IR: %v", err)
	}
	var fetches []ir.Node
	for _, node := range graph.Nodes {
		if node.Kind == ir.KindFetch {
			fetches = append(fetches, node)
		}
	}
	if len(fetches) != 2 || fetches[0].ID == fetches[1].ID || fetches[0].Dest == fetches[1].Dest {
		t.Fatalf("different vendor specs collided: %+v", fetches)
	}
	if fetches[0].VendorSpecDigest == fetches[1].VendorSpecDigest {
		t.Fatalf("different vendor commands produced one closure identity: %+v", fetches)
	}
}

func TestCompileSourceIdentityIsTransportIndependentAndCollisionFree(t *testing.T) {
	component := func(name, repo, tree, image string) catalog.NamedComponent {
		return catalog.NamedComponent{Name: name, Component: catalog.Component{Platforms: []string{"linux/amd64", "linux/arm64"},
			FromSource: true, Repo: repo, Tag: "v1.0.0",
			Commit: strings.Repeat("a", 40), TreeHash: tree,
			ToolchainImage: image, OutputType: "raw", BuildCommand: []string{"make"},
			WriteDir: "out", ArtifactPath: "out/artifact", SourceDate: 1,
		}}
	}
	digest := strings.Repeat("c", 64)
	left := &Resolved{
		Platform:   "linux/amd64",
		Components: []catalog.NamedComponent{component("alpha", "https://one.example/team/shared.git", strings.Repeat("b", 40), "one.example/toolchain@sha256:"+digest)},
	}
	right := &Resolved{
		Platform:   left.Platform,
		Components: []catalog.NamedComponent{component("alpha", "ssh://mirror.example/other/name.git", strings.Repeat("b", 40), "mirror.example/toolchain@sha256:"+digest)},
	}
	leftGraph, rightGraph := mustCompile(t, left), mustCompile(t, right)
	leftDigest, err := leftGraph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	rightDigest, err := rightGraph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if leftDigest != rightDigest {
		t.Fatalf("repository/registry relocation changed content identity: %s != %s", leftDigest, rightDigest)
	}
	if leftGraph.Nodes[1].ID != rightGraph.Nodes[1].ID || leftGraph.Nodes[1].Dest != rightGraph.Nodes[1].Dest {
		t.Fatalf("transport relocation changed source node identity: %+v != %+v", leftGraph.Nodes[1], rightGraph.Nodes[1])
	}
	if got := strings.TrimPrefix(leftGraph.Nodes[1].Dest, "source-"); len(got) != 64 {
		t.Fatalf("source destination truncates its collision-resistant content identity: %q", leftGraph.Nodes[1].Dest)
	}

	collision := &Resolved{
		Platform: left.Platform,
		Components: []catalog.NamedComponent{
			component("alpha", "https://one.example/team/shared.git", strings.Repeat("b", 40), "one.example/toolchain@sha256:"+digest),
			component("beta", "https://two.example/other/shared.git", strings.Repeat("d", 40), "one.example/toolchain@sha256:"+digest),
		},
	}
	graph := mustCompile(t, collision)
	var fetches []ir.Node
	for _, node := range graph.Nodes {
		if node.Kind == ir.KindFetch {
			fetches = append(fetches, node)
		}
	}
	if len(fetches) != 2 || fetches[0].ID == fetches[1].ID || fetches[0].Dest == fetches[1].Dest {
		t.Fatalf("different content pins with colliding repo basenames shared a fetch: %+v", fetches)
	}
}

func TestCompileRejectsMalformedPublicResolvedWithoutPanic(t *testing.T) {
	malformed := &Resolved{
		Platform: "linux/amd64",
		Components: []catalog.NamedComponent{{Name: "bad", Component: catalog.Component{Platforms: []string{"linux/amd64", "linux/arm64"},
			FromSource: true, Repo: "https://example.test/source", Commit: "short", TreeHash: strings.Repeat("b", 40),
			ToolchainImage: "toolchain@sha256:" + strings.Repeat("c", 64), VendorCommand: []string{"bad\xff"},
			Tag: "v1", SourceDate: 1, OutputType: "raw", BuildCommand: []string{"make"}, WriteDir: "out", ArtifactPath: "out/bad",
		}}},
	}
	if graph, err := Compile(malformed); err == nil || graph != nil {
		t.Fatalf("malformed public Resolved compiled successfully: graph=%+v err=%v", graph, err)
	}
}

// Every build node carries the reproducibility flag discipline pinned in the IR
// itself, so it is part of buildDigest and provably applied to every Go lane
// regardless of the toolchain image's ENV (NKD-FR-BLD-023, NKD-FR-REP-004,
// NKD-NFR-005). GOFLAGS is authoritative and cannot be shadowed by a catalog stamp.
func TestBuildNodesPinReproFlags(t *testing.T) {
	cat := kubernetesFixture(t)
	res, err := Resolve(cat, Selection{Components: fixtureProducers(), Platform: "linux/amd64"})
	if err != nil {
		t.Fatal(err)
	}
	g := mustCompile(t, res)
	seen := 0
	for _, n := range g.Nodes {
		if n.Kind != ir.KindBuild {
			continue
		}
		seen++
		var goflags string
		for _, ev := range n.Env {
			if ev.Name == "GOFLAGS" {
				goflags = ev.Value
			}
		}
		for _, want := range []string{"-trimpath", "-buildvcs=false"} {
			if !strings.Contains(goflags, want) {
				t.Errorf("build %s: GOFLAGS %q missing %q", n.Component, goflags, want)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no build nodes found")
	}
}

// Multi-arch (model B): the same producer selection resolves for amd64 and arm64, yielding
// DISTINCT buildDigests, each build node carries the target GOARCH, and the runc
// CGO lane selects a toolchain image NATIVE to the arch (no cross-compilation).
func TestMultiArchDistinctDigestsAndNativeToolchain(t *testing.T) {
	cat := kubernetesFixture(t)
	base := Selection{Components: fixtureProducers(), Platform: "linux/amd64"}
	res := func(arch string) *Resolved {
		c := base
		c.Platform = "linux/" + arch
		r, err := Resolve(cat, c)
		if err != nil {
			t.Fatalf("resolve arch %q: %v", arch, err)
		}
		return r
	}
	gAmd, gArm := mustCompile(t, res("amd64")), mustCompile(t, res("arm64"))
	dAmd, err := gAmd.Digest()
	if err != nil {
		t.Fatal(err)
	}
	dArm, _ := gArm.Digest()
	if dAmd == dArm {
		t.Fatalf("amd64 and arm64 buildDigests must differ (got %s for both)", dAmd)
	}

	runcTC := func(g *ir.Graph) (tc, goarch string) {
		for _, n := range g.Nodes {
			if n.Kind == ir.KindBuild && n.Component == "runc" {
				tc = n.Toolchain
				for _, ev := range n.Env {
					if ev.Name == "GOARCH" {
						goarch = ev.Value
					}
				}
			}
		}
		return
	}
	tcAmd, gaAmd := runcTC(gAmd)
	tcArm, gaArm := runcTC(gArm)
	if gaAmd != "amd64" || gaArm != "arm64" {
		t.Fatalf("runc GOARCH mismatch: amd64-build=%q arm64-build=%q", gaAmd, gaArm)
	}
	if tcAmd == "" || tcArm == "" || tcAmd == tcArm {
		t.Fatalf("runc must use a native per-arch toolchain: amd64=%q arm64=%q", tcAmd, tcArm)
	}

}

// Multi-arch across the WHOLE set (the "expand to all components" guarantee): the
// same explicit selection (all 15 components) compiles for amd64 and arm64 into
// distinct buildDigests, and EVERY build node is native to its arch (model B) —
// target platform + GOARCH match, no foreign-arch literal leaks into the command or
// artifact path, and kubernetes' CGO/C lanes use a per-arch native toolchain + plain
// gcc (no cross-compiler prefix).
func TestMultiArchAllComponents(t *testing.T) {
	cat := kubernetesFixture(t)
	base := Selection{Components: fixtureProducers(), Platform: "linux/amd64"}
	compileArch := func(arch string) *ir.Graph {
		c := base
		c.Platform = "linux/" + arch
		r, err := Resolve(cat, c)
		if err != nil {
			t.Fatalf("resolve %s: %v", arch, err)
		}
		return mustCompile(t, r)
	}
	gAmd, gArm := compileArch("amd64"), compileArch("arm64")

	dAmd, err := gAmd.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if dArm, _ := gArm.Digest(); dAmd == dArm {
		t.Fatalf("full-set amd64 and arm64 buildDigests must differ (got %s for both)", dAmd)
	}

	// The foreign arch's literals must never appear in a build's command/path — this is
	// the concrete "no amd64 hardcoding" assertion across every lane.
	foreign := map[string][]string{"amd64": {"arm64", "aarch64"}, "arm64": {"amd64", "x86_64"}}
	check := func(g *ir.Graph, arch string) {
		builds := 0
		for _, n := range g.Nodes {
			if n.Kind != ir.KindBuild {
				continue
			}
			builds++
			if n.Platform != "linux/"+arch {
				t.Errorf("%s: build %s platform=%q, want linux/%s", arch, n.Component, n.Platform, arch)
			}
			var goarch string
			for _, ev := range n.Env {
				if ev.Name == "GOARCH" {
					goarch = ev.Value
				}
			}
			if goarch != arch {
				t.Errorf("%s: build %s GOARCH=%q, want %s", arch, n.Component, goarch, arch)
			}
			hay := strings.Join(n.Command, " ") + " " + n.ArtifactPath
			for _, bad := range foreign[arch] {
				if strings.Contains(hay, bad) {
					t.Errorf("%s: build %s leaks foreign-arch literal %q in %q", arch, n.Component, bad, hay)
				}
			}
		}
		if builds != 15 {
			t.Fatalf("%s: expected 15 build nodes, got %d", arch, builds)
		}
	}
	check(gAmd, "amd64")
	check(gArm, "arm64")

	// kubernetes' toolchain is a per-arch NATIVE pin: distinct image per arch.
	toolchainOf := func(g *ir.Graph, comp string) string {
		for _, n := range g.Nodes {
			if n.Kind == ir.KindBuild && n.Component == comp {
				return n.Toolchain
			}
		}
		return ""
	}
	if a, b := toolchainOf(gAmd, "kube-apiserver"), toolchainOf(gArm, "kube-apiserver"); a == "" || b == "" || a == b {
		t.Fatalf("kubernetes toolchain must be native-per-arch: amd64=%q arm64=%q", a, b)
	}
	// kubelet (the k8s CGO lane) uses plain native gcc, not a cross triple, on both arches.
	for _, g := range []*ir.Graph{gAmd, gArm} {
		for _, n := range g.Nodes {
			if n.Kind == ir.KindBuild && n.Component == "kubelet" {
				var cc string
				for _, ev := range n.Env {
					if ev.Name == "CC" {
						cc = ev.Value
					}
				}
				if cc != "gcc" {
					t.Errorf("kubelet CC=%q, want native gcc (model B, no cross prefix)", cc)
				}
			}
		}
	}
}

// buildDigest is a pure function of the resolved output (NKD-FR-RC-018).
func TestBuildDigestDeterministic(t *testing.T) {
	cat := kubernetesFixture(t)
	q := Selection{Components: fixtureProducers(), Platform: "linux/amd64"}
	res1, _ := Resolve(cat, q)
	res2, _ := Resolve(cat, q)
	d1, err := mustCompile(t, res1).Digest()
	if err != nil {
		t.Fatal(err)
	}
	d2, _ := mustCompile(t, res2).Digest()
	if d1 != d2 {
		t.Fatalf("buildDigest not deterministic: %s vs %s", d1, d2)
	}
}

func code(err error) nkderr.Code {
	if e, ok := err.(*nkderr.Error); ok {
		return e.Code
	}
	return ""
}

func fixtureProducers() []string {
	return []string{"cni-plugins", "containerd", "coredns", "crictl", "etcd", "helm", "kube-apiserver", "kube-controller-manager", "kube-proxy", "kube-scheduler", "kubeadm", "kubectl", "kubelet", "pause", "runc"}
}
