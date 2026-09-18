// Package aicr integrates the external AICR recipe generator without linking it
// into nkdctl. A qualified render binds both the generated recipe and the exact
// generator executable bytes, validates the complete output tree, and publishes
// it with an atomic rename.
package aicr

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

// DefaultBinary is the aicr executable name.
const DefaultBinary = "aicr"

const (
	bundleSchemaVersion         = "nkd.dev/aicr-bundle/v2"
	maxGeneratorExecutableBytes = 256 << 20
	maxGeneratedRecipeBytes     = 16 << 20
)

// Profile is the complete AICR identity selected by the catalog. Identity and
// Repo are human/provenance fields. Every output-affecting generator input, the
// canonical recipe digest, and the executable digest are qualified pins.
type Profile struct {
	Identity        string `json:"identity"`
	Digest          string `json:"digest"`          // sha256:<64-hex> canonical AICR recipe digest
	GeneratorDigest string `json:"generatorDigest"` // sha256:<64-hex> exact executable bytes
	Repo            string `json:"repo,omitempty"`
	Service         string `json:"service"`
	Accelerator     string `json:"accelerator"`
	OS              string `json:"os"`
	Intent          string `json:"intent"`
	Platform        string `json:"platform,omitempty"`
}

// Selection is the complete concrete input tuple for an upstream AICR profile.
type Selection struct {
	Service     string
	Accelerator string
	OS          string
	Intent      string
	Platform    string
}

// Validate rejects incomplete selections rather than treating missing AICR axes as
// wildcards.
func (s Selection) Validate() error {
	return ValidateValues(s.Service, s.Accelerator, s.OS, s.Intent, s.Platform)
}

// Options parameterize a bundle render. Qualified renders always use a private
// recipe file inside the staging tree and always verify the pinned generator.
type Options struct {
	OutputDir       string
	Deployer        string
	Attest          bool
	ExtraBundleArgs []string
}

// Result describes one rendered tree. It is deliberately unqualified: its public
// fields can be populated by an external adapter and therefore cannot, by itself,
// authorize attestation. QualifiedResult is the opaque authority-bearing form.
type Result struct {
	Profile         Profile
	RecipeFile      string
	RecipeDigest    string // bare 64-hex retained for compatibility with AICR output
	GeneratorDigest string // sha256:<64-hex>
	BundleDigest    string // sha256:<64-hex>, canonical.KindAICRBundle
	OutputDir       string
	ChecksumsFile   string
}

func (r Result) validateForContext(ctx context.Context, expected Profile) error {
	if err := contextError(ctx, "validating AICR result"); err != nil {
		return err
	}
	if err := expected.Validate(); err != nil {
		return err
	}
	if r.Profile != expected {
		return e.New(e.CodeVerifyFailed, "AICR bundler receipt profile does not match the requested profile")
	}
	if r.RecipeDigest != strings.TrimPrefix(expected.Digest, "sha256:") {
		return e.New(e.CodeVerifyFailed,
			"AICR bundler receipt recipe digest %q does not match %s", r.RecipeDigest, expected.Digest)
	}
	if r.GeneratorDigest != expected.GeneratorDigest {
		return e.New(e.CodeVerifyFailed,
			"AICR bundler receipt generator digest %q does not match %s", r.GeneratorDigest, expected.GeneratorDigest)
	}
	if !canonical.IsDigest(r.BundleDigest) {
		return e.New(e.CodeVerifyFailed, "AICR bundler receipt has invalid bundle digest %q", r.BundleDigest)
	}
	if strings.TrimSpace(r.OutputDir) == "" || strings.Contains(r.OutputDir, "://") {
		return e.New(e.CodeVerifyFailed, "AICR bundler receipt has invalid local output directory %q", r.OutputDir)
	}
	if want := filepath.Join(r.OutputDir, "recipe.yaml"); r.RecipeFile != want {
		return e.New(e.CodeVerifyFailed,
			"AICR bundler receipt recipe path %q does not match published path %q", r.RecipeFile, want)
	}
	if want := filepath.Join(r.OutputDir, "checksums.txt"); r.ChecksumsFile != want {
		return e.New(e.CodeVerifyFailed,
			"AICR bundler receipt checksums path %q does not match published path %q", r.ChecksumsFile, want)
	}
	observed, err := descriptorBundleDigest(ctx, r.OutputDir)
	if err != nil {
		return err
	}
	if observed != r.BundleDigest {
		return e.New(e.CodeVerifyFailed,
			"AICR bundle tree digest %s does not match receipt %s", observed, r.BundleDigest)
	}
	return nil
}

