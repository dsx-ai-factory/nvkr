package aicr

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

func code(err error) nkderr.Code {
	if e, ok := err.(*nkderr.Error); ok {
		return e.Code
	}
	return ""
}

var demo = Profile{
	Identity: "h100-eks-ubuntu-training", Digest: "sha256:" + strings.Repeat("a", 64),
	GeneratorDigest: "sha256:" + strings.Repeat("b", 64),
	Service:         "eks", Accelerator: "h100", OS: "ubuntu", Intent: "training",
}

func TestRecipeArgs(t *testing.T) {
	got := strings.Join(RecipeArgs(demo, "/w/recipe.yaml"), " ")
	want := "recipe --service eks --accelerator h100 --os ubuntu --intent training -o /w/recipe.yaml"
	if got != want {
		t.Fatalf("RecipeArgs:\n got %q\nwant %q", got, want)
	}
	// optional criteria are omitted; platform is added when set.
	p := demo
	p.Platform = "kubeflow"
	if !strings.Contains(strings.Join(RecipeArgs(p, "r"), " "), "--platform kubeflow") {
		t.Fatal("platform flag missing when set")
	}
}

func TestEvidenceAndBundleArgs(t *testing.T) {
	if got := strings.Join(EvidenceDigestArgs("/w/recipe.yaml"), " "); got != "evidence digest --recipe /w/recipe.yaml" {
		t.Fatalf("EvidenceDigestArgs: %q", got)
	}
	got := strings.Join(BundleArgs("/w/recipe.yaml", "./bundle", "helm", true, []string{"--repo", "http://g"}), " ")
	want := "bundle --recipe /w/recipe.yaml --output ./bundle --deployer helm --attest --repo http://g"
	if got != want {
		t.Fatalf("BundleArgs:\n got %q\nwant %q", got, want)
	}
}

func TestEvidenceDigestOutputIsStrict(t *testing.T) {
	body := strings.Repeat("a", 64)
	for _, raw := range []string{body + "\n", "sha256:" + body + "\n"} {
		got, err := parseEvidenceDigest([]byte(raw))
		if err != nil || got != body {
			t.Fatalf("parse %q: got %q err=%v", raw, got, err)
		}
	}
	for _, raw := range []string{"digest=" + body, body + "\n" + body, "sha256:short"} {
		if _, err := parseEvidenceDigest([]byte(raw)); code(err) != nkderr.CodeBuildFailed {
			t.Fatalf("malformed/ambiguous output %q must fail closed, got %v", raw, err)
		}
	}
}

// Preview includes the final semantic recipe recheck that closes the bundle mutation
// boundary; --print must not conceal an authority-bearing invocation.
func TestPreviewIncludesFinalRecipeVerification(t *testing.T) {
	steps := Preview("aicr", demo, Options{OutputDir: "./b"})
	if len(steps) != 4 || steps[0][1] != "recipe" || steps[1][1] != "evidence" ||
		steps[2][1] != "bundle" || steps[3][1] != "evidence" {
		t.Fatalf("preview must be recipe→evidence→bundle→evidence: %v", steps)
	}
	if steps[0][0] != "aicr" {
		t.Fatalf("preview must prepend the binary: %v", steps[0])
	}
}

// nkdctl never fabricates a bundle: with no aicr binary the render fails closed.
func TestCLIBundlerFailsClosedWithoutBinary(t *testing.T) {
	b := CLIBundler{Binary: "aicr-does-not-exist-xyz"}
	if b.Available() {
		t.Fatal("Available() must be false for a missing binary")
	}
	outputParent := filepath.Join(t.TempDir(), "must-not-be-created")
	output := filepath.Join(outputParent, "bundle")
	_, err := b.Bundle(context.Background(), demo, Options{OutputDir: output}, io.Discard, io.Discard)
	wantCode := nkderr.CodeUnsupported
	if runtime.GOOS != "linux" {
		wantCode = nkderr.CodeEngineCapability
	}
	if code(err) != wantCode {
		t.Fatalf("missing aicr returned %v, want %s", err, wantCode)
	}
	if _, statErr := os.Lstat(outputParent); !os.IsNotExist(statErr) {
		t.Fatalf("missing generator mutated output filesystem before admission: %v", statErr)
	}
}

