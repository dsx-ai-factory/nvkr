package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/profile"
)

func qualifiedResultOptions(t *testing.T, sourceRoot string) (ir.Node, RunOptions, profile.ExecutionReceipt) {
	t.Helper()
	p, host, nodes, receipt := qualificationFixture(t)
	observation := profile.ExecutionObservation{
		RunnerDigest: "sha256:" + repeatHex("a"), RunnerOrigin: profile.RunnerPreexisting,
		RuntimeIdentity: "sha256:" + repeatHex("b"),
		Nodes: []profile.NodeExecutionObservation{{
			NodeID: nodes[0].ID, EngineClass: ir.EngineOCIHermetic,
			ToolchainDigest: "sha256:" + repeatHex("c"), SandboxMode: "fixture-sandbox",
		}},
	}
	return nodes[0], RunOptions{
		Ctx: context.Background(), SourceRoot: sourceRoot, Runtime: os.Args[0],
		Qualification: &QualifiedExecution{
			Profile: p, HostReceipt: host,
			Observer: fixedPipelineExecutionObserver{observation: observation},
		},
	}, receipt
}

func qualifiedResultGraph(t *testing.T, node ir.Node) (*ir.Graph, string) {
	t.Helper()
	fetch := testFetchNode(node.Needs[0], "https://example.test/source", node.SourceCommit,
		node.SourceTreeHash, node.SourceDest, node.Toolchain, node.VendorSpecDigest)
	graph := testSourceGraph(fetch, node)
	return graph, mustGraphDigest(t, graph)
}

func TestVerifyResultsRehydratesAndRehashesExactProducerSet(t *testing.T) {
	work := filepath.Join(t.TempDir(), "work")
	node, opts, receipt := qualifiedResultOptions(t, filepath.Join(work, "src"))
	g, buildDigest := qualifiedResultGraph(t, node)
	artifact := artifactHostPath(opts, node)
	if err := os.MkdirAll(filepath.Dir(artifact), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte("compiled"), 0o755); err != nil {
		t.Fatal(err)
	}
	digest, size, _ := sha256File(artifact)
	result := ArtifactResult{
		Component: node.Component, OutputType: node.OutputType, Toolchain: node.Toolchain,
		Qualification: ArtifactQualifiedExecution,
		InputDigest:   "sha256:" + repeatHex("b"), BuilderType: node.BuilderType,
		EngineClass: ir.EngineOCIHermetic, Path: node.ArtifactPath, SHA256: digest, Size: size,
		ProfileDigest: receipt.ProfileDigest(), ExecutionBinding: receipt.ExecutionBinding(),
	}
	results := filepath.Join(filepath.Dir(opts.SourceRoot), "results")
	writeResultForTest(t, results, node.ID, result)
	rep, err := (Local{}).VerifyResults(g, buildDigest, results, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Artifacts) != 1 || rep.Artifacts[0].AbsPath != artifact {
		t.Fatalf("terminal verifier did not rehydrate the authoritative path: %+v", rep.Artifacts)
	}

	if err := os.WriteFile(artifact, []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := (Local{}).VerifyResults(g, buildDigest, results, opts); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("post-job artifact tamper must fail closed, got %v", err)
	}
}