// QualifiedResult is an opaque same-process capability minted only after
// CLIBundler executed one verified generator snapshot through every phase. Its
// fields are private so callers cannot fabricate authority from descriptive fields.
type QualifiedResult struct {
	result        Result
	qualification *resultQualification
}

type resultQualification struct {
	profile         Profile
	recipeDigest    string
	generatorDigest string
	bundleDigest    string
}

// ValidateFor consumes the opaque qualification and rehashes the published tree
// through the descriptor-rooted, bounded traversal immediately before an
// authority-bearing caller uses its returned Result.
func (q *QualifiedResult) ValidateFor(ctx context.Context, expected Profile) (Result, error) {
	if err := contextError(ctx, "validating qualified AICR result"); err != nil {
		return Result{}, err
	}
	if q == nil || q.qualification == nil {
		return Result{}, e.New(e.CodeVerifyFailed, "AICR result has no qualified execution receipt")
	}
	receipt := q.qualification
	if receipt.profile != expected || q.result.Profile != expected ||
		receipt.recipeDigest != q.result.RecipeDigest ||
		receipt.generatorDigest != q.result.GeneratorDigest ||
		receipt.bundleDigest != q.result.BundleDigest {
		return Result{}, e.New(e.CodeVerifyFailed, "AICR qualified execution receipt binding changed")
	}
	if err := q.result.validateForContext(ctx, expected); err != nil {
		return Result{}, err
	}
	return q.result, nil
}

// Validate checks the complete, concrete AICR selection and both content pins.
func (p Profile) Validate() error {
	if strings.TrimSpace(p.Identity) == "" || p.Identity != strings.TrimSpace(p.Identity) {
		return e.New(e.CodeInvalidCatalog,
			"aicr profile identity %q is empty or has surrounding whitespace", p.Identity)
	}
	if !canonical.IsDigest(p.Digest) {
		return e.New(e.CodeUnpinnedSource,
			"aicr profile digest %q is not a lowercase sha256:<64-hex> pin", p.Digest)
	}
	if !canonical.IsDigest(p.GeneratorDigest) {
		return e.New(e.CodeUnpinnedSource,
			"aicr profile generator digest %q is not a lowercase sha256:<64-hex> pin", p.GeneratorDigest)
	}
	if err := ValidateValues(p.Service, p.Accelerator, p.OS, p.Intent, p.Platform); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "aicr profile %q", p.Identity)
	}
	return nil
}

// RecipeArgs builds `aicr recipe ... -o <outFile>` from digest-bound inputs.
func RecipeArgs(p Profile, outFile string) []string {
	args := []string{"recipe"}
	add := func(flag, value string) {
		if value != "" {
			args = append(args, flag, value)
		}
	}
	add("--service", p.Service)
	add("--accelerator", p.Accelerator)
	add("--os", p.OS)
	add("--intent", p.Intent)
	add("--platform", p.Platform)
	return append(args, "-o", outFile)
}

// EvidenceDigestArgs builds the canonical-recipe digest invocation.
func EvidenceDigestArgs(recipeFile string) []string {
	return []string{"evidence", "digest", "--recipe", recipeFile}
}

// BundleArgs builds the deployment-bundle invocation.
func BundleArgs(recipeFile, outDir, deployer string, attest bool, extra []string) []string {
	args := []string{"bundle", "--recipe", recipeFile, "--output", outDir}
	if deployer != "" {
		args = append(args, "--deployer", deployer)
	}
	if attest {
		args = append(args, "--attest")
	}
	return append(args, extra...)
}