func TestCLIBundlerFailsClosedWhenHeldExecutionIsUnsupported(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin deliberately cannot execute anonymous descriptors")
	}
	binary := writeFakeAICR(t, "normal")
	profile := demo
	profile.GeneratorDigest = digestFileBytes(t, binary)
	outputParent := filepath.Join(t.TempDir(), "must-not-be-created")
	output := filepath.Join(outputParent, "published")
	_, err := (CLIBundler{Binary: binary}).BundleQualified(
		context.Background(), profile, Options{OutputDir: output}, io.Discard, io.Discard)
	if code(err) != nkderr.CodeEngineCapability {
		t.Fatalf("unsupported held execution returned %v, want CodeEngineCapability", err)
	}
	if _, statErr := os.Lstat(output); !os.IsNotExist(statErr) {
		t.Fatalf("unsupported held execution produced output: %v", statErr)
	}
	if _, statErr := os.Lstat(outputParent); !os.IsNotExist(statErr) {
		t.Fatalf("unsupported held execution touched output parent: %v", statErr)
	}
}

func TestBundleRejectsIncompleteProfile(t *testing.T) {
	b := CLIBundler{Binary: "aicr-does-not-exist-xyz"}
	_, err := b.Bundle(context.Background(), Profile{Service: "eks"}, Options{}, io.Discard, io.Discard)
	if code(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("incomplete profile must be rejected, got %v", err)
	}
}

func TestBundleRejectsNilContextBeforeExecution(t *testing.T) {
	bundler := CLIBundler{Binary: "aicr-path-must-not-be-resolved"}
	options := Options{OutputDir: filepath.Join(t.TempDir(), "bundle")}
	if _, err := bundler.BundleQualified(nil, demo, options, io.Discard, io.Discard); code(err) != nkderr.CodeUsage { //nolint:staticcheck // verifies the nil-context fail-closed contract
		t.Fatalf("nil qualified-bundle context returned %v, want CodeUsage", err)
	}
	if _, err := bundler.Bundle(nil, demo, options, io.Discard, io.Discard); code(err) != nkderr.CodeUsage { //nolint:staticcheck // verifies the nil-context fail-closed contract
		t.Fatalf("nil compatibility-bundle context returned %v, want CodeUsage", err)
	}
}

func TestProfileValidationFailsClosed(t *testing.T) {
	badDigest := demo
	badDigest.Digest = "sha256:short"
	if code(badDigest.Validate()) != nkderr.CodeUnpinnedSource {
		t.Fatalf("short profile digest must fail as unpinned, got %v", badDigest.Validate())
	}
	badAxis := demo
	badAxis.Accelerator = "future-gpu"
	if code(badAxis.Validate()) != nkderr.CodeInvalidCatalog {
		t.Fatalf("unsupported profile axis must fail catalog validation, got %v", badAxis.Validate())
	}
	if err := demo.Validate(); err != nil {
		t.Fatalf("complete pinned profile must validate: %v", err)
	}
	badGenerator := demo
	badGenerator.GeneratorDigest = "sha256:" + strings.Repeat("A", 64)
	if code(badGenerator.Validate()) != nkderr.CodeUnpinnedSource {
		t.Fatalf("generator digest must be lowercase and exact, got %v", badGenerator.Validate())
	}
}

func TestQualifiedBundleCannotDisableOrRedirectVerification(t *testing.T) {
	b := CLIBundler{Binary: "aicr-does-not-exist-xyz"}
	for _, args := range [][]string{
		{"--recipe", "/tmp/other.yaml"}, {"--recipe=/tmp/other.yaml"}, {"-r", "/tmp/other.yaml"}, {"-r=/tmp/other.yaml"}, {"-r/tmp/other.yaml"},
		{"--output", "/tmp/other"}, {"--output=/tmp/other"}, {"-o", "/tmp/other"}, {"-o=/tmp/other"}, {"-o/tmp/other"},
		{"--deployer", "flux"}, {"--deployer=flux"}, {"-dflux"}, {"--attest=false"}, {"--"},
	} {
		_, err := b.Bundle(context.Background(), demo, Options{OutputDir: filepath.Join(t.TempDir(), "bundle"), ExtraBundleArgs: args}, io.Discard, io.Discard)
		if code(err) != nkderr.CodeInvalidCatalog {
			t.Fatalf("critical override %v must fail before invoking AICR, got %v", args, err)
		}
	}
}

