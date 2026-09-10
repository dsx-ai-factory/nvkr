package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func sampleGraph() *ir.Graph {
	commit := strings.Repeat("c", 40)
	tree := strings.Repeat("e", 40)
	toolchain := "golang@sha256:" + strings.Repeat("d", 64)
	vendorSpec, err := ir.VendorSpecIdentity(nil, toolchain)
	if err != nil {
		panic(err)
	}
	return &ir.Graph{
		SchemaVersion: ir.SchemaVersion, RecipeID: "k8s-core-worker",
		Criteria: criteria.Criteria{Category: "kubernetes", Role: "worker", K8sVersion: "1.32.0", OS: "ubuntu", Arch: "amd64"},
		Nodes: []ir.Node{
			{ID: "build-kubectl", Kind: ir.KindBuild, Component: "kubectl", Needs: []string{"fetch-k"},
				BuilderType: ir.BuilderOCIHermetic, Toolchain: toolchain,
				Platform: "linux/amd64", Network: ir.NetworkNone, Workdir: "/src",
				Env: []ir.EnvVar{
					{Name: "CGO_ENABLED", Value: "0"}, {Name: "GOAMD64", Value: "v1"},
					{Name: "GOARCH", Value: "amd64"}, {Name: "GOFLAGS", Value: "-trimpath -buildvcs=false"},
					{Name: "GOOS", Value: "linux"}, {Name: "GOTOOLCHAIN", Value: "local"},
					{Name: "LC_ALL", Value: "C"}, {Name: "SOURCE_DATE_EPOCH", Value: "1"}, {Name: "TZ", Value: "UTC"},
				},
				Command: []string{"make", "kubectl"}, SourceDest: "k-src", SourceCommit: commit,
				SourceTreeHash: tree, VendorSpecDigest: vendorSpec, SourceReadOnly: true,
				WriteDir: "out", ArtifactPath: "out/kubectl", OutputType: "host-package", Version: "1.32.0"},
			{ID: "fetch-k", Kind: ir.KindFetch, Repo: "https://x/k", Commit: commit, TreeHash: tree,
				Dest: "k-src", VendorImage: toolchain, VendorSpecDigest: vendorSpec, Network: ir.NetworkDefault},
			{ID: "verify", Kind: ir.KindVerify, Needs: []string{"build-kubectl"}},
		},
	}
}

func prebuiltEmitterGraph() *ir.Graph {
	return &ir.Graph{
		SchemaVersion: ir.SchemaVersion, RecipeID: "prebuilt",
		Criteria: criteria.Criteria{Role: "worker", K8sVersion: "1.32.0", OS: "ubuntu", Arch: "amd64"},
		Nodes: []ir.Node{
			{ID: "pull-kubectl", Kind: ir.KindPull, Component: "kubectl", OutputType: "host-package",
				Network: ir.NetworkDefault, Platform: "linux/amd64", Artifact: "https://dl/amd64/kubectl",
				Digest: "sha256:" + strings.Repeat("a", 64), ArtifactPath: "kubectl", Version: "1.32.0",
				Env: []ir.EnvVar{{Name: "SOURCE_DATE_EPOCH", Value: "1"}}},
			{ID: "pull-pause", Kind: ir.KindPull, Component: "pause", OutputType: "oci-image",
				Network: ir.NetworkDefault, Platform: "linux/amd64", Artifact: "registry.k8s.io/pause:3.10",
				Digest: "sha256:" + strings.Repeat("b", 64), Version: "3.10.0"},
			{ID: "verify", Kind: ir.KindVerify, Needs: []string{"pull-kubectl", "pull-pause"}},
		},
	}
}