func TestVerifyResultsBindsOnlyTheSelectedProducerSet(t *testing.T) {
	work := filepath.Join(t.TempDir(), "work")
	selected, opts, _ := qualifiedResultOptions(t, filepath.Join(work, "src"))
	fetch := testFetchNode(selected.Needs[0], "https://example.test/source", selected.SourceCommit,
		selected.SourceTreeHash, selected.SourceDest, selected.Toolchain, selected.VendorSpecDigest)
	other := completeTestBuild(ir.Node{
		ID: "build-a-other", Kind: ir.KindBuild, Component: "a-other",
		BuilderType: ir.BuilderOCIHermetic, Toolchain: selected.Toolchain,
		WriteDir: "out-other", ArtifactPath: "out-other/a-other",
		Command: []string{"must-not-run"},
	}, fetch)
	graph := testSourceGraph(fetch, selected, other)
	buildDigest := mustGraphDigest(t, graph)
	observation := profile.ExecutionObservation{
		RunnerDigest: "sha256:" + repeatHex("a"), RunnerOrigin: profile.RunnerPreexisting,
		RuntimeIdentity: "sha256:" + repeatHex("b"),
		Nodes: []profile.NodeExecutionObservation{{
			NodeID: selected.ID, EngineClass: ir.EngineOCIHermetic,
			ToolchainDigest: "sha256:" + repeatHex("c"), SandboxMode: "fixture-sandbox",
		}},
	}
	receipt, err := profile.VerifyExecution(context.Background(), opts.Qualification.HostReceipt,
		opts.Qualification.Profile, []ir.Node{selected}, observation)
	if err != nil {
		t.Fatal(err)
	}
	opts.Only = []string{selected.Component}
	opts.Qualification.Observer = fixedPipelineExecutionObserver{observation: observation}
	artifact := artifactHostPath(opts, selected)
	if err := os.MkdirAll(filepath.Dir(artifact), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte("selected"), 0o755); err != nil {
		t.Fatal(err)
	}
	digest, size, err := sha256File(artifact)
	if err != nil {
		t.Fatal(err)
	}
	writeResultForTest(t, filepath.Join(filepath.Dir(opts.SourceRoot), "results"), selected.ID, ArtifactResult{
		Component: selected.Component, OutputType: selected.OutputType, Toolchain: selected.Toolchain,
		Qualification: ArtifactQualifiedExecution, InputDigest: "sha256:" + repeatHex("b"),
		BuilderType: selected.BuilderType, EngineClass: ir.EngineOCIHermetic, Path: selected.ArtifactPath,
		SHA256: digest, Size: size, ProfileDigest: receipt.ProfileDigest(),
		ExecutionBinding: receipt.ExecutionBinding(),
	})

	report, err := (Local{}).VerifyResults(graph, buildDigest, filepath.Join(filepath.Dir(opts.SourceRoot), "results"), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Artifacts) != 1 || report.Artifacts[0].Component != selected.Component {
		t.Fatalf("terminal receipt did not bind the exact selected producer set: %+v", report.Artifacts)
	}
	if err := report.RequireQualified(context.Background()); err != nil {
		t.Fatalf("selected terminal report lost its authority: %v", err)
	}
}

func TestVerifyResultsRejectsMissingExtraAndForgedFields(t *testing.T) {
	work := filepath.Join(t.TempDir(), "work")
	node, opts, receipt := qualifiedResultOptions(t, filepath.Join(work, "src"))
	g, buildDigest := qualifiedResultGraph(t, node)
	results := filepath.Join(filepath.Dir(opts.SourceRoot), "results")
	if err := os.MkdirAll(results, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := (Local{}).VerifyResults(g, buildDigest, results, opts); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("missing producer result must fail closed, got %v", err)
	}

	forged := ArtifactResult{
		Component: "other", OutputType: node.OutputType, Toolchain: node.Toolchain,
		Qualification: ArtifactQualifiedExecution,
		InputDigest:   "sha256:" + repeatHex("b"), BuilderType: ir.BuilderOCIHermetic,
		EngineClass: ir.EngineOCIHermetic, Path: node.ArtifactPath, SHA256: "sha256:" + repeatHex("c"),
		ProfileDigest: receipt.ProfileDigest(), ExecutionBinding: receipt.ExecutionBinding(),
	}
	unqualified := forged
	unqualified.Component = node.Component
	unqualified.Qualification = ArtifactUnqualified
	writeResultForTest(t, results, node.ID, unqualified)
	if _, err := (Local{}).VerifyResults(g, buildDigest, results, opts); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("explicitly unqualified exec-node result must fail terminal verification, got %v", err)
	}
	writeResultForTest(t, results, node.ID, forged)
	if _, err := (Local{}).VerifyResults(g, buildDigest, results, opts); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("forged result identity must fail closed, got %v", err)
	}
	forged.Component = node.Component
	forged.SHA256 = repeatHex("c") // a bare hex string is not a content-addressed identity
	writeResultForTest(t, results, node.ID, forged)
	if _, err := (Local{}).VerifyResults(g, buildDigest, results, opts); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("unqualified result digest must fail closed, got %v", err)
	}
	if err := os.WriteFile(filepath.Join(results, "extra.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (Local{}).VerifyResults(g, buildDigest, results, opts); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("extra result must fail exact-set verification, got %v", err)
	}
	resultPath := filepath.Join(results, sanitizeName(node.ID)+".json")
	if err := os.Remove(filepath.Join(results, "extra.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultPath, []byte(`{"component":"c","component":"c"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (Local{}).VerifyResults(g, buildDigest, results, opts); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("duplicate result members must fail exact decoding, got %v", err)
	}
}

func TestTerminalTreeTraversalRejectsIntermediateSymlink(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, "host-secret"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "blobs")); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshotRegularTree(root); err == nil {
		t.Fatal("descriptor-relative terminal traversal followed an intermediate symlink")
	}
}

func writeResultForTest(t *testing.T, dir, nodeID string, result ArtifactResult) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := canonical.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sanitizeName(nodeID)+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func repeatHex(v string) string {
	out := ""
	for i := 0; i < 64; i++ {
		out += v
	}
	return out
}