// The unqualified bundler seam is implementable without importing AICR — an SDK-backed adapter
// (or a test double) satisfies it, so it can be injected via the SDK facade.
type fakeSDKBundler struct{ called bool }

func (f *fakeSDKBundler) Available() bool { return true }
func (f *fakeSDKBundler) Bundle(_ context.Context, p Profile, o Options, _, _ io.Writer) (Result, error) {
	f.called = true
	return Result{Profile: p, OutputDir: o.OutputDir, RecipeDigest: strings.TrimPrefix(p.Digest, "sha256:")}, nil
}

func TestBundlerSeamInjectable(t *testing.T) {
	var b UnqualifiedBundler = &fakeSDKBundler{}
	res, err := b.Bundle(context.Background(), demo, Options{OutputDir: "/tmp/x"}, io.Discard, io.Discard)
	if err != nil || res.Profile.Identity != demo.Identity {
		t.Fatalf("seam not injectable: %v %+v", err, res)
	}
	if !b.(*fakeSDKBundler).called {
		t.Fatal("injected bundler not invoked")
	}
}

type forgedBundler struct{ result Result }

func (f forgedBundler) Available() bool { return true }
func (f forgedBundler) Bundle(context.Context, Profile, Options, io.Writer, io.Writer) (Result, error) {
	return f.result, nil
}

func TestUnqualifiedAdapterCannotForgeExecutionAuthority(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"recipe.yaml": "caller: composed\n", "checksums.txt": "caller-composed\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bundleDigest, err := digestBundle(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	forged := Result{
		Profile: demo, RecipeFile: filepath.Join(root, "recipe.yaml"),
		RecipeDigest:    strings.TrimPrefix(demo.Digest, "sha256:"),
		GeneratorDigest: demo.GeneratorDigest, BundleDigest: bundleDigest, OutputDir: root,
		ChecksumsFile: filepath.Join(root, "checksums.txt"),
	}
	var adapter UnqualifiedBundler = forgedBundler{result: forged}
	diagnostic, err := adapter.Bundle(context.Background(), demo, Options{}, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := diagnostic.ValidateUnqualifiedForContext(context.Background(), demo); err != nil {
		t.Fatalf("forgery fixture should be structurally self-consistent: %v", err)
	}
	// Public Result fields can prove only structural consistency. A zero-value
	// QualifiedResult has no private execution receipt, and there is no exported
	// constructor that can attach one to the adapter's Result.
	var attemptedForgery QualifiedResult
	if _, err := attemptedForgery.ValidateFor(context.Background(), demo); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("caller-composed result gained execution authority: %v", err)
	}
	var nilForgery *QualifiedResult
	if _, err := nilForgery.ValidateFor(context.Background(), demo); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("nil qualified result gained execution authority: %v", err)
	}
}