// Preview returns the logical invocations for human inspection. The qualified
// execution replaces these display paths with private staging paths and executes
// a verified snapshot rather than resolving bin again.
func Preview(bin string, p Profile, o Options) [][]string {
	rf := recipeFile(o)
	return [][]string{
		prepend(bin, RecipeArgs(p, rf)),
		prepend(bin, EvidenceDigestArgs(rf)),
		prepend(bin, BundleArgs(rf, o.OutputDir, o.Deployer, o.Attest, o.ExtraBundleArgs)),
		prepend(bin, EvidenceDigestArgs(rf)),
	}
}

// CLIBundler is the dependency-free AICR command adapter.
type CLIBundler struct {
	Binary string // empty means DefaultBinary
}

func (c CLIBundler) bin() string {
	if c.Binary != "" {
		return c.Binary
	}
	return DefaultBinary
}

var evidenceDigestOutput = regexp.MustCompile(`^(?:sha256:)?([0-9a-f]{64})$`)

// BundleQualified executes the verified generator capability through all four phases, validates
// the complete output tree, atomically publishes it, and mints an opaque receipt.
// No command writes to the caller's destination before all checks succeed.
func (c CLIBundler) BundleQualified(ctx context.Context, p Profile, o Options, stdout, stderr io.Writer) (*QualifiedResult, error) {
	result, err := c.bundle(ctx, p, o, stdout, stderr)
	if err != nil {
		return nil, err
	}
	return &QualifiedResult{
		result: result,
		qualification: &resultQualification{
			profile: p, recipeDigest: result.RecipeDigest,
			generatorDigest: result.GeneratorDigest, bundleDigest: result.BundleDigest,
		},
	}, nil
}