func graphDigest(t *testing.T, graph *ir.Graph) string {
	t.Helper()
	digest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func testVerificationConfig() VerificationConfig {
	return VerificationConfig{
		Cosign: "trust/cosign", CosignDigest: "sha256:" + strings.Repeat("c", 64),
		ExpectedDigests: "trust/expected-digests.json", Certificate: "trust/vector.pem",
		Bundle: "trust/vector.bundle", TrustedRoot: "trust/root.json",
		TrustedRootDigest: "sha256:" + strings.Repeat("d", 64),
		Identity:          "vector@example.test", Issuer: "https://issuer.example.test",
		Profile: "trust/profile.json", ProfileCertificate: "trust/profile.pem",
		ProfileBundle: "trust/profile.bundle", ProfileTrustedRoot: "trust/root.json",
		ProfileTrustedRootDigest: "sha256:" + strings.Repeat("d", 64),
		ProfileIdentity:          "profile@example.test", ProfileIssuer: "https://issuer.example.test",
	}
}

func testEmitters(data string) []Emitter {
	verify := testVerificationConfig()
	runner := "sha256:" + strings.Repeat("a", 64)
	return []Emitter{
		GitLab{Data: data, RunnerDigest: runner, Verify: verify},
	}
}

func TestEmitterContextAdmission(t *testing.T) {
	if output, err := (GitLab{}).Render(nil, nil, ""); executionErrorCode(err) != nkderr.CodeUsage || output != nil { //nolint:staticcheck // intentional nil-context admission test
		t.Fatalf("nil-context render = %q, %v; want no bytes and usage", output, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if output, err := (GitLab{}).Render(ctx, nil, ""); executionErrorCode(err) != nkderr.CodeTimeout || output != nil {
		t.Fatalf("canceled render = %q, %v; want no bytes and timeout", output, err)
	}
}

func TestIncompleteTransportAdaptersRejectPrebuiltBeforeBytes(t *testing.T) {
	graph := prebuiltEmitterGraph()
	digest := graphDigest(t, graph)
	verify := testVerificationConfig()
	runner := "sha256:" + strings.Repeat("a", 64)
	// GitHub is no longer here: under ADR-18 §8 its jobs run on generic runners and
	// isolation is helper-enforced, so the prebuilt lane is admitted (see the GitLab
	// prebuilt tests). Tekton/Argo still lack a complete Model-B transport adapter.
	for _, emitter := range []Emitter{
		Tekton{RunnerDigest: runner, Verify: verify},
		Argo{RunnerDigest: runner, Verify: verify},
	} {
		output, err := emitter.Render(context.Background(), graph, digest)
		if executionErrorCode(err) != nkderr.CodeEngineCapability {
			t.Fatalf("%s emitted without a complete transport adapter: %v", emitter.Name(), err)
		}
		if len(output) != 0 {
			t.Fatalf("%s returned %d bytes before rejecting its incomplete transport mapping", emitter.Name(), len(output))
		}
	}
}

// A rendered provider pipeline cannot honestly assert what container manifest or
// sandbox the provider eventually selected. Until an emitter is configured with a
// narrow provider-attestation adapter, every default from-source emitter therefore
// fails before writing any apparently authoritative YAML.
func TestDefaultEmittersRejectFromSourceWithoutExecutionAttestation(t *testing.T) {
	g := sampleGraph()
	digest := graphDigest(t, g)
	for _, em := range []Emitter{GitHub{}, GitLab{}, Tekton{}, Argo{}} {
		b, err := em.Render(context.Background(), g, digest)
		if executionErrorCode(err) != nkderr.CodeEngineCapability {
			t.Fatalf("%s: from-source emission must require provider execution attestation, got %v", em.Name(), err)
		}
		if len(b) != 0 {
			t.Errorf("%s returned %d bytes before rejecting missing execution authority", em.Name(), len(b))
		}
	}
}

// The engine-neutral lowering remains one structural implementation. It can be
// inspected independently of whether a concrete provider has an attestation adapter.
func TestLoweringKeepsBuildSemanticsInPinnedIR(t *testing.T) {
	jobs, err := LowerJobs(sampleGraph())
	if err != nil {
		t.Fatal(err)
	}
	var build Job
	for _, job := range jobs {
		if job.Kind == "build" {
			build = job
			break
		}
	}
	if build.Name != "build-kubectl" || build.Image != "golang@sha256:"+strings.Repeat("d", 64) {
		t.Fatalf("build job did not retain the pinned node/container identity: %+v", build)
	}
	script := strings.Join(ExecScript(build, "sha256:"+strings.Repeat("a", 64)), "\n")
	for _, want := range []string{"nkd-exec exec-node", "nodes-digest", "build-kubectl.json"} {
		if !strings.Contains(script, want) {
			t.Errorf("lowered execution missing %q: %s", want, script)
		}
	}
	if strings.Contains(script, "docker run") || strings.Contains(script, "make kubectl") {
		t.Fatalf("build semantics leaked into rendered execution: %s", script)
	}
}

// The terminal verify node must run unconditionally in every engine — even after a
// build fails — so a divergence is always named (NKD-FR-BLD-006, NKD-FR-REP-020).
func TestEmittersEmitUnconditionalVerify(t *testing.T) {
	g := prebuiltEmitterGraph()
	digest := graphDigest(t, g)
	unconditional := map[string]string{
		"github-actions": "if: ${{ always() }}",
		"gitlab-ci":      "when: always",
		"tekton":         "finally:",
		"argo":           ".Failed",
	}
	for _, em := range testEmitters("") {
		b, err := em.Render(context.Background(), g, digest)
		if err != nil {
			t.Fatalf("%s: %v", em.Name(), err)
		}
		if marker := unconditional[em.Name()]; !strings.Contains(string(b), marker) {
			t.Errorf("%s: verify is not unconditional (missing %q)", em.Name(), marker)
		}
	}
}

// The emitted terminal verify MUST carry the full resolved criteria (and, for a
// non-embedded catalog, --data).
func TestEmittersVerifyCarriesFullCriteria(t *testing.T) {
	g := prebuiltEmitterGraph()
	digest := graphDigest(t, g)
	for _, em := range testEmitters("opcat") {
		b, err := em.Render(context.Background(), g, digest)
		if err != nil {
			t.Fatalf("%s: %v", em.Name(), err)
		}
		out := string(b)
		for _, want := range []string{
			"--role", "worker", "--os", "ubuntu", "--arch", "amd64", "--k8s", "1.32.0", "--data", "opcat",
			"--cosign", "trust/cosign", "--cosign-digest", "sha256:" + strings.Repeat("c", 64),
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: verify command missing %q", em.Name(), want)
			}
		}
	}
}

func TestEmitterRejectsMissingCosignPinBeforeBytes(t *testing.T) {
	g := prebuiltEmitterGraph()
	verify := testVerificationConfig()
	verify.CosignDigest = ""
	out, err := (GitLab{
		RunnerDigest: "sha256:" + strings.Repeat("a", 64),
		Verify:       verify,
	}).Render(context.Background(), g, graphDigest(t, g))
	if executionErrorCode(err) != nkderr.CodeUnpinnedInput {
		t.Fatalf("emitter must reject a terminal verifier without an exact cosign pin: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("emitter returned %d bytes before rejecting the missing cosign pin", len(out))
	}
}

// Every adapter lowers a pull (no-compile) node to a uniform `exec-node` job, and the
// terminal verify waits on every pull job. The acquisition logic lives in the runner +
// nodes.json, not the emitted YAML.
func TestEmittersLowerPullNodes(t *testing.T) {
	g := prebuiltEmitterGraph()
	digest := graphDigest(t, g)
	for _, em := range testEmitters("") {
		b, err := em.Render(context.Background(), g, digest)
		if err != nil {
			t.Fatalf("%s: %v", em.Name(), err)
		}
		out := string(b)
		for _, want := range []string{
			"pull-kubectl", "pull-pause",
			"exec-node --node pull-kubectl", "exec-node --node pull-pause",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: pull emission missing %q", em.Name(), want)
			}
		}
	}
}

// The build command, env, commit, and vendor command are NOT in the emitted YAML —
// they live in the committed nodes.json the runner reads (ADR-18 §2). A non-vendored
// source's vendor command travels there as data.
func TestNodesJSONCarriesBuildSemantics(t *testing.T) {
	g := sampleGraph()
	g.Nodes[1].VendorCmd = []string{"go", "mod", "vendor"}
	g.Nodes[1].VendorSpecDigest, _ = ir.VendorSpecIdentity(g.Nodes[1].VendorCmd, g.Nodes[1].VendorImage)
	g.Nodes[0].VendorSpecDigest = g.Nodes[1].VendorSpecDigest
	nodes, err := NodesJSON(g)
	if err != nil {
		t.Fatal(err)
	}
	s := string(nodes)
	for _, want := range []string{strings.Repeat("c", 40), strings.Repeat("e", 40), "make", "kubectl", "go", "mod", "vendor", "CGO_ENABLED"} {
		if !strings.Contains(s, want) {
			t.Errorf("nodes.json missing build-semantic field %q", want)
		}
	}
	// And the engine-neutral shell plan must not contain the vendor command either.
	jobs, err := LowerJobs(g)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if strings.Contains(strings.Join(ExecScript(job, "sha256:"+strings.Repeat("a", 64)), "\n"), "go mod vendor") {
			t.Error("vendor command leaked into the execution shell (should live in nodes.json)")
		}
	}
}

func TestBundleIdentityBindsExactPipelineBodyAndNodes(t *testing.T) {
	g := sampleGraph()
	nodes, err := NodesJSON(g)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("jobs:\n  build: {}\n")
	digest := graphDigest(t, g)
	base, nodesDigest, bodyDigest, err := BundleDigest("test", digest, nodes, body)
	if err != nil {
		t.Fatal(err)
	}
	if base == "" || nodesDigest != FileDigest(nodes) || bodyDigest != FileDigest(body) {
		t.Fatalf("bundle identity did not expose exact file digests: %q %q %q", base, nodesDigest, bodyDigest)
	}
	withBodyDrift, _, _, err := BundleDigest("test", digest, nodes, append(body, '#'))
	if err != nil || withBodyDrift == base {
		t.Fatalf("bundle identity must change when the executable body changes: %v", err)
	}
	substitute := prebuiltEmitterGraph()
	substitute.RecipeID = "substituted"
	substitutedNodes, err := NodesJSON(substitute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := BundleDigest("test", digest, substitutedNodes, body); executionErrorCode(err) != nkderr.CodePipelineDrift {
		t.Fatalf("nodes from a different recipe must not accept the caller-supplied recipe digest, got %v", err)
	}

	emitGraph := prebuiltEmitterGraph()
	emitted, err := (testEmitters("")[0]).Render(context.Background(), emitGraph, graphDigest(t, emitGraph))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{BundleSchemaVersion, "# nodesDigest: sha256:", "# pipelineBodyDigest: sha256:", "# bundleDigest: sha256:"} {
		if !strings.Contains(string(emitted), want) {
			t.Fatalf("generated header missing %q", want)
		}
	}
}

func TestLowerJobsSeparatesNetworkFetchFromOfflineBuild(t *testing.T) {
	jobs, err := LowerJobs(sampleGraph())
	if err != nil {
		t.Fatal(err)
	}
	byKind := map[string][]Job{}
	for _, j := range jobs {
		byKind[j.Kind] = append(byKind[j.Kind], j)
		if j.Kind == "build" && j.Network != ir.NetworkNone {
			t.Fatalf("build job %q is not offline: %q", j.Name, j.Network)
		}
		if len(j.Nodes) > 1 {
			t.Fatalf("job %q combines network phases: nodes=%v", j.Name, j.Nodes)
		}
	}
	if len(byKind["fetch"]) != 1 || len(byKind["build"]) != 1 {
		t.Fatalf("want one fetch and one build job, got %+v", byKind)
	}
	b := byKind["build"][0]
	if len(b.Consumes) < 2 || b.Produces == nil || b.NodesDigest == "" {
		t.Fatalf("offline build transport contract is incomplete: %+v", b)
	}
	script := ExecScript(b, "sha256:"+strings.Repeat("a", 64))
	if !strings.Contains(strings.Join(script, "\n"), "--nodes-digest sha256:") ||
		!strings.Contains(strings.Join(script, "\n"), "--result .nkd/work/results/") {
		t.Fatalf("build script does not authenticate its IR and persist its result: %v", script)
	}
}

func TestTransportPlanBindsEveryProducerConsumerEdge(t *testing.T) {
	plan, err := TransportPlanJSON(sampleGraph())
	if err != nil {
		t.Fatal(err)
	}
	var decoded TransportPlan
	if err := canonical.UnmarshalExact(plan, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SchemaVersion != TransportPlanSchema || len(decoded.Bindings) != 3 {
		t.Fatalf("unexpected transport plan: %+v", decoded)
	}
	byName := map[string]TransportBinding{}
	for _, binding := range decoded.Bindings {
		byName[binding.Name] = binding
		if binding.Producer == "" || len(binding.Consumers) == 0 || len(binding.Paths) == 0 || binding.Authentication == "" {
			t.Fatalf("incomplete transport binding: %+v", binding)
		}
	}
	if byName[RunnerBinary].Producer != BootstrapJob || len(byName[RunnerBinary].Consumers) != 3 {
		t.Fatalf("runner edge is not exhaustive: %+v", byName[RunnerBinary])
	}
	if !strings.Contains(string(plan), "runtime-content-receipt+consumer-rehash") {
		t.Fatalf("runtime-produced transport edges lack receipt authority: %s", plan)
	}
}

func TestArtifactAdaptersUseAllowListedTarCarriers(t *testing.T) {
	g := prebuiltEmitterGraph()
	digest := graphDigest(t, g)
	for _, emitter := range testEmitters("") {
		out, err := emitter.Render(context.Background(), g, digest)
		if err != nil {
			t.Fatal(err)
		}
		text := string(out)
		for _, required := range []string{"transport-pack", "transport-unpack", ".nkd/transport/result-pull-kubectl.tar"} {
			if !strings.Contains(text, required) {
				t.Fatalf("%s omits carrier contract %q", emitter.Name(), required)
			}
		}
		if emitter.Name() == "github-actions" && strings.Contains(text, "path: |\n") {
			t.Fatal("GitHub adapter still relies on multi-path artifact reconstruction")
		}
	}
}

// TestPinnedHelperImageSkipsBootstrap covers ADR-18 §8's pinned-helper-image delivery
// (beside the from-source bootstrap): with HelperImage set, no from-source bootstrap job is
// emitted, every job references the pinned image, and the helper is invoked on PATH
// (nkd-exec) with no per-job re-hash — the image digest pins it.
func TestPinnedHelperImageSkipsBootstrap(t *testing.T) {
	g := prebuiltEmitterGraph()
	digest := graphDigest(t, g)
	verify := testVerificationConfig()
	runner := "sha256:" + strings.Repeat("a", 64)
	img := "ghcr.io/nvidia/nkd-exec@sha256:" + strings.Repeat("e", 64)

	for _, tc := range []struct {
		name   string
		render func() ([]byte, error)
	}{
		{"gitlab", func() ([]byte, error) {
			return GitLab{RunnerDigest: runner, Verify: verify, HelperImage: img}.Render(context.Background(), g, digest)
		}},
		{"github", func() ([]byte, error) {
			return GitHub{RunnerDigest: runner, Verify: verify, HelperImage: img}.Render(context.Background(), g, digest)
		}},
	} {
		b, err := tc.render()
		if err != nil {
			t.Fatalf("%s pinned-helper render failed: %v", tc.name, err)
		}
		out := string(b)
		if !strings.Contains(out, img) {
			t.Fatalf("%s must reference the pinned helper image:\n%s", tc.name, out)
		}
		if strings.Contains(out, BootstrapJob) || strings.Contains(out, "go build -trimpath -o "+RunnerBinary) {
			t.Fatalf("%s pinned-helper mode must omit the from-source bootstrap:\n%s", tc.name, out)
		}
		// Helper is on PATH (nkd-exec), not the transported ./nkd-exec, and not re-hashed.
		if strings.Contains(out, "./"+RunnerBinary) || strings.Contains(out, "sha256sum "+RunnerBinary) {
			t.Fatalf("%s pinned-helper mode must use the on-PATH helper with no re-hash:\n%s", tc.name, out)
		}
		if !strings.Contains(out, RunnerBinary+" transport-unpack") {
			t.Fatalf("%s must invoke the on-PATH helper:\n%s", tc.name, out)
		}
	}
}

func TestBootstrapFailsClosedWithoutRunnerPin(t *testing.T) {
	missing := strings.Join(BootstrapScript(""), "\n")
	if !strings.Contains(missing, "ERROR:") || !strings.Contains(missing, "exit 1") || strings.Contains(missing, "go build") {
		t.Fatalf("missing runner pin must stop before building: %s", missing)
	}
	pinned := strings.Join(BootstrapScript("sha256:runner"), "\n")
	if !strings.Contains(pinned, "go build") || !strings.Contains(pinned, `test "$GOT" = "sha256:runner"`) {
		t.Fatalf("pinned bootstrap must build and verify the runner: %s", pinned)
	}
}

// Under ADR-18 §8 the GitHub prebuilt lane is admitted, so an extension overlay must
// be RENDERED into the workflow (ADR-21: a provider route must render every declared hook,
// never silently drop it), not fail closed.
func TestGitHubRendersExtensionOverlayHooks(t *testing.T) {
	g := prebuiltEmitterGraph()
	digest := graphDigest(t, g)
	ext := &Extensions{PreBuild: []string{"echo pre"}, PostVerify: []string{"echo post"}}
	b, err := (GitHub{Ext: ext, RunnerDigest: "sha256:" + strings.Repeat("a", 64), Verify: testVerificationConfig()}).Render(context.Background(), g, digest)
	if err != nil {
		t.Fatalf("Model-B GitHub prebuilt render failed: %v", err)
	}
	out := string(b)
	for _, want := range []string{"echo pre", "echo post"} {
		if !strings.Contains(out, want) {
			t.Fatalf("extension hook %q was not rendered into the workflow:\n%s", want, out)
		}
	}
	// And it must remain generic-runner Model B: no engine container block, no custom tag.
	if strings.Contains(out, "container:") || strings.Contains(out, "nkd-network-none") {
		t.Fatalf("GitHub Model B must not use an engine container block or a network-none tag:\n%s", out)
	}
}
