package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/profile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/trust"
	"github.com/urfave/cli/v2"
	"golang.org/x/sys/unix"
)

func TestWriteExecutionBundlePublishesExactBytesAndCommitManifest(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "workflow.yml")
	fixture := newExecutionBundleFixture(t, out)
	if err := writeExecutionBundle(context.Background(), out, fixture.emitted, fixture.nodes, fixture.transportPlan, fixture.manifest); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string][]byte{
		out:                                    fixture.emitted,
		filepath.Join(dir, pipeline.NodesFile): fixture.nodes,
		filepath.Join(dir, pipeline.TransportPlanFile): fixture.transportPlan,
		bundleManifestPath(out):                        fixture.manifest,
	} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(want) {
			t.Fatalf("bundle member %s = %q, want %q: %v", path, got, want, err)
		}
	}
	if err := pipeline.VerifyManifest(fixture.manifest, "github", "r", fixture.recipeDigest, out, fixture.emitted, fixture.nodes, fixture.transportPlan); err != nil {
		t.Fatal(err)
	}
	var decoded pipeline.BundleManifest
	if err := json.Unmarshal(fixture.manifest, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Members) != 3 {
		t.Fatalf("commit manifest has %d members, want 3", len(decoded.Members))
	}
	for _, member := range decoded.Members {
		if member.MediaType == "" || member.SHA256 == "" || member.Mode == 0 {
			t.Fatalf("manifest member lacks exact type/digest/mode binding: %+v", member)
		}
	}
	lockInfo, err := os.Lstat(out + ".lease")
	if err != nil || !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm()&0o077 != 0 {
		t.Fatalf("persistent bundle lock is not an owner-only regular inode: %v (%v)", lockInfo, err)
	}
}

func TestBundleRejectsUnsupportedProviderSpecificSurfacesBeforeResolution(t *testing.T) {
	for _, args := range [][]string{
		{"nkdctl", "bundle", "--engine", "gitlab", "--meta-lint", "--data", "must-not-be-read"},
		{"nkdctl", "bundle", "--engine", "gitlab", "--extensions", "must-not-be-read.yaml", "--data", "must-not-be-read"},
		{"nkdctl", "bundle", "--engine", "gitlab", "--airgap", "--data", "must-not-be-read"},
	} {
		app := &cli.App{Commands: []*cli.Command{cmdBundle()}}
		err := app.Run(args)
		want := nkderr.CodeEngineCapability
		if containsString(args, "--airgap") {
			want = nkderr.CodeUnsupported
		}
		if cliErrorCode(err) != want {
			t.Fatalf("unsupported provider surface did not fail closed before catalog/I/O: args=%v err=%v", args, err)
		}
	}
}

func TestBundleRejectsUnknownEngineBeforeResolution(t *testing.T) {
	app := &cli.App{Commands: []*cli.Command{cmdBundle()}}
	err := app.Run([]string{"nkdctl", "bundle", "--engine", "unknown", "--data", "must-not-be-read"})
	if cliErrorCode(err) != nkderr.CodeUnsupported {
		t.Fatalf("unknown engine reached catalog resolution: %v", err)
	}
}

func TestBundleRejectsUnavailableEnginesBeforeResolution(t *testing.T) {
	for _, engine := range []string{"github", "tekton", "argo"} {
		t.Run(engine, func(t *testing.T) {
			app := &cli.App{Commands: []*cli.Command{cmdBundle()}}
			err := app.Run([]string{
				"nkdctl", "bundle", "--engine", engine, "--data", "must-not-be-read",
			})
			if cliErrorCode(err) != nkderr.CodeEngineCapability {
				t.Fatalf("unavailable engine reached verifier or catalog I/O: %v", err)
			}
		})
	}
}

func TestBundleAdmitsVerifierInputsBeforeCatalogIO(t *testing.T) {
	app := &cli.App{Commands: []*cli.Command{cmdBundle()}}
	err := app.Run([]string{"nkdctl", "bundle", "--engine", "gitlab", "--data", "must-not-be-read"})
	if cliErrorCode(err) != nkderr.CodeUnpinnedInput {
		t.Fatalf("missing verifier pin reached catalog resolution: %v", err)
	}
	err = app.Run([]string{"nkdctl", "bundle", "--engine", "gitlab", "--check"})
	if cliErrorCode(err) != nkderr.CodeUsage {
		t.Fatalf("bundle --check without --out code = %q, want usage: %v", cliErrorCode(err), err)
	}
}