func (c CLIBundler) bundle(ctx context.Context, p Profile, o Options, stdout, stderr io.Writer) (Result, error) {
	if err := p.Validate(); err != nil {
		return Result{}, err
	}
	if err := validateOptions(o); err != nil {
		return Result{}, err
	}
	if err := contextError(ctx, "before AICR bundling"); err != nil {
		return Result{}, err
	}
	if err := heldfile.RequireExecutableSupport(); err != nil {
		return Result{}, e.Wrap(e.CodeEngineCapability, err,
			"holding AICR executable by descriptor")
	}

	snapshot, err := c.snapshotExecutable(ctx, p.GeneratorDigest)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = snapshot.Close() }()

	// Admit and hold the exact generator capability before creating the caller's
	// output parent or staging tree. A missing/mismatched generator is knowable
	// without mutating output state and therefore must win over path I/O.
	destination, displayDestination, err := resolveDestination(o.OutputDir)
	if err != nil {
		return Result{}, err
	}
	if err := requireAbsent(destination); err != nil {
		return Result{}, err
	}

	stage, err := os.MkdirTemp(filepath.Dir(destination), "."+filepath.Base(destination)+".nkd-stage-")
	if err != nil {
		return Result{}, e.Wrap(e.CodeBuildFailed, err, "creating private AICR staging directory")
	}
	if err := os.Chmod(stage, 0o700); err != nil {
		os.RemoveAll(stage)
		return Result{}, e.Wrap(e.CodeBuildFailed, err, "securing private AICR staging directory")
	}
	// The generator-facing stage is never itself published. Publication is fed
	// only from the immutable held-tree snapshot captured after all generator
	// phases complete.
	defer os.RemoveAll(stage)

	stagedRecipe := filepath.Join(stage, "recipe.yaml")
	if err := runCommand(ctx, snapshot, RecipeArgs(p, stagedRecipe), stdout, stderr, "aicr recipe"); err != nil {
		return Result{}, err
	}
	recipe, err := holdGeneratedRecipe(ctx, stagedRecipe)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = recipe.Close() }()
	// Every authority-bearing consumer below receives the anonymous recipe
	// descriptor. Remove the mutable pathname so no later phase can accidentally
	// reopen different bytes.
	if err := os.Remove(stagedRecipe); err != nil {
		return Result{}, e.Wrap(e.CodeVerifyFailed, err,
			"removing mutable generated AICR recipe pathname")
	}
	heldRecipePath, closeRecipeEndpoint, err := serveHeldRecipe(ctx, recipe)
	if err != nil {
		return Result{}, err
	}
	defer closeRecipeEndpoint()

	var digestOutput bytes.Buffer
	if err := runCommand(ctx, snapshot, EvidenceDigestArgs(heldRecipePath),
		&digestOutput, stderr, "aicr evidence digest"); err != nil {
		return Result{}, err
	}
	gotRecipeDigest, err := parseEvidenceDigest(digestOutput.Bytes())
	if err != nil {
		return Result{}, err
	}
	if want := strings.TrimPrefix(p.Digest, "sha256:"); gotRecipeDigest != want {
		return Result{}, e.New(e.CodeVerifyFailed,
			"AICR recipe digest %s does not match pinned digest %s", gotRecipeDigest, want)
	}

	if err := runCommand(ctx, snapshot,
		BundleArgs(heldRecipePath, stage, o.Deployer, o.Attest, o.ExtraBundleArgs),
		stdout, stderr, "aicr bundle"); err != nil {
		return Result{}, err
	}
	stagedChecksums := filepath.Join(stage, "checksums.txt")
	if err := requireRegularFile(stagedChecksums, "AICR bundle checksums"); err != nil {
		return Result{}, err
	}
	// Re-run semantic verification against the same held bytes after bundle. This
	// catches phase-dependent generator behavior without reopening a mutable recipe.
	digestOutput.Reset()
	if err := runCommand(ctx, snapshot, EvidenceDigestArgs(heldRecipePath),
		&digestOutput, stderr, "final AICR evidence digest"); err != nil {
		return Result{}, err
	}
	finalRecipeDigest, err := parseEvidenceDigest(digestOutput.Bytes())
	if err != nil {
		return Result{}, err
	}
	if finalRecipeDigest != gotRecipeDigest || finalRecipeDigest != strings.TrimPrefix(p.Digest, "sha256:") {
		return Result{}, e.New(e.CodeVerifyFailed,
			"AICR recipe verification changed across bundle generation: final digest %s, verified digest %s, pin %s",
			finalRecipeDigest, gotRecipeDigest, strings.TrimPrefix(p.Digest, "sha256:"))
	}
	if _, err := os.Lstat(stagedRecipe); err == nil {
		// AICR releases that consume a URL intentionally materialize the resolved
		// recipe in their output tree. Admit that copy only when a held snapshot is
		// byte-identical to the original capability, then replace the name below from
		// the original held bytes. A generator-created substitution still fails.
		recreated, openErr := heldfile.OpenContext(ctx, stagedRecipe, heldfile.Options{
			Label: "aicr-recreated-recipe", MaxBytes: maxGeneratedRecipeBytes,
		})
		if openErr != nil {
			return Result{}, e.Wrap(e.CodeVerifyFailed, openErr,
				"holding AICR bundle's recreated recipe")
		}
		matches := recreated.Digest() == recipe.Digest() && recreated.Size() == recipe.Size()
		closeErr := recreated.Close()
		if closeErr != nil {
			return Result{}, e.Wrap(e.CodeVerifyFailed, closeErr,
				"closing AICR bundle's recreated recipe")
		}
		if !matches {
			return Result{}, e.New(e.CodeVerifyFailed,
				"AICR bundle phase recreated recipe.yaml with different bytes")
		}
		if err := os.Remove(stagedRecipe); err != nil {
			return Result{}, e.Wrap(e.CodeVerifyFailed, err,
				"removing verified AICR bundle recipe copy")
		}
	} else if !os.IsNotExist(err) {
		return Result{}, e.Wrap(e.CodeVerifyFailed, err,
			"checking AICR bundle recipe output")
	}
	if err := materializeHeldRecipe(ctx, stage, stagedRecipe, recipe); err != nil {
		return Result{}, err
	}
	if err := requireRegularFile(stagedRecipe, "final generated AICR recipe"); err != nil {
		return Result{}, err
	}
	if err := contextError(ctx, "validating AICR bundle"); err != nil {
		return Result{}, err
	}
	heldTree, err := snapshotBundle(ctx, stage, recipe)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = heldTree.Close() }()

	commitStage, err := os.MkdirTemp(filepath.Dir(destination),
		"."+filepath.Base(destination)+".nkd-commit-")
	if err != nil {
		return Result{}, e.Wrap(e.CodeBuildFailed, err,
			"creating private AICR commit directory")
	}
	if err := os.Chmod(commitStage, 0o700); err != nil {
		_ = os.RemoveAll(commitStage)
		return Result{}, e.Wrap(e.CodeBuildFailed, err,
			"securing private AICR commit directory")
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(commitStage)
		}
	}()
	if err := heldTree.materialize(ctx, commitStage); err != nil {
		return Result{}, err
	}
	materializedDigest, err := descriptorBundleDigest(ctx, commitStage)
	if err != nil {
		return Result{}, err
	}
	if materializedDigest != heldTree.digest {
		return Result{}, e.New(e.CodeImmutableViolation,
			"materialized AICR bundle digest %s does not match held tree digest %s",
			materializedDigest, heldTree.digest)
	}
	confirmedBundleDigest, committed, err := commitBundle(
		ctx, commitStage, destination, displayDestination, heldTree.digest, publishDirectoryNoReplace,
	)
	if err != nil {
		return Result{}, err
	}

	result := Result{
		Profile:         p,
		RecipeFile:      filepath.Join(displayDestination, "recipe.yaml"),
		RecipeDigest:    gotRecipeDigest,
		GeneratorDigest: snapshot.Digest(),
		BundleDigest:    confirmedBundleDigest,
		OutputDir:       displayDestination,
	}
	if checksums := filepath.Join(displayDestination, "checksums.txt"); fileExists(checksums) {
		result.ChecksumsFile = checksums
	}
	return result, nil
}

