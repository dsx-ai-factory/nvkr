package pipeline

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/profile"
)

type fixedPipelineExecutionObserver struct {
	observation profile.ExecutionObservation
	err         error
}

type pipelineExecutionObserverFunc func(context.Context, ExecutionObservationRequest) (profile.ExecutionObservation, error)

func (observe pipelineExecutionObserverFunc) ObserveExecution(ctx context.Context, request ExecutionObservationRequest) (profile.ExecutionObservation, error) {
	return observe(ctx, request)
}

func executionErrorCode(err error) nkderr.Code {
	var coded *nkderr.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

func (observer fixedPipelineExecutionObserver) ObserveExecution(context.Context, ExecutionObservationRequest) (profile.ExecutionObservation, error) {
	return observer.observation, observer.err
}

func qualificationFixture(t *testing.T) (profile.BuilderProfile, profile.HostPreflightReceipt, []ir.Node, profile.ExecutionReceipt) {
	t.Helper()
	p := profile.BuilderProfile{
		SchemaVersion: profile.SchemaVersion, Identity: "qualified-local", Version: "1",
		HostControlClass: profile.OperatorControlled, Provides: []string{ir.BuilderOCIHermetic},
		Fields: []profile.Field{
			{Name: profile.FieldTargetArch, Value: "amd64", HardFail: true, MeasurementStage: profile.MeasurementHost},
			{Name: profile.FieldRunnerDigest, Value: "sha256:" + repeatHex("a"), HardFail: true, MeasurementStage: profile.MeasurementExecution},
			{Name: profile.FieldRuntimeIdentity, Value: "sha256:" + repeatHex("b"), HardFail: true, MeasurementStage: profile.MeasurementExecution},
			{Name: profile.FieldOCISandboxMode, Value: "fixture-sandbox", HardFail: true, MeasurementStage: profile.MeasurementExecution},
		},
	}
	toolchain := "registry.example/toolchain@sha256:" + repeatHex("c")
	fixtureFetch := testFetchNode("fetch-z", "https://example.test/source", testSourceCommit, testSourceTree, "source", toolchain, "sha256:"+repeatHex("d"))
	nodes := []ir.Node{completeTestBuild(ir.Node{
		ID: "build-z-source", Kind: ir.KindBuild, Component: "z-source",
		BuilderType: ir.BuilderOCIHermetic,
		Toolchain:   toolchain,
		Network:     ir.NetworkNone, SourceReadOnly: true, WriteDir: "out",
		Command: []string{"must-not-run"}, ArtifactPath: "out/z-source",
	}, fixtureFetch)}
	host, divergences, err := p.VerifyHost(context.Background(), map[string]string{profile.FieldTargetArch: "amd64"})
	if err != nil || len(divergences) != 0 {
		t.Fatalf("host fixture: %v %v", err, divergences)
	}
	receipt, err := profile.VerifyExecution(context.Background(), host, p, nodes, profile.ExecutionObservation{
		RunnerDigest: "sha256:" + repeatHex("a"), RunnerOrigin: profile.RunnerPreexisting,
		RuntimeIdentity: "sha256:" + repeatHex("b"),
		Nodes: []profile.NodeExecutionObservation{{
			NodeID: "build-z-source", EngineClass: ir.EngineOCIHermetic,
			ToolchainDigest: "sha256:" + repeatHex("c"), SandboxMode: "fixture-sandbox",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return p, host, nodes, receipt
}

func qualifiedTerminalReport(t *testing.T) (*Report, string) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "work")
	node, opts, receipt := qualifiedResultOptions(t, filepath.Join(work, "src"))
	graph, buildDigest := qualifiedResultGraph(t, node)
	artifact := artifactHostPath(opts, node)
	if err := os.MkdirAll(filepath.Dir(artifact), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte("terminally-observed"), 0o755); err != nil {
		t.Fatal(err)
	}
	digest, size, err := sha256File(artifact)
	if err != nil {
		t.Fatal(err)
	}
	writeResultForTest(t, filepath.Join(filepath.Dir(opts.SourceRoot), "results"), node.ID, ArtifactResult{
		Component: node.Component, OutputType: node.OutputType, Toolchain: node.Toolchain,
		Qualification: ArtifactQualifiedExecution, InputDigest: "sha256:" + repeatHex("f"),
		BuilderType: node.BuilderType, EngineClass: ir.EngineOCIHermetic, Path: node.ArtifactPath,
		SHA256: digest, Size: size, ProfileDigest: receipt.ProfileDigest(),
		ExecutionBinding: receipt.ExecutionBinding(),
	})
	report, err := (Local{}).VerifyResults(graph, buildDigest, filepath.Join(filepath.Dir(opts.SourceRoot), "results"), opts)
	if err != nil {
		t.Fatal(err)
	}
	return report, artifact
}

func TestQualifiedReportRequiresTerminalReceiptAndRejectsMutation(t *testing.T) {
	report, _ := qualifiedTerminalReport(t)
	if err := report.RequireQualified(context.Background()); err != nil {
		t.Fatalf("terminal report did not retain its receipt binding: %v", err)
	}
	report.Artifacts[0].SHA256 = "sha256:" + repeatHex("2")
	if executionErrorCode(report.RequireQualified(context.Background())) != nkderr.CodeVerifyFailed {
		t.Fatal("artifact mutation preserved report authority")
	}
}

func TestTerminalReceiptRejectsPostVerificationReplacementAndSymlink(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{
			name: "same-byte inode replacement",
			mutate: func(t *testing.T, artifact string) {
				raw, err := os.ReadFile(artifact)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(artifact, artifact+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(artifact, raw, 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "host symlink",
			mutate: func(t *testing.T, artifact string) {
				target := filepath.Join(t.TempDir(), "host-file")
				if err := os.WriteFile(target, []byte("terminally-observed"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(artifact, artifact+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, artifact); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			report, artifact := qualifiedTerminalReport(t)
			test.mutate(t, artifact)
			if executionErrorCode(report.RequireQualified(context.Background())) != nkderr.CodeVerifyFailed {
				t.Fatal("post-terminal artifact replacement retained qualified authority")
			}
		})
	}
}

func TestLocalQualificationFailurePrecedesArtifactWork(t *testing.T) {
	p, host, nodes, _ := qualificationFixture(t)
	fetch := testFetchNode("fetch-z", "https://example.invalid/source", nodes[0].SourceCommit,
		nodes[0].SourceTreeHash, nodes[0].SourceDest, nodes[0].Toolchain, nodes[0].VendorSpecDigest)
	graph := testSourceGraph(fetch, nodes[0])
	digest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	sourceRoot := t.TempDir()
	_, err = (Local{}).Run(graph, digest, RunOptions{
		Ctx: context.Background(), SourceRoot: sourceRoot, Runtime: os.Args[0],
		Qualification: &QualifiedExecution{
			Profile: p, HostReceipt: host,
		},
	})
	if executionErrorCode(err) != nkderr.CodePreflightFailed || len(graph.Nodes) != 3 {
		t.Fatalf("missing observation did not fail before artifact work: %v", err)
	}
	if _, statErr := os.Stat(ArtifactPath(RunOptions{SourceRoot: sourceRoot}, nodes[0])); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("artifact work occurred after failed execution observation: %v", statErr)
	}
}

func TestStageTwoObservationRunsAfterTrustedFetchMaterialization(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("qualified local execution requires a Linux sealed runtime descriptor")
	}
	repository := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.name", "NKD Test"},
		{"config", "user.email", "nkd@example.test"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repository}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repository, "trusted-input"), []byte("materialized"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "trusted-input"}, {"-c", "commit.gpgsign=false", "commit", "-qm", "fixture"}} {
		if out, err := exec.Command("git", append([]string{"-C", repository}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	commit, err := gitOut(context.Background(), repository, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	tree, err := gitOut(context.Background(), repository, "rev-parse", "HEAD^{tree}")
	if err != nil {
		t.Fatal(err)
	}

	p, host, nodes, _ := qualificationFixture(t)
	const destination = "source"
	fetch := ir.Node{
		ID: "fetch-z", Kind: ir.KindFetch, Repo: repository, Commit: commit,
		TreeHash: tree, Dest: destination, VendorImage: nodes[0].Toolchain,
		Network: ir.NetworkDefault,
	}
	fetch.VendorSpecDigest, err = fetchVendorSpecDigest(fetch)
	if err != nil {
		t.Fatal(err)
	}
	build := nodes[0]
	build.Needs = []string{fetch.ID}
	build.SourceDest = destination
	build.SourceCommit = commit
	build.SourceTreeHash = tree
	build.VendorSpecDigest = fetch.VendorSpecDigest
	graph := testSourceGraph(fetch, build)
	digest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	sourceRoot := filepath.Join(t.TempDir(), "work", "src")
	observationStopped := errors.New("observation stopped before artifact work")
	observedMaterializedInput := false
	_, err = (Local{}).Run(graph, digest, RunOptions{
		Ctx: context.Background(), SourceRoot: sourceRoot, Runtime: os.Args[0],
		Qualification: &QualifiedExecution{
			Profile: p, HostReceipt: host,
			Observer: pipelineExecutionObserverFunc(func(context.Context, ExecutionObservationRequest) (profile.ExecutionObservation, error) {
				raw, readErr := os.ReadFile(filepath.Join(sourceRoot, destination, "trusted-input"))
				observedMaterializedInput = readErr == nil && string(raw) == "materialized"
				return profile.ExecutionObservation{}, observationStopped
			}),
		},
	})
	if !errors.Is(err, observationStopped) || !observedMaterializedInput {
		t.Fatalf("stage two ran before trusted fetch materialization: observed=%t err=%v", observedMaterializedInput, err)
	}
}

func TestObserveContainerImageMeasuresBareLocalImageID(t *testing.T) {
	want := "sha256:" + repeatHex("c")
	runtimePath := filepath.Join(t.TempDir(), "runtime")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = image ] && [ \"$2\" = inspect ]; then printf '\"" + want + "\"'; exit 0; fi\n" +
		"exit 41\n"
	if err := os.WriteFile(runtimePath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	node := ir.Node{ID: "build-local", Kind: ir.KindBuild, Toolchain: want}
	got, err := observeContainerImage(context.Background(), runtimePath, node)
	if err != nil || got != want {
		t.Fatalf("local image ID was not independently inspected: got=%q err=%v", got, err)
	}
	node.Toolchain = "sha256:" + repeatHex("d")
	if _, err := observeContainerImage(context.Background(), runtimePath, node); executionErrorCode(err) != nkderr.CodePreflightFailed {
		t.Fatalf("runtime-selected image ID mismatch did not fail closed: %v", err)
	}
}

func TestRuntimeIdentityBindsEndpointAndServer(t *testing.T) {
	raw := []byte(`{"Client":{"Version":"1"},"Server":{"Version":"1"}}`)
	base, err := runtimeIdentityFromOutput("sha256:"+repeatHex("a"), qualifiedRuntimeEndpoint, "daemon-a", raw)
	if err != nil {
		t.Fatal(err)
	}
	otherEndpoint, err := runtimeIdentityFromOutput("sha256:"+repeatHex("a"), "unix:///run/other.sock", "daemon-a", raw)
	if err != nil {
		t.Fatal(err)
	}
	otherServer, err := runtimeIdentityFromOutput("sha256:"+repeatHex("a"), qualifiedRuntimeEndpoint, "daemon-b", raw)
	if err != nil {
		t.Fatal(err)
	}
	if base == otherEndpoint || base == otherServer || otherEndpoint == otherServer {
		t.Fatal("runtime identity omitted its exact endpoint or server identity")
	}
}

func TestRuntimeIdentityRejectsAmbiguousVersionJSON(t *testing.T) {
	_, err := runtimeIdentityFromOutput(
		"sha256:"+repeatHex("a"), qualifiedRuntimeEndpoint, "daemon-a",
		[]byte(`{"Server":{"Version":"trusted"},"Server":{"Version":"attacker"}}`),
	)
	if executionErrorCode(err) != nkderr.CodeEngineCapability {
		t.Fatalf("ambiguous runtime identity JSON did not fail closed: %v", err)
	}
}

func TestRuntimeSnapshotClosesObserveThenExecutePathRace(t *testing.T) {
	original := filepath.Join(t.TempDir(), "runtime")
	if err := os.WriteFile(original, []byte("#!/bin/sh\nprintf original"), 0o755); err != nil {
		t.Fatal(err)
	}
	snapshot, err := snapshotRuntimeExecutable(original)
	if runtime.GOOS != "linux" {
		if executionErrorCode(err) != nkderr.CodeEngineCapability {
			t.Fatalf("non-Linux qualified runtime capture did not fail closed: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.capability.Close()
	if err := os.WriteFile(original, []byte("#!/bin/sh\nprintf replaced"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := heldExecutionOutput(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "original" {
		t.Fatalf("snapshotted execution used replaced runtime bytes: %q", out)
	}
}

func TestQualifiedReportConcurrentValidation(t *testing.T) {
	report, _ := qualifiedTerminalReport(t)
	const workers = 32
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- report.RequireQualified(context.Background())
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestQualifiedReportCloseRevokesShallowCopies(t *testing.T) {
	report, _ := qualifiedTerminalReport(t)
	copy := *report
	if !report.Qualified() || !copy.Qualified() {
		t.Fatal("qualified report fixture lacks authority")
	}
	if err := report.Close(); err != nil {
		t.Fatal(err)
	}
	if err := report.Close(); err != nil {
		t.Fatalf("report close is not idempotent: %v", err)
	}
	for name, candidate := range map[string]*Report{"original": report, "shallow-copy": &copy} {
		if candidate.Qualified() || candidate.QualificationProfileDigest() != "" || candidate.ExecutionBinding() != "" {
			t.Fatalf("%s retained visible authority after close", name)
		}
		if executionErrorCode(candidate.RequireQualified(context.Background())) != nkderr.CodePreflightFailed {
			t.Fatalf("%s retained consumable authority after close", name)
		}
	}
}

func TestQualifiedReportConcurrentCloseIsRaceSafe(t *testing.T) {
	report, _ := qualifiedTerminalReport(t)
	copy := *report
	start := make(chan struct{})
	const workers = 24
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 16 {
				_ = copy.Qualified()
				_ = copy.QualificationProfileDigest()
				_ = copy.ExecutionBinding()
				_ = copy.RequireQualified(context.Background())
			}
		}()
	}
	close(start)
	if err := report.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if copy.Qualified() || executionErrorCode(copy.RequireQualified(context.Background())) != nkderr.CodePreflightFailed {
		t.Fatal("concurrent close did not revoke shared report authority")
	}
}