func TestBundleDigestIncludesEmptyDirectoriesAndNormalizedModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission identity uses Unix mode semantics")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "payload.yaml"), []byte("value: pinned\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	base, err := digestBundle(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(root, "empty")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	withDirectory, err := digestBundle(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if withDirectory == base {
		t.Fatal("adding an empty output directory did not change the bundle identity")
	}
	if err := os.Chmod(empty, 0o644); err != nil {
		t.Fatal(err)
	}
	withChangedMode, err := digestBundle(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if withChangedMode == withDirectory {
		t.Fatal("changing an output directory's normalized mode did not change the bundle identity")
	}
	if err := os.Remove(empty); err != nil {
		t.Fatal(err)
	}
	restored, err := digestBundle(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if restored != base {
		t.Fatalf("removing the empty directory did not restore bundle identity: %s != %s", restored, base)
	}
}

func TestQualifiedBundlePinsSnapshotAndBundleTree(t *testing.T) {
	requireShell(t)
	binary := writeFakeAICR(t, "normal")
	profile := demo
	profile.GeneratorDigest = digestFileBytes(t, binary)
	t.Setenv("AICR_RENAME_RESTORE_ORIGINAL", binary)
	recipeMutationMarker := filepath.Join(t.TempDir(), "recipe-mutated")
	t.Setenv("AICR_RECIPE_MUTATION_MARKER", recipeMutationMarker)
	output := filepath.Join(t.TempDir(), "published")

	qualified, err := (CLIBundler{Binary: binary}).BundleQualified(
		context.Background(), profile, Options{OutputDir: output}, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	result, err := qualified.ValidateFor(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.GeneratorDigest != profile.GeneratorDigest {
		t.Fatalf("measured generator digest = %q, want %q", result.GeneratorDigest, profile.GeneratorDigest)
	}
	if !canonical.IsDigest(result.BundleDigest) {
		t.Fatalf("bundle tree receipt is not a digest: %q", result.BundleDigest)
	}
	recomputed, err := digestBundle(context.Background(), output)
	if err != nil || recomputed != result.BundleDigest {
		t.Fatalf("published bundle did not reproduce receipt: got %q err=%v want %q", recomputed, err, result.BundleDigest)
	}
	// The recipe phase replaces the configured pathname with an attacker program;
	// the first evidence phase can restore it only because it ran from the already
	// held executable descriptor rather than reopening that pathname.
	if digestFileBytes(t, binary) != profile.GeneratorDigest {
		t.Fatal("adversarial generator pathname was not restored after held execution")
	}
	if _, err := os.Lstat(recipeMutationMarker); !os.IsNotExist(err) {
		t.Fatalf("held recipe descriptor accepted an adversarial write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(output, "payload", "values.yaml")); err != nil {
		t.Fatalf("atomic output was not published: %v", err)
	}
	if err := result.ValidateUnqualifiedForContext(context.Background(), profile); err != nil {
		t.Fatalf("qualified receipt did not revalidate: %v", err)
	}
	for name, mutate := range map[string]func(*Result){
		"profile": func(receipt *Result) { receipt.Profile.Identity = "substituted" },
		"recipe":  func(receipt *Result) { receipt.RecipeDigest = strings.Repeat("c", 64) },
		"generator": func(receipt *Result) {
			receipt.GeneratorDigest = "sha256:" + strings.Repeat("c", 64)
		},
		"bundle":     func(receipt *Result) { receipt.BundleDigest = "sha256:" + strings.Repeat("c", 64) },
		"output":     func(receipt *Result) { receipt.OutputDir = "" },
		"recipePath": func(receipt *Result) { receipt.RecipeFile = "elsewhere" },
		"checksums":  func(receipt *Result) { receipt.ChecksumsFile = "elsewhere" },
	} {
		t.Run("receipt-"+name, func(t *testing.T) {
			changed := result
			mutate(&changed)
			if code(changed.ValidateUnqualifiedForContext(context.Background(), profile)) != nkderr.CodeVerifyFailed {
				t.Fatalf("caller-composed %s receipt retained authority", name)
			}
		})
	}
	if err := os.WriteFile(filepath.Join(output, "payload", "values.yaml"), []byte("mutated: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := qualified.ValidateFor(context.Background(), profile); code(err) != nkderr.CodeVerifyFailed {
		t.Fatal("post-render bundle mutation retained a qualified receipt")
	}
}

func TestQualifiedBundleRejectsRecipeReplacementByGenerator(t *testing.T) {
	requireShell(t)
	binary := writeFakeAICR(t, "replace-recipe")
	profile := demo
	profile.GeneratorDigest = digestFileBytes(t, binary)
	output := filepath.Join(t.TempDir(), "published")
	if _, err := (CLIBundler{Binary: binary}).BundleQualified(
		context.Background(), profile, Options{OutputDir: output}, io.Discard, io.Discard,
	); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("generator recipe replacement must fail final semantic verification, got %v", err)
	}
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		t.Fatalf("recipe-replaced bundle reached publication: %v", err)
	}
}

func TestBundlePublicationIsAtomicNoReplaceUnderConcurrency(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("this platform deliberately fails closed without atomic no-replace rename")
	}
	parent := t.TempDir()
	destination := filepath.Join(parent, "published")
	stages := []string{filepath.Join(parent, "stage-a"), filepath.Join(parent, "stage-b")}
	for i, stage := range stages {
		if err := os.Mkdir(stage, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stage, "winner"), []byte{byte('a' + i)}, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	start := make(chan struct{})
	results := make(chan error, len(stages))
	var wait sync.WaitGroup
	for _, stage := range stages {
		stage := stage
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			results <- publishDirectoryNoReplace(stage, destination)
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	successes := 0
	failures := 0
	var publicationFailure error
	for err := range results {
		if err == nil {
			successes++
		} else {
			failures++
			publicationFailure = err
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("concurrent no-replace publications = %d successes/%d failures, want 1/1", successes, failures)
	}
	if !errors.Is(publicationFailure, os.ErrExist) {
		t.Fatalf("losing publication failed with %v, want destination-exists", publicationFailure)
	}
	content, err := os.ReadFile(filepath.Join(destination, "winner"))
	if err != nil || (string(content) != "a" && string(content) != "b") {
		t.Fatalf("published tree is incomplete or substituted: %q (%v)", content, err)
	}
	remaining := 0
	for _, stage := range stages {
		if _, err := os.Lstat(stage); err == nil {
			remaining++
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	if remaining != 1 {
		t.Fatalf("losing staging tree count = %d, want 1", remaining)
	}
}

func TestCommitBundleRejectsStageRenameSubstitution(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("this platform deliberately fails closed without atomic no-replace rename")
	}
	parent := t.TempDir()
	stage := filepath.Join(parent, "stage")
	substitute := filepath.Join(parent, "substitute")
	destination := filepath.Join(parent, "published")
	for root, payload := range map[string]string{
		stage:      "validated: true\n",
		substitute: "attacker: substituted\n",
	} {
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "payload.yaml"), []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := digestBundle(context.Background(), stage)
	if err != nil {
		t.Fatal(err)
	}
	verifiedStage := stage + ".verified"
	adversarialPublish := func(source, destination string) error {
		if err := os.Rename(source, verifiedStage); err != nil {
			return err
		}
		if err := os.Rename(substitute, source); err != nil {
			return err
		}
		return publishDirectoryNoReplace(source, destination)
	}
	got, committed, err := commitBundle(
		context.Background(), stage, destination, destination, want, adversarialPublish,
	)
	if !committed {
		t.Fatal("adversarial fixture did not publish the substituted source path")
	}
	if got != "" || code(err) != nkderr.CodeImmutableViolation {
		t.Fatalf("stage rename substitution acquired a bundle receipt: digest=%q err=%v", got, err)
	}
	observed, digestErr := digestBundle(context.Background(), destination)
	if digestErr != nil {
		t.Fatal(digestErr)
	}
	if observed == want {
		t.Fatal("adversarial fixture did not substitute different publication bytes")
	}
}

func TestCommitBundleCancellationLinearizesAtPublication(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("this platform deliberately fails closed without atomic no-replace rename")
	}
	t.Run("canceled before rename publishes nothing", func(t *testing.T) {
		parent := t.TempDir()
		stage := filepath.Join(parent, "stage")
		destination := filepath.Join(parent, "published")
		if err := os.Mkdir(stage, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stage, "payload"), []byte("exact\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		called := false
		_, committed, err := commitBundle(ctx, stage, destination, destination,
			"sha256:"+strings.Repeat("a", 64), func(_, _ string) error {
				called = true
				return nil
			})
		if code(err) != nkderr.CodeTimeout || committed || called {
			t.Fatalf("pre-commit cancellation = committed %v called %v err %v", committed, called, err)
		}
		if _, statErr := os.Lstat(destination); !os.IsNotExist(statErr) {
			t.Fatalf("pre-commit cancellation published output: %v", statErr)
		}
	})

	t.Run("rename commit wins over later cancellation", func(t *testing.T) {
		parent := t.TempDir()
		stage := filepath.Join(parent, "stage")
		destination := filepath.Join(parent, "published")
		if err := os.Mkdir(stage, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stage, "payload"), []byte("exact\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		want, err := digestBundle(context.Background(), stage)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		got, committed, err := commitBundle(ctx, stage, destination, destination, want,
			func(source, target string) error {
				if err := publishDirectoryNoReplace(source, target); err != nil {
					return err
				}
				cancel()
				return nil
			})
		if err != nil || !committed || got != want {
			t.Fatalf("post-rename cancellation = digest %q committed %v err %v", got, committed, err)
		}
		if _, statErr := os.Stat(filepath.Join(destination, "payload")); statErr != nil {
			t.Fatalf("committed bundle is incomplete: %v", statErr)
		}
	})
}

func TestQualifiedResultValidationIsContextFirst(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var result *QualifiedResult
	if _, err := result.ValidateFor(ctx, demo); code(err) != nkderr.CodeTimeout {
		t.Fatalf("canceled validation returned %v, want timeout", err)
	}
	//nolint:staticcheck // The exported boundary must reject a nil context before filesystem I/O.
	if _, err := result.ValidateFor(nil, demo); code(err) != nkderr.CodeUsage {
		t.Fatalf("nil-context validation returned %v, want usage", err)
	}
}

func TestHeldBundleSnapshotPinsRecipeAndSurvivesStageReplacement(t *testing.T) {
	parent := t.TempDir()
	stage := filepath.Join(parent, "stage")
	if err := os.MkdirAll(filepath.Join(stage, "payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	recipeBytes := []byte("kind: Recipe\nsource: held\n")
	for path, content := range map[string][]byte{
		"recipe.yaml":         recipeBytes,
		"checksums.txt":       []byte("verified\n"),
		"payload/values.yaml": []byte("authority: original\n"),
	} {
		if err := os.WriteFile(filepath.Join(stage, filepath.FromSlash(path)), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	recipe, err := heldfile.Open(filepath.Join(stage, "recipe.yaml"), heldfile.Options{
		Label: "test-recipe", MaxBytes: maxGeneratedRecipeBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer recipe.Close()

	wrongRecipe, err := heldfile.FromBytes([]byte("kind: Substituted\n"), heldfile.Options{
		Label: "wrong-recipe", MaxBytes: maxGeneratedRecipeBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := snapshotBundle(context.Background(), stage, wrongRecipe); code(err) != nkderr.CodeVerifyFailed {
		wrongRecipe.Close()
		t.Fatalf("tree with a different recipe row acquired held authority: %v", err)
	}
	wrongRecipe.Close()

	snapshot, err := snapshotBundle(context.Background(), stage, recipe)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	wantDigest := snapshot.digest

	original := stage + ".original"
	if err := os.Rename(stage, original); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stage, "payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string][]byte{
		"recipe.yaml":         []byte("kind: AttackerRecipe\n"),
		"checksums.txt":       []byte("attacker\n"),
		"payload/values.yaml": []byte("authority: attacker\n"),
	} {
		if err := os.WriteFile(filepath.Join(stage, filepath.FromSlash(path)), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	commitTree := filepath.Join(parent, "commit")
	if err := os.Mkdir(commitTree, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.materialize(context.Background(), commitTree); err != nil {
		t.Fatal(err)
	}
	observed, err := digestBundle(context.Background(), commitTree)
	if err != nil {
		t.Fatal(err)
	}
	if observed != wantDigest {
		t.Fatalf("materialized held tree digest = %s, want %s", observed, wantDigest)
	}
	gotRecipe, err := os.ReadFile(filepath.Join(commitTree, "recipe.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotRecipe, recipeBytes) {
		t.Fatalf("materialized recipe = %q, want held bytes %q", gotRecipe, recipeBytes)
	}
	gotPayload, err := os.ReadFile(filepath.Join(commitTree, "payload", "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotPayload) != "authority: original\n" {
		t.Fatalf("materialized payload came from replaced stage: %q", gotPayload)
	}
}

func TestGeneratorDigestMismatchFailsBeforeExecution(t *testing.T) {
	requireShell(t)
	binary := writeFakeAICR(t, "normal")
	output := filepath.Join(t.TempDir(), "published")
	_, err := (CLIBundler{Binary: binary}).Bundle(
		context.Background(), demo, Options{OutputDir: output}, io.Discard, io.Discard)
	if code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("substituted generator must fail verification, got %v", err)
	}
	if _, statErr := os.Lstat(output); !os.IsNotExist(statErr) {
		t.Fatalf("generator mismatch published output: %v", statErr)
	}
}

func TestBundleRejectsNonRegularEntriesAndDoesNotPublish(t *testing.T) {
	requireShell(t)
	for _, mode := range []string{"symlink", "fifo"} {
		t.Run(mode, func(t *testing.T) {
			binary := writeFakeAICR(t, mode)
			profile := demo
			profile.GeneratorDigest = digestFileBytes(t, binary)
			output := filepath.Join(t.TempDir(), "published")
			_, err := (CLIBundler{Binary: binary}).Bundle(
				context.Background(), profile, Options{OutputDir: output}, io.Discard, io.Discard)
			if code(err) != nkderr.CodeVerifyFailed {
				t.Fatalf("%s bundle entry must fail verification, got %v", mode, err)
			}
			if _, statErr := os.Lstat(output); !os.IsNotExist(statErr) {
				t.Fatalf("invalid tree was published: %v", statErr)
			}
		})
	}
}

func TestBundleRequiresChecksumsContract(t *testing.T) {
	requireShell(t)
	binary := writeFakeAICR(t, "no-checksums")
	profile := demo
	profile.GeneratorDigest = digestFileBytes(t, binary)
	output := filepath.Join(t.TempDir(), "published")
	_, err := (CLIBundler{Binary: binary}).Bundle(
		context.Background(), profile, Options{OutputDir: output}, io.Discard, io.Discard)
	if code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("bundle without checksums contract must fail verification, got %v", err)
	}
	if _, statErr := os.Lstat(output); !os.IsNotExist(statErr) {
		t.Fatalf("incomplete tree was published: %v", statErr)
	}
}

func TestBundleTreeDigestIsDeterministicAndMutationSensitive(t *testing.T) {
	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")
	for _, root := range []string{first, second} {
		if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(first, "z"), []byte("z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first, "nested", "a"), []byte("a\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Reverse creation order and vary irrelevant read/write bits.
	if err := os.WriteFile(filepath.Join(second, "nested", "a"), []byte("a\n"), 0o711); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "z"), []byte("z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d1, err := digestBundle(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := digestBundle(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Fatalf("creation order/irrelevant mode bits changed tree identity: %s != %s", d1, d2)
	}
	if err := os.WriteFile(filepath.Join(second, "z"), []byte("mutated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d3, err := digestBundle(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if d3 == d1 {
		t.Fatal("bundle content mutation did not change tree identity")
	}
	if err := os.WriteFile(filepath.Join(second, "z"), []byte("z\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(second, "z"), 0o755); err != nil {
		t.Fatal(err)
	}
	d4, err := digestBundle(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if d4 == d1 {
		t.Fatal("executable-mode mutation did not change tree identity")
	}
}

func TestBundleTreeCanonicalSchemaGolden(t *testing.T) {
	tree := bundleTree{
		SchemaVersion: bundleSchemaVersion,
		Entries: []bundleEntry{{
			Path: "recipe.yaml", Type: "file", SHA256: "sha256:" + strings.Repeat("a", 64), Mode: "0644",
		}},
	}
	gotBytes, err := canonical.Marshal(tree)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes := `{"entries":[{"mode":"0644","path":"recipe.yaml","sha256":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","type":"file"}],"schemaVersion":"nkd.dev/aicr-bundle/v2"}`
	if string(gotBytes) != wantBytes {
		t.Fatalf("AICR bundle tree schema changed:\n got %s\nwant %s", gotBytes, wantBytes)
	}
	gotDigest, err := canonical.Digest(canonical.KindAICRBundle, tree)
	if err != nil {
		t.Fatal(err)
	}
	if want := "sha256:24a524adfa5497d495da6ce359462e8b1804068f0e7c325d41c5ed2456d04902"; gotDigest != want {
		t.Fatalf("AICR bundle domain/schema digest changed: got %s want %s", gotDigest, want)
	}
}

func TestBundleTreeRejectsPlatformAmbiguousPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("backslash is a path separator on Windows")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, `ambiguous\name`), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := digestBundle(context.Background(), root); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("backslash-bearing bundle path must fail closed, got %v", err)
	}
}

func TestBundleRejectsStaleDestinationBeforeGeneratorWork(t *testing.T) {
	requireShell(t)
	binary := writeFakeAICR(t, "normal")
	profile := demo
	profile.GeneratorDigest = digestFileBytes(t, binary)
	output := filepath.Join(t.TempDir(), "published")
	if err := os.Mkdir(output, 0o755); err != nil {
		t.Fatal(err)
	}
	before := digestFileBytes(t, binary)
	_, err := (CLIBundler{Binary: binary}).Bundle(
		context.Background(), profile, Options{OutputDir: output}, io.Discard, io.Discard)
	if code(err) != nkderr.CodeImmutableViolation {
		t.Fatalf("stale destination must fail immutably, got %v", err)
	}
	if after := digestFileBytes(t, binary); after != before {
		t.Fatal("generator ran before stale destination rejection")
	}
}

func TestBundleCancellationCleansStagingAndDoesNotPublish(t *testing.T) {
	requireShell(t)
	binary := writeFakeAICR(t, "block")
	profile := demo
	profile.GeneratorDigest = digestFileBytes(t, binary)
	parent := t.TempDir()
	output := filepath.Join(parent, "published")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := (CLIBundler{Binary: binary}).Bundle(ctx, profile, Options{OutputDir: output}, io.Discard, io.Discard)
	if code(err) != nkderr.CodeTimeout {
		t.Fatalf("canceled generator must return timeout, got %v", err)
	}
	if _, statErr := os.Lstat(output); !os.IsNotExist(statErr) {
		t.Fatalf("canceled bundle was published: %v", statErr)
	}
	stages, globErr := filepath.Glob(filepath.Join(parent, ".published.nkd-stage-*"))
	if globErr != nil || len(stages) != 0 {
		t.Fatalf("canceled bundle left private staging: %v %v", stages, globErr)
	}
}

func requireShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("qualified descriptor execution is Linux-only")
	}
}

func digestFileBytes(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func writeFakeAICR(t *testing.T, mode string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "aicr")
	script := `#!/bin/sh
set -eu
operation="$1"
shift
case "$operation" in
	  recipe)
	    if [ "${AICR_RENAME_RESTORE_ORIGINAL:-}" != "" ]; then
	      mv "$AICR_RENAME_RESTORE_ORIGINAL" "$AICR_RENAME_RESTORE_ORIGINAL.before-attack"
	      printf '#!/bin/sh\nexit 97\n' > "$AICR_RENAME_RESTORE_ORIGINAL"
	      chmod 700 "$AICR_RENAME_RESTORE_ORIGINAL"
	    fi
    if [ "` + mode + `" = "block" ]; then
      while :; do :; done
    fi
    output=""
    while [ "$#" -gt 0 ]; do
      if [ "$1" = "-o" ]; then output="$2"; shift 2; else shift; fi
    done
    printf 'kind: Recipe\n' > "$output"
    ;;
	  evidence)
	    if [ "${AICR_RENAME_RESTORE_ORIGINAL:-}" != "" ] && [ -e "$AICR_RENAME_RESTORE_ORIGINAL.before-attack" ]; then
	      rm -f "$AICR_RENAME_RESTORE_ORIGINAL"
	      mv "$AICR_RENAME_RESTORE_ORIGINAL.before-attack" "$AICR_RENAME_RESTORE_ORIGINAL"
	    fi
	    recipe=""
	    while [ "$#" -gt 0 ]; do
	      if [ "$1" = "--recipe" ]; then recipe="$2"; shift 2; else shift; fi
	    done
	    if [ "${AICR_RECIPE_MUTATION_MARKER:-}" != "" ] && printf 'kind: Replaced\n' > "$recipe" 2>/dev/null; then
	      : > "$AICR_RECIPE_MUTATION_MARKER"
	    fi
    if [ -n "$recipe" ] && grep -q 'Replaced' "$recipe"; then
      printf '` + strings.Repeat("c", 64) + `\n'
    else
      printf '` + strings.Repeat("a", 64) + `\n'
    fi
    ;;
	  bundle)
	    output=""
	    recipe=""
	    while [ "$#" -gt 0 ]; do
	      if [ "$1" = "--output" ]; then output="$2"; shift 2
	      elif [ "$1" = "--recipe" ]; then recipe="$2"; shift 2
	      else shift; fi
	    done
	    if [ "${AICR_RECIPE_MUTATION_MARKER:-}" != "" ] && printf 'kind: Replaced\n' > "$recipe" 2>/dev/null; then
	      : > "$AICR_RECIPE_MUTATION_MARKER"
	    fi
    mkdir -p "$output/payload"
    printf 'enabled: true\n' > "$output/payload/values.yaml"
    if [ "` + mode + `" != "no-checksums" ]; then
      printf 'verified\n' > "$output/checksums.txt"
    fi
    if [ "` + mode + `" = "symlink" ]; then
      ln -s /etc/passwd "$output/escape"
    fi
    if [ "` + mode + `" = "fifo" ]; then
      mkfifo "$output/pipe"
    fi
    if [ "` + mode + `" = "replace-recipe" ]; then
      printf 'kind: Replaced\n' > "$output/recipe.yaml"
    fi
    ;;
  *) exit 98 ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