type directoryPublisher func(stage, destination string) error

// commitBundle persists one validated staging tree, checks cancellation at the
// last reversible boundary, and atomically publishes that exact directory inode.
// Once the no-replace rename succeeds, commit wins: later caller cancellation
// cannot turn a durable immutable publication into a reported failure.
func commitBundle(ctx context.Context, stage, destination, displayDestination, expectedDigest string,
	publish directoryPublisher,
) (digest string, committed bool, err error) {
	if publish == nil {
		return "", false, e.New(e.CodeInternal, "AICR directory publisher is nil")
	}
	if err := contextError(ctx, "persisting AICR bundle before publication"); err != nil {
		return "", false, err
	}
	stageHandle, err := os.Open(stage)
	if err != nil {
		return "", false, e.Wrap(e.CodeImmutableViolation, err,
			"opening validated AICR commit directory")
	}
	defer stageHandle.Close()
	stageInfo, err := stageHandle.Stat()
	if err != nil || !stageInfo.IsDir() {
		if err != nil {
			return "", false, e.Wrap(e.CodeImmutableViolation, err,
				"inspecting validated AICR commit directory")
		}
		return "", false, e.New(e.CodeImmutableViolation,
			"validated AICR commit path is not a directory")
	}
	if err := syncBundleDirectories(ctx, stage); err != nil {
		return "", false, err
	}
	if err := contextError(ctx, "publishing AICR bundle"); err != nil {
		return "", false, err
	}
	if err := publish(stage, destination); err != nil {
		return "", false, e.Wrap(e.CodeImmutableViolation, err,
			"publishing AICR bundle without replacing %s", displayDestination)
	}
	committed = true
	destinationInfo, statErr := os.Lstat(destination)
	if statErr != nil || !destinationInfo.IsDir() || !os.SameFile(stageInfo, destinationInfo) {
		if statErr != nil {
			return "", committed, e.Wrap(e.CodeImmutableViolation, statErr,
				"inspecting committed AICR bundle %s", displayDestination)
		}
		return "", committed, e.New(e.CodeImmutableViolation,
			"published AICR bundle is not the validated commit directory")
	}
	if syncErr := syncDirectory(filepath.Dir(destination)); syncErr != nil {
		removeErr := os.RemoveAll(destination)
		resyncErr := syncDirectory(filepath.Dir(destination))
		if removeErr == nil && resyncErr == nil {
			committed = false
			return "", false, e.Wrap(e.CodeImmutableViolation, syncErr,
				"persisting AICR publication directory")
		}
		return "", true, e.Wrap(e.CodeImmutableViolation,
			errors.Join(syncErr, removeErr, resyncErr),
			"persisting or rolling back AICR publication directory")
	}
	return expectedDigest, true, nil
}