func TestBundleRequiresAuthenticatedRunnerProfileBeforeCatalogIO(t *testing.T) {
	verifierCalls := 0
	app := &cli.App{Commands: []*cli.Command{cmdBundleWithProfileVerifier(cliVerifierFunc(
		func(context.Context, trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
			verifierCalls++
			return cliEvidence(profileSigner, profileIssuer), nil
		},
	))}}
	err := app.Run([]string{
		"nkdctl", "bundle", "--engine", "gitlab", "--data", "must-not-be-read",
		"--cosign", "tools/cosign", "--cosign-digest", verifyTestDigest('1'),
		"--expected-digests", "evidence/vector.json",
		"--certificate", "evidence/vector.pem", "--bundle", "evidence/vector.bundle",
		"--trusted-root", "evidence/vector-root.json", "--trusted-root-digest", verifyTestDigest('2'),
		"--identity", vectorSigner, "--issuer", vectorIssuer,
		// All runner-profile material except the signed profile itself is present.
		// Admission must reject that missing authority without calling the verifier
		// or attempting to load --data.
		"--profile-certificate", "evidence/profile.pem", "--profile-bundle", "evidence/profile.bundle",
		"--profile-trusted-root", "evidence/profile-root.json",
		"--profile-trusted-root-digest", verifyTestDigest('3'),
		"--profile-identity", profileSigner, "--profile-issuer", profileIssuer,
	})
	if cliErrorCode(err) != nkderr.CodeUntrustedProfile {
		t.Fatalf("missing signed runner profile reached catalog resolution: %v", err)
	}
	if verifierCalls != 0 {
		t.Fatalf("missing runner profile called verifier %d time(s)", verifierCalls)
	}
}