func syncBundleDirectories(ctx context.Context, root string) error {
	var directories []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := contextError(ctx, "persisting AICR commit tree"); err != nil {
			return err
		}
		if entry.IsDir() {
			directories = append(directories, path)
		}
		return nil
	})
	if err != nil {
		var coded *e.Error
		if errors.As(err, &coded) {
			return err
		}
		return e.Wrap(e.CodeBuildFailed, err, "walking AICR commit directories")
	}
	sort.Slice(directories, func(i, j int) bool {
		return len(directories[i]) > len(directories[j])
	})
	for _, directory := range directories {
		if err := contextError(ctx, "persisting AICR commit tree"); err != nil {
			return err
		}
		if err := syncDirectory(directory); err != nil {
			return e.Wrap(e.CodeBuildFailed, err,
				"syncing AICR commit directory %q", directory)
		}
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}

// snapshotExecutable opens the resolved executable once, copies those exact bytes
// into an anonymous held capability, and verifies their external pin. Every phase
// inherits that descriptor; no named snapshot exists to rename or replace.
func (c CLIBundler) snapshotExecutable(ctx context.Context, want string) (*heldfile.File, error) {
	if err := contextError(ctx, "before holding AICR executable"); err != nil {
		return nil, err
	}
	if err := heldfile.RequireExecutableSupport(); err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err,
			"holding AICR executable by descriptor")
	}
	resolved, err := exec.LookPath(c.bin())
	if err != nil {
		return nil, e.Wrap(e.CodeUnsupported, err,
			"resolving AICR executable %q", c.bin())
	}
	if err := contextError(ctx, "resolving AICR executable"); err != nil {
		return nil, err
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err,
			"resolving AICR executable path %q", resolved)
	}
	snapshot, err := heldfile.OpenContext(ctx, resolved, heldfile.Options{
		Label: "aicr-generator", Executable: true,
		MaxBytes: maxGeneratorExecutableBytes, ExpectedDigest: want,
	})
	if err != nil {
		if ctxErr := contextError(ctx, "capturing pinned AICR executable"); ctxErr != nil {
			return nil, ctxErr
		}
		if errors.Is(err, heldfile.ErrUnsupported) {
			return nil, e.Wrap(e.CodeEngineCapability, err,
				"holding AICR executable %q by descriptor", resolved)
		}
		return nil, e.Wrap(e.CodeVerifyFailed, err,
			"capturing pinned AICR executable %q", resolved)
	}
	return snapshot, nil
}

func holdGeneratedRecipe(ctx context.Context, path string) (*heldfile.File, error) {
	if err := contextError(ctx, "before holding generated AICR recipe"); err != nil {
		return nil, err
	}
	recipe, err := heldfile.OpenContext(ctx, path, heldfile.Options{
		Label: "aicr-recipe", MaxBytes: maxGeneratedRecipeBytes,
	})
	if err != nil {
		if ctxErr := contextError(ctx, "capturing generated AICR recipe"); ctxErr != nil {
			return nil, ctxErr
		}
		if errors.Is(err, heldfile.ErrUnsupported) {
			return nil, e.Wrap(e.CodeEngineCapability, err,
				"holding generated AICR recipe by descriptor")
		}
		return nil, e.Wrap(e.CodeVerifyFailed, err,
			"capturing generated AICR recipe")
	}
	return recipe, nil
}

func materializeHeldRecipe(ctx context.Context, stage, destination string, recipe *heldfile.File) error {
	if err := contextError(ctx, "before materializing held AICR recipe"); err != nil {
		return err
	}
	if recipe == nil {
		return e.New(e.CodeInternal, "held AICR recipe is missing")
	}
	temporary, err := os.CreateTemp(stage, ".nkd-held-recipe-*")
	if err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "creating final held AICR recipe")
	}
	temporaryPath := temporary.Name()
	cleanup := true
	defer func() {
		_ = temporary.Close()
		if cleanup {
			_ = os.Remove(temporaryPath)
		}
	}()
	written, copyErr := recipe.WriteToContext(ctx, temporary)
	if copyErr != nil || written != recipe.Size() {
		if ctxErr := contextError(ctx, "materializing held AICR recipe"); ctxErr != nil {
			return ctxErr
		}
		if copyErr == nil {
			copyErr = io.ErrShortWrite
		}
		return e.Wrap(e.CodeVerifyFailed, copyErr,
			"materializing held AICR recipe")
	}
	if err := contextError(ctx, "materializing held AICR recipe"); err != nil {
		return err
	}
	syncErr := temporary.Sync()
	chmodErr := temporary.Chmod(0o644)
	closeErr := temporary.Close()
	for _, failure := range []struct {
		operation string
		err       error
	}{
		{operation: "syncing", err: syncErr},
		{operation: "setting mode on", err: chmodErr},
		{operation: "closing", err: closeErr},
	} {
		if failure.err != nil {
			return e.Wrap(e.CodeBuildFailed, failure.err,
				"%s final held AICR recipe", failure.operation)
		}
	}
	if err := contextError(ctx, "publishing held AICR recipe"); err != nil {
		return err
	}
	if err := publishDirectoryNoReplace(temporaryPath, destination); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err,
			"publishing held AICR recipe without replacement")
	}
	cleanup = false
	return nil
}

func parseEvidenceDigest(output []byte) (string, error) {
	match := evidenceDigestOutput.FindStringSubmatch(strings.TrimSpace(string(output)))
	if len(match) != 2 {
		return "", e.New(e.CodeBuildFailed,
			"aicr evidence digest returned malformed or ambiguous output")
	}
	return match[1], nil
}

func validateOptions(o Options) error {
	if strings.TrimSpace(o.OutputDir) == "" {
		return e.New(e.CodeInvalidCatalog, "aicr bundle: output directory is required")
	}
	if strings.Contains(o.OutputDir, "://") {
		return e.New(e.CodeUnsupported,
			"aicr bundle: qualified publication requires a local directory destination")
	}
	switch o.Deployer {
	case "", "helm", "argocd", "flux", "helmfile":
	default:
		return e.New(e.CodeInvalidCatalog, "aicr bundle: unsupported deployer %q", o.Deployer)
	}
	return validateBundleArgs(o.ExtraBundleArgs)
}

// validateBundleArgs prevents a pass-through argument from creating a second
// meaning for any qualified option. Other extension flags remain available.
func validateBundleArgs(args []string) error {
	for _, argument := range args {
		if argument == "" {
			return e.New(e.CodeInvalidCatalog, "aicr bundle: empty extra argument")
		}
		if argument == "--" {
			return e.New(e.CodeInvalidCatalog,
				"aicr bundle: end-of-options may not bypass qualified arguments")
		}
		name := argument
		if index := strings.IndexByte(name, '='); index >= 0 {
			name = name[:index]
		}
		longProtected := name == "--recipe" || name == "--output" ||
			name == "--deployer" || name == "--attest"
		shortProtected := name == "-r" || name == "-o" || name == "-d" ||
			strings.HasPrefix(name, "-r") || strings.HasPrefix(name, "-o") ||
			strings.HasPrefix(name, "-d")
		if longProtected || shortProtected {
			return e.New(e.CodeInvalidCatalog,
				"aicr bundle: extra argument %q may not override a qualified option", argument)
		}
	}
	return nil
}