func TestBundleRunnerPinUsesAuthenticatedSnapshotAfterProfilePathSubstitution(t *testing.T) {
	original := profile.Embedded("amd64")
	wantRunner := verifyTestDigest('4')
	original.Repin(profile.FieldRunnerDigest, wantRunner, true)
	originalBytes, err := original.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	changed := original
	changedRunner := verifyTestDigest('5')
	changed.Repin(profile.FieldRunnerDigest, changedRunner, true)
	changedBytes, err := changed.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	profilePath := filepath.Join(dir, "runner-profile.json")
	if err := os.WriteFile(profilePath, originalBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	rootBytes := []byte(`{"trustedRoot":"fixture"}`)
	rootPath := filepath.Join(dir, "trusted-root.json")
	if err := os.WriteFile(rootPath, rootBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	rootSum := sha256.Sum256(rootBytes)
	rootDigest := fmt.Sprintf("sha256:%x", rootSum)

	flags := flag.NewFlagSet("bundle-runner-profile", flag.ContinueOnError)
	for name, value := range map[string]string{
		"profile": profilePath, "profile-signature": filepath.Join(dir, "profile.sig"),
		"profile-certificate":  filepath.Join(dir, "profile.pem"),
		"profile-bundle":       filepath.Join(dir, "profile.bundle"),
		"profile-trusted-root": rootPath, "profile-trusted-root-digest": rootDigest,
		"profile-identity": profileSigner, "profile-issuer": profileIssuer,
		"cosign": "tools/cosign", "cosign-digest": verifyTestDigest('6'),
	} {
		flags.String(name, value, "")
	}
	cliContext := cli.NewContext(&cli.App{}, flags, nil)
	cliContext.Context = context.Background()

	verifier := cliVerifierFunc(func(_ context.Context, request trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
		if request.Role != trust.RoleBuilderProfile {
			t.Fatalf("runner profile authenticated under role %q", request.Role)
		}
		if !bytes.Equal(request.ExactBytes, originalBytes) {
			t.Fatalf("verifier received substituted profile bytes: %s", request.ExactBytes)
		}
		replacement := filepath.Join(dir, "replacement.json")
		if err := os.WriteFile(replacement, changedBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, profilePath); err != nil {
			t.Fatal(err)
		}
		return cliEvidence(profileSigner, profileIssuer), nil
	})

	gotRunner, err := authenticateBundleRunnerDigest(cliContext, verifier)
	if err != nil {
		t.Fatalf("authenticating runner profile: %v", err)
	}
	if gotRunner != wantRunner {
		t.Fatalf("runner pin = %s, want authenticated snapshot pin %s", gotRunner, wantRunner)
	}
	if gotRunner == changedRunner {
		t.Fatal("substituted authored profile selected runner authority")
	}
}

func TestCatalogUpdateCIFailsBeforeOutputOrCatalogIO(t *testing.T) {
	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	t.Cleanup(func() { os.Stdout = oldStdout })
	app := &cli.App{Commands: []*cli.Command{cmdCatalog()}}
	runErr := app.Run([]string{"nkdctl", "catalog", "update-ci", "--dir", "must-not-be-read"})
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = oldStdout
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if cliErrorCode(runErr) != nkderr.CodeEngineCapability {
		t.Fatalf("update-ci code = %q, want %q: %v",
			cliErrorCode(runErr), nkderr.CodeEngineCapability, runErr)
	}
	if len(output) != 0 {
		t.Fatalf("unsupported update-ci emitted bytes: %q", output)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestWriteExecutionBundleReusesUnlockedStaleLockInode(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "workflow.yml")
	fixture := newExecutionBundleFixture(t, out)
	command := exec.Command(os.Args[0], "-test.run=^TestExecutionBundleLockCrashHelper$")
	command.Env = append(os.Environ(), "NKD_EXECUTION_BUNDLE_LOCK_CRASH_HELPER="+out+".lease")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("crash lock helper: %v: %s", err, output)
	}
	if err := writeExecutionBundle(context.Background(), out, fixture.emitted, fixture.nodes, fixture.transportPlan, fixture.manifest); err != nil {
		t.Fatalf("unlocked stale lock inode prevented recovery: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil || !bytes.Equal(got, fixture.emitted) {
		t.Fatalf("recovered bundle = %q, want %q: %v", got, fixture.emitted, err)
	}
	if info, err := os.Lstat(out + ".lease"); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("reusable lock inode was removed or replaced: %v (%v)", info, err)
	}
}

func TestExecutionBundleLockCrashHelper(t *testing.T) {
	lockPath := os.Getenv("NKD_EXECUTION_BUNDLE_LOCK_CRASH_HELPER")
	if lockPath == "" {
		return
	}
	if _, err := acquireExecutionBundleLock(context.Background(), lockPath); err != nil {
		t.Fatal(err)
	}
	// Deliberately bypass the release closure and file Close. Process death is the
	// recovery event and must leave an unlocked, reusable directory entry.
	os.Exit(0)
}

func TestWriteExecutionBundleActiveCrossProcessLockWaitsForCancellation(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "workflow.yml")
	fixture := newExecutionBundleFixture(t, out)
	lockPath := out + ".lease"
	command := exec.Command(os.Args[0], "-test.run=^TestExecutionBundleLockProcessHelper$")
	command.Env = append(os.Environ(), "NKD_EXECUTION_BUNDLE_LOCK_HELPER="+lockPath)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = stdin.Close()
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	ready := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err == nil && line != "ready\n" {
			err = fmt.Errorf("lock helper readiness = %q", line)
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("waiting for lock helper: %v: %s", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("lock helper did not become ready: %s", stderr.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	err = writeExecutionBundle(ctx, out, fixture.emitted, fixture.nodes, fixture.transportPlan, fixture.manifest)
	if cliErrorCode(err) != nkderr.CodeTimeout || ctx.Err() == nil {
		t.Fatalf("active cross-process lock code = %q, want %q after cancellation (%v)",
			cliErrorCode(err), nkderr.CodeTimeout, err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("canceled contending writer published pipeline bytes: %v", err)
	}
	if temps, err := filepath.Glob(filepath.Join(dir, ".nkd-bundle-*.tmp")); err != nil || len(temps) != 0 {
		t.Fatalf("canceled contending writer left staged files: %v (%v)", temps, err)
	}

	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("lock helper exit: %v: %s", err, stderr.String())
	}
	waited = true
	if err := writeExecutionBundle(context.Background(), out, fixture.emitted, fixture.nodes, fixture.transportPlan, fixture.manifest); err != nil {
		t.Fatalf("kernel-released cross-process lock prevented recovery: %v", err)
	}
}

func TestExecutionBundleLockProcessHelper(t *testing.T) {
	lockPath := os.Getenv("NKD_EXECUTION_BUNDLE_LOCK_HELPER")
	if lockPath == "" {
		return
	}
	release, err := acquireExecutionBundleLock(context.Background(), lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := fmt.Fprintln(os.Stdout, "ready"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
}

func TestWriteExecutionBundleCanceledBeforeFilesystemIO(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "must-not-be-created")
	out := filepath.Join(dir, "workflow.yml")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := writeExecutionBundle(ctx, out, nil, nil, nil, nil)
	if cliErrorCode(err) != nkderr.CodeTimeout {
		t.Fatalf("pre-canceled bundle write code = %q, want %q (%v)",
			cliErrorCode(err), nkderr.CodeTimeout, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("pre-canceled bundle write touched output directory: %v", err)
	}
}

func TestBundleCheckRejectsOversizedCommittedMemberWithoutReadingIt(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "workflow.yml")
	fixture := newExecutionBundleFixture(t, out)
	file, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxExecutionBundleMemberBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	err = bundleCheck(context.Background(), out, "github", "r", fixture.recipeDigest,
		fixture.emitted, fixture.nodes, fixture.transportPlan, false)
	if cliErrorCode(err) != nkderr.CodePipelineDrift {
		t.Fatalf("oversized committed bundle code = %q, want %q: %v",
			cliErrorCode(err), nkderr.CodePipelineDrift, err)
	}
}

func TestBundleCheckCanceledBeforeFilesystemIO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "must-not-be-read")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := bundleCheck(ctx, path, "github", "r", "sha256:"+strings.Repeat("a", 64),
		[]byte("pipeline"), []byte("nodes"), []byte("transport"), false)
	if cliErrorCode(err) != nkderr.CodeTimeout {
		t.Fatalf("canceled bundle check code = %q, want %q: %v",
			cliErrorCode(err), nkderr.CodeTimeout, err)
	}
}

func TestExecutionBundleReadersRejectLinksAndFIFOsWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "member-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readExecutionBundleMember(context.Background(), link, 64); cliErrorCode(err) != nkderr.CodePipelineDrift {
		t.Fatalf("symlink bundle read code = %q, want %q: %v",
			cliErrorCode(err), nkderr.CodePipelineDrift, err)
	}
	if _, err := bundleFileState(context.Background(), link); cliErrorCode(err) != nkderr.CodeWriteConflict {
		t.Fatalf("symlink bundle state code = %q, want %q: %v",
			cliErrorCode(err), nkderr.CodeWriteConflict, err)
	}

	fifo := filepath.Join(root, "member-fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := readExecutionBundleMember(ctx, fifo, 64); cliErrorCode(err) != nkderr.CodePipelineDrift {
		t.Fatalf("FIFO bundle read code = %q, want %q without blocking: %v",
			cliErrorCode(err), nkderr.CodePipelineDrift, err)
	}
	app := &cli.App{Commands: []*cli.Command{cmdExecNode()}}
	err := app.RunContext(ctx, []string{"nkd-exec", "exec-node", "--nodes-digest", "sha256:" + strings.Repeat("a", 64), fifo})
	if cliErrorCode(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("exec-node FIFO code = %q, want %q without blocking: %v",
			cliErrorCode(err), nkderr.CodeInvalidCatalog, err)
	}
}

type executionBundleFixture struct {
	emitted       []byte
	nodes         []byte
	transportPlan []byte
	manifest      []byte
	recipeDigest  string
}

func newExecutionBundleFixture(t *testing.T, out string) executionBundleFixture {
	t.Helper()
	graph := bundleFixtureGraph()
	nodes, err := pipeline.NodesJSON(graph)
	if err != nil {
		t.Fatal(err)
	}
	transportPlan, err := pipeline.TransportPlanJSON(graph)
	if err != nil {
		t.Fatal(err)
	}
	recipeDigest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	emitted := []byte("# recipeDigest: " + recipeDigest + "\n# nodesDigest: " + pipeline.FileDigest(nodes) + "\n# transportPlanDigest: " + pipeline.FileDigest(transportPlan) + "\n# bundleDigest: sha256:bundle\njobs: {}\n")
	manifest, err := pipeline.ManifestJSON("github", "r", recipeDigest, out, emitted, nodes, transportPlan)
	if err != nil {
		t.Fatal(err)
	}
	return executionBundleFixture{
		emitted: emitted, nodes: nodes, transportPlan: transportPlan,
		manifest: manifest, recipeDigest: recipeDigest,
	}
}

func bundleFixtureGraph() *ir.Graph {
	return &ir.Graph{
		SchemaVersion: ir.SchemaVersion, RecipeID: "r",
		Criteria: criteria.Criteria{OS: "ubuntu", Arch: "amd64"},
		Nodes: []ir.Node{
			{ID: "pull-artifact", Kind: ir.KindPull, Component: "artifact", OutputType: "raw",
				Artifact: "https://example.test/artifact", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Platform: "linux/amd64", Network: ir.NetworkDefault, ArtifactPath: "artifact", Version: "1.0.0"},
			{ID: "verify", Kind: ir.KindVerify, Needs: []string{"pull-artifact"}},
		},
	}
}