func resolveDestination(output string) (absolute, display string, err error) {
	display = filepath.Clean(output)
	absolute, err = filepath.Abs(display)
	if err != nil {
		return "", "", e.Wrap(e.CodeInvalidCatalog, err,
			"resolving AICR bundle destination %q", output)
	}
	parent := filepath.Dir(absolute)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", "", e.Wrap(e.CodeBuildFailed, err,
			"creating AICR bundle parent directory %q", parent)
	}
	return absolute, display, nil
}

func requireAbsent(path string) error {
	_, err := os.Lstat(path)
	if err == nil {
		return e.New(e.CodeImmutableViolation,
			"AICR bundle destination %q already exists; refusing to replace it", path)
	}
	if !os.IsNotExist(err) {
		return e.Wrap(e.CodeImmutableViolation, err,
			"checking AICR bundle destination %q", path)
	}
	return nil
}

func requireRegularFile(path, description string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "%s is missing", description)
	}
	if !info.Mode().IsRegular() {
		return e.New(e.CodeVerifyFailed, "%s %q is not a regular file", description, path)
	}
	return nil
}

func runCommand(ctx context.Context, executable *heldfile.File, args []string, stdout, stderr io.Writer, step string,
	inputs ...heldfile.Descriptor,
) error {
	if err := contextError(ctx, step); err != nil {
		return err
	}
	err := heldfile.RunContext(ctx, executable, args, heldfile.RunOptions{
		Stdout: stdout,
		Stderr: stderr,
		Env:    aicrCommandEnvironment(),
	}, inputs...)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return e.Wrap(e.CodeTimeout, contextErr, "%s canceled", step)
		}
		if errors.Is(err, heldfile.ErrUnsupported) {
			return e.Wrap(e.CodeEngineCapability, err, "%s held execution", step)
		}
		return e.Wrap(e.CodeBuildFailed, err, "%s", step)
	}
	return contextError(ctx, step)
}

// serveHeldRecipe gives extension-sensitive AICR releases a YAML locator without
// publishing a mutable recipe pathname. The immutable held bytes are copied once
// into memory and served only on loopback at an exact .yaml path; utility containers
// run with external networking disabled, and proxy variables are scrubbed below.
func serveHeldRecipe(ctx context.Context, recipe *heldfile.File) (string, func(), error) {
	if recipe == nil {
		return "", nil, e.New(e.CodeInternal, "serving AICR recipe requires a held capability")
	}
	raw, err := recipe.ReadAllContext(ctx, maxGeneratedRecipeBytes)
	if err != nil {
		if ctx.Err() != nil {
			return "", nil, e.Wrap(e.CodeTimeout, ctx.Err(), "reading held AICR recipe")
		}
		return "", nil, e.Wrap(e.CodeVerifyFailed, err, "reading held AICR recipe")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", nil, e.Wrap(e.CodeEngineCapability, err, "opening private AICR recipe endpoint")
	}
	path := "/recipe-" + strings.TrimPrefix(recipe.Digest(), "sha256:") + ".yaml"
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writer.Header().Set("Content-Type", "application/yaml")
		writer.Header().Set("Content-Length", strconv.Itoa(len(raw)))
		writer.Header().Set("Cache-Control", "no-store")
		_, _ = writer.Write(raw)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second, IdleTimeout: 2 * time.Second}
	go func() { _ = server.Serve(listener) }()
	closeEndpoint := func() {
		_ = server.Close()
		_ = listener.Close()
	}
	return "http://" + listener.Addr().String() + path, closeEndpoint, nil
}

func aicrCommandEnvironment() []string {
	result := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		name := entry
		if index := strings.IndexByte(entry, '='); index >= 0 {
			name = entry[:index]
		}
		switch strings.ToUpper(name) {
		case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY":
			continue
		default:
			result = append(result, entry)
		}
	}
	return append(result, "NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost")
}

func contextError(ctx context.Context, operation string) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "%s requires a non-nil context", operation)
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "%s canceled", operation)
	}
	return nil
}

func recipeFile(o Options) string {
	dir := o.OutputDir
	if dir == "" {
		dir = "."
	}
	return filepath.Join(dir, "recipe.yaml")
}

func prepend(binary string, args []string) []string {
	return append([]string{binary}, args...)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
