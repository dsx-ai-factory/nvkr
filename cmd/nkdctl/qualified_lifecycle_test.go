package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	"github.com/nhensley/nkdctl/pkg/nkd/catalog"
	"github.com/nhensley/nkdctl/pkg/nkd/criteria"
	nkderr "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/recipe"
	"github.com/nhensley/nkdctl/pkg/nkd/release"
	"github.com/nhensley/nkdctl/pkg/nkd/status"
	"github.com/nhensley/nkdctl/pkg/nkd/transport"
	"github.com/nhensley/nkdctl/pkg/nkd/trust"
	"github.com/nhensley/nkdctl/pkg/nkd/vuln"
	"github.com/urfave/cli/v2"
	"golang.org/x/sys/unix"
)

const (
	statusTestIdentity = "status@example.test"
	statusTestIssuer   = "https://status-issuer.example.test"
)

func TestStockQualifiedLifecycleCommandsRequireCosignPinBeforeInputIO(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.json")
	tests := []struct {
		name    string
		command *cli.Command
		args    []string
	}{
		{
			name: "release-set", command: releaseSetCmd(),
			args: []string{"verify-set", "--set", missing, "--certificate", "missing.pem", "--release-set-issuer", "https://issuer.example.test"},
		},
		{
			name: "promotion-policy", command: releasePromotionPolicyCmd(),
			args: []string{"policy", "--policy", missing, "--certificate", "missing.pem", "--promotion-issuer", "https://issuer.example.test"},
		},
		{
			name: "status", command: releaseStatusCmd(),
			args: []string{"status", "--target", testRecipeDigest, "--authority", "status.example.test", "--documents", missing,
				"--high-water", missing, "--as-of", "1", "--status-issuer", "https://issuer.example.test"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := &cli.App{Commands: []*cli.Command{test.command}}
			err := app.Run(append([]string{"nkdctl"}, test.args...))
			if cliErrorCode(err) != nkderr.CodeUnpinnedInput {
				t.Fatalf("stock command must reject an ambient verifier before opening inputs: %v", err)
			}
		})
	}
}

func TestQualifiedSigningFailurePublishesNoDocumentOrSidecars(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "status-checkpoint.json")
	bundle := filepath.Join(dir, "status-checkpoint.sigstore")
	command := &cli.Command{
		Name:  "sign-test",
		Flags: qualifiedDocumentSignerFlags(),
		Action: func(c *cli.Context) error {
			return writeQualifiedSignedDocument(c, []byte(`{"schemaVersion":"test/v1"}`), out, bundle)
		},
	}
	err := (&cli.App{Commands: []*cli.Command{command}}).Run([]string{"nkdctl", "sign-test"})
	if cliErrorCode(err) != nkderr.CodeUnpinnedInput {
		t.Fatalf("missing signer pin code = %q, want %q (%v)",
			cliErrorCode(err), nkderr.CodeUnpinnedInput, err)
	}
	for _, path := range []string{out, out + ".sig", out + ".pem", bundle} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("failed signing published %s: %v", path, statErr)
		}
	}
}

func TestAttestSigningAndProfileAuthorityFailBeforeCatalogIO(t *testing.T) {
	for _, args := range [][]string{
		{"nkdctl", "attest", "--sign", "--data", "must-not-be-read"},
		{"nkdctl", "attest", "--profile", "must-not-be-read-profile.json", "--data", "must-not-be-read"},
	} {
		app := &cli.App{Commands: []*cli.Command{cmdAttest()}}
		err := app.Run(args)
		if cliErrorCode(err) != nkderr.CodeUnpinnedInput {
			t.Fatalf("authority admission did not precede catalog/profile I/O: args=%v err=%v", args, err)
		}
	}
}

func TestQualifiedPackagingFailsBeforeCatalogOrArtifactIO(t *testing.T) {
	for _, test := range []struct {
		command *cli.Command
		args    []string
	}{
		{command: cmdAttest(), args: []string{"nkdctl", "attest", "--package", "--data", "must-not-be-read"}},
		{command: cmdVerify(), args: []string{"nkdctl", "verify", "--package", "--expected-digests", "must-not-be-read", "--data", "must-not-be-read"}},
	} {
		err := (&cli.App{Commands: []*cli.Command{test.command}}).Run(test.args)
		if cliErrorCode(err) != nkderr.CodeEngineCapability {
			t.Fatalf("qualified packaging reached catalog or artifact I/O: args=%v err=%v", test.args, err)
		}
	}
}

func TestSignedProfileRepinRequiresNewGenerationBeforeInputIO(t *testing.T) {
	app := &cli.App{Commands: []*cli.Command{cmdProfile()}}
	err := app.Run([]string{"nkdctl", "profile", "repin",
		"--sign", "--profile", "must-not-be-read.json", "--runner", testRecipeDigest,
	})
	if cliErrorCode(err) != nkderr.CodeUsage {
		t.Fatalf("signed in-place profile repin did not fail before profile I/O: %v", err)
	}
}

func TestStatusCheckpointSigningRequiresExecutablePinBeforePublication(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "checkpoint.json")
	app := &cli.App{Commands: []*cli.Command{releaseCheckpointCmd()}}
	err := app.Run([]string{"nkdctl", "checkpoint",
		"--target", testRecipeDigest,
		"--authority", "status.example.test",
		"--issued-epoch", "1",
		"--valid-until-epoch", "2",
		"--out", out,
		"--sign",
	})
	if cliErrorCode(err) != nkderr.CodeUnpinnedInput {
		t.Fatalf("missing status signer pin code = %q, want %q (%v)",
			cliErrorCode(err), nkderr.CodeUnpinnedInput, err)
	}
	for _, path := range []string{out, out + ".sig", out + ".pem"} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("failed checkpoint signing published %s: %v", path, statErr)
		}
	}
}

func TestQualifiedStatusAuthenticatesCheckpointAndPersistsHighWater(t *testing.T) {
	target := testRecipeDigest
	checkpoint := status.StatusCheckpoint{
		SchemaVersion: status.CheckpointSchemaVersion, Target: target,
		Authority: "status.example.test", Sequence: 0, DeclarationDigests: []string{},
		IssuedEpoch: 10, ValidUntilEpoch: 100,
	}
	checkpointRaw, err := checkpoint.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	checkpointPath := writeCLIFile(t, "checkpoint.json", checkpointRaw)
	checkpointDigest, err := checkpoint.Digest()
	if err != nil {
		t.Fatal(err)
	}
	inputsRaw, err := canonical.Marshal(statusVerificationInputs{
		SchemaVersion: statusInputsSchemaVersion,
		Checkpoint: signedDocumentLocation{
			Document: checkpointPath, Signature: "checkpoint.sig", Certificate: "checkpoint.pem",
		},
		Declarations: []signedDocumentLocation{},
	})
	if err != nil {
		t.Fatal(err)
	}
	inputsPath := writeCLIFile(t, "documents.json", inputsRaw)
	highWaterRaw, err := canonical.Marshal(statusHighWaterFile{
		SchemaVersion: statusHighWaterSchemaVersion, Target: target, Authority: "status.example.test",
		Sequence: 0, CheckpointDigest: checkpointDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	highWaterPath := writeCLIFile(t, "high-water.json", highWaterRaw)
	output := highWaterPath
	verifications := 0
	verifier := cliVerifierFunc(func(_ context.Context, request trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
		verifications++
		if request.Role != trust.RoleStatus {
			t.Fatalf("status command authenticated document under role %q", request.Role)
		}
		return cliEvidence(statusTestIdentity, statusTestIssuer), nil
	})
	app := &cli.App{Commands: []*cli.Command{releaseStatusCmdWithVerifier(verifier)}}
	oldStdout := os.Stdout
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writeEnd
	t.Cleanup(func() { os.Stdout = oldStdout })
	err = app.Run([]string{"nkdctl", "status",
		"--target", target, "--authority", "status.example.test",
		"--documents", inputsPath, "--high-water", highWaterPath,
		"--as-of", "50", "--status-identity", statusTestIdentity, "--status-issuer", statusTestIssuer,
	})
	if closeErr := writeEnd.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	os.Stdout = oldStdout
	message, readErr := io.ReadAll(readEnd)
	if closeErr := readEnd.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("qualified status failed: %v", err)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(message), "UNQUALIFIED DIAGNOSTIC STATUS") ||
		!strings.Contains(string(message), "no trusted-time binding or clearance authority") {
		t.Fatalf("diagnostic status output obscured its authority boundary: %q", message)
	}
	if verifications != 1 {
		t.Fatalf("document verifications = %d, want 1", verifications)
	}
	accepted, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(accepted) != string(highWaterRaw) {
		t.Fatalf("persisted high-water differs\n got: %s\nwant: %s", accepted, highWaterRaw)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("high-water mode = %o, want 600", info.Mode().Perm())
	}
}

func TestReleaseStructuralAuthoringLabelsOutputUnauthenticated(t *testing.T) {
	artifactDigest := "sha256:" + strings.Repeat("a", 64)
	inventory := release.Inventory{
		SchemaVersion: release.InventorySchemaVersion,
		Version:       "1",
		Components: []release.InventoryComponent{{
			Name: "kubelet", Role: "worker", RequiredArtifacts: []string{artifactDigest},
			ArtifactKinds: []release.ArtifactKindBinding{{ArtifactDigest: artifactDigest, Kind: release.ArtifactHostPackage}},
		}},
	}
	inventoryRaw, err := canonical.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	inventoryPath := writeCLIFile(t, "inventory.json", inventoryRaw)
	member := release.Member{
		Component: "kubelet", Role: "worker", Arch: "amd64", Version: "1",
		RecipeDigest:        "sha256:" + strings.Repeat("b", 64),
		VectorDigest:        "sha256:" + strings.Repeat("c", 64),
		ResolvedInputDigest: "sha256:" + strings.Repeat("d", 64),
		ArtifactDigests:     []string{artifactDigest},
	}
	memberRaw, err := canonical.Marshal(member)
	if err != nil {
		t.Fatal(err)
	}
	memberPath := writeCLIFile(t, "member.json", memberRaw)
	out := filepath.Join(t.TempDir(), "release-set.json")

	oldStdout := os.Stdout
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writeEnd
	t.Cleanup(func() { os.Stdout = oldStdout })
	app := &cli.App{Commands: []*cli.Command{releaseFromVectorsCmd()}}
	runErr := app.Run([]string{"nkdctl", "from-vectors", "--inventory", inventoryPath,
		"--version", "1", "--out", out, memberPath})
	if closeErr := writeEnd.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	os.Stdout = oldStdout
	message, readErr := io.ReadAll(readEnd)
	if closeErr := readEnd.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if runErr != nil {
		t.Fatal(runErr)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	output := string(message)
	if !strings.Contains(output, "wrote UNQUALIFIED structural ReleaseSet") ||
		!strings.Contains(output, "UNQUALIFIED STRUCTURAL DIAGNOSTIC") ||
		!strings.Contains(output, "not a promotion/completeness receipt") ||
		strings.Contains(output, "gate: PASS") {
		t.Fatalf("release authoring output obscured its authority boundary: %q", output)
	}
	if info, statErr := os.Stat(out); statErr != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		t.Fatalf("structural release set was not written: info=%v err=%v", info, statErr)
	}
}

func TestReleaseDiagnosticHelpStatesAuthorityBoundaries(t *testing.T) {
	statusCommand := releaseStatusCmd()
	if !strings.Contains(strings.ToLower(statusCommand.Usage), "diagnostic") ||
		!strings.Contains(strings.ToLower(statusCommand.Usage), "not release clearance") {
		t.Fatalf("release status help obscures diagnostic authority: %q", statusCommand.Usage)
	}
	var asOfUsage string
	for _, flag := range statusCommand.Flags {
		if flag.Names()[0] == "as-of" {
			asOfUsage = flag.(*cli.Int64Flag).Usage
		}
	}
	if !strings.Contains(asOfUsage, "not trusted-time authority") {
		t.Fatalf("--as-of help obscures caller authority: %q", asOfUsage)
	}
	if usage := releaseFromVectorsCmd().Usage; !strings.Contains(usage, "UNQUALIFIED") ||
		!strings.Contains(usage, "caller-supplied") {
		t.Fatalf("release from-vectors help obscures structural authority: %q", usage)
	}
}

func TestReleaseSelectCannotConfuseResolutionWithAuthorization(t *testing.T) {
	app := &cli.App{Commands: []*cli.Command{cmdRelease()}}
	err := app.Run([]string{"nkdctl", "release", "select", "release.example/stable@" + testRecipeDigest})
	if cliErrorCode(err) != nkderr.CodeUnsupported {
		t.Fatalf("unauthenticated selector produced a release decision: %v", err)
	}
}

func TestQualifiedStatusPersistsWithdrawalCheckpointBeforeFailClosedDecision(t *testing.T) {
	target := testRecipeDigest
	declaration := status.StatusDeclaration{
		SchemaVersion: status.SchemaVersion, Target: target, Action: status.ActionWithdrawn,
		Reason: "compromised", EffectiveEpoch: 20,
	}
	declarationRaw, err := declaration.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	declarationPath := writeCLIFile(t, "withdrawal.json", declarationRaw)
	declarationDigest, err := declaration.Digest()
	if err != nil {
		t.Fatal(err)
	}
	previous := "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	checkpoint := status.StatusCheckpoint{
		SchemaVersion: status.CheckpointSchemaVersion, Target: target, Authority: "status.example.test",
		Sequence: 1, PreviousCheckpoint: previous, DeclarationDigests: []string{declarationDigest},
		IssuedEpoch: 25, ValidUntilEpoch: 100,
	}
	checkpointRaw, err := checkpoint.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	checkpointPath := writeCLIFile(t, "checkpoint.json", checkpointRaw)
	checkpointDigest, err := checkpoint.Digest()
	if err != nil {
		t.Fatal(err)
	}
	inputsRaw, err := canonical.Marshal(statusVerificationInputs{
		SchemaVersion: statusInputsSchemaVersion,
		Checkpoint:    signedDocumentLocation{Document: checkpointPath, Signature: "checkpoint.sig", Certificate: "checkpoint.pem"},
		Declarations:  []signedDocumentLocation{{Document: declarationPath, Signature: "withdrawal.sig", Certificate: "withdrawal.pem"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	inputsPath := writeCLIFile(t, "documents.json", inputsRaw)
	highWaterRaw, err := canonical.Marshal(statusHighWaterFile{SchemaVersion: statusHighWaterSchemaVersion,
		Target: target, Authority: "status.example.test", Sequence: 0, CheckpointDigest: previous})
	if err != nil {
		t.Fatal(err)
	}
	highWaterPath := writeCLIFile(t, "high-water.json", highWaterRaw)
	verifier := cliVerifierFunc(func(context.Context, trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
		return cliEvidence(statusTestIdentity, statusTestIssuer), nil
	})
	app := &cli.App{Commands: []*cli.Command{releaseStatusCmdWithVerifier(verifier)}}
	err = app.Run([]string{"nkdctl", "status", "--target", target, "--authority", "status.example.test",
		"--documents", inputsPath, "--high-water", highWaterPath, "--as-of", "50",
		"--status-identity", statusTestIdentity, "--status-issuer", statusTestIssuer})
	if cliErrorCode(err) != nkderr.CodeWithdrawn {
		t.Fatalf("code = %q, want withdrawn (%v)", cliErrorCode(err), err)
	}
	var accepted statusHighWaterFile
	if err := readCanonicalJSON(highWaterPath, &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.Sequence != 1 || accepted.CheckpointDigest != checkpointDigest {
		t.Fatalf("withdrawal checkpoint was not persisted before failure: %+v", accepted)
	}
}

func TestQualifiedStatusRejectsNonCanonicalTransportConfigBeforeVerification(t *testing.T) {
	path := writeCLIFile(t, "documents.json", []byte("{\n  \"schemaVersion\": \"nkd.dev/status-verification-inputs/v1\"\n}\n"))
	called := false
	verifier := cliVerifierFunc(func(context.Context, trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
		called = true
		return trust.VerificationEvidence{}, nil
	})
	app := &cli.App{Commands: []*cli.Command{releaseStatusCmdWithVerifier(verifier)}}
	err := app.Run([]string{"nkdctl", "status",
		"--target", testRecipeDigest, "--authority", "status.example.test", "--documents", path,
		"--high-water", "unused.json", "--as-of", "50", "--status-identity", statusTestIdentity,
		"--status-issuer", statusTestIssuer,
	})
	if cliErrorCode(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("code = %q, want %q (%v)", cliErrorCode(err), nkderr.CodeInvalidCatalog, err)
	}
	if called {
		t.Fatal("verifier was called for non-canonical command configuration")
	}
}

func TestPromotionPolicyCommandNarrowsRoleQualifiedDocument(t *testing.T) {
	policy := release.PromotionPolicy{
		SchemaVersion:             release.PromotionPolicySchemaVersion,
		CompatibilityPolicyDigest: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		ConformancePolicyDigest:   "sha256:2222222222222222222222222222222222222222222222222222222222222222",
		Rules: []release.PromotionRule{{ArtifactKind: release.ArtifactRaw,
			Evidence: []release.EvidenceRequirement{{Type: release.EvidenceSBOM}}}},
		EffectiveEpoch: 10, ExpiresEpoch: 100,
	}
	raw, err := policy.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	path := writeCLIFile(t, "promotion-policy.json", raw)
	verifier := cliVerifierFunc(func(_ context.Context, request trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
		if request.Role != trust.RolePromotionPolicy {
			t.Fatalf("promotion policy authenticated under role %q", request.Role)
		}
		return cliEvidence("promotion@example.test", "https://promotion-issuer.example.test"), nil
	})
	app := &cli.App{Commands: []*cli.Command{releasePromotionPolicyCmdWithVerifier(verifier)}}
	err = app.Run([]string{"nkdctl", "policy", "--policy", path, "--signature", "policy.sig",
		"--certificate", "policy.pem", "--promotion-identity", "promotion@example.test",
		"--promotion-issuer", "https://promotion-issuer.example.test",
	})
	if err != nil {
		t.Fatalf("qualified promotion policy failed: %v", err)
	}
}

func TestReleaseSetCommandNarrowsRoleQualifiedNormalizedDocument(t *testing.T) {
	set := release.ReleaseSet{
		SchemaVersion: release.ReleaseSetSchemaVersion,
		Version:       "1.0.0",
		InventoryRef:  "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		Members: []release.Member{{
			Component: "node", Arch: "amd64", RecipeDigest: testRecipeDigest,
			VectorDigest:        testInputDigest,
			ResolvedInputDigest: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
			ArtifactDigests:     []string{testInputDigest, testArtifactDigest},
		}},
	}
	raw, err := set.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	path := writeCLIFile(t, "release-set.json", raw)
	verifier := cliVerifierFunc(func(_ context.Context, request trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
		if request.Role != trust.RoleReleaseSet {
			t.Fatalf("release set authenticated under role %q", request.Role)
		}
		return cliEvidence("release@example.test", "https://release-issuer.example.test"), nil
	})
	command := releaseSetCmdWithVerifier(verifier)
	app := &cli.App{Commands: []*cli.Command{command}}
	args := []string{"nkdctl", "verify-set", "--set", path, "--signature", "release.sig",
		"--certificate", "release.pem", "--release-set-identity", "release@example.test",
		"--release-set-issuer", "https://release-issuer.example.test"}
	if err := app.Run(args); err != nil {
		t.Fatalf("qualified release-set authentication failed: %v", err)
	}

	// Canonical JSON is insufficient: a ReleaseSet must also use its normalized
	// member/artifact ordering before those exact bytes are signed.
	nonNormalized, err := canonical.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	nonNormalizedPath := writeCLIFile(t, "release-set-nonnormalized.json", nonNormalized)
	args[3] = nonNormalizedPath
	if err := app.Run(args); cliErrorCode(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("non-normalized release set code = %q, want %q (%v)",
			cliErrorCode(err), nkderr.CodeInvalidCatalog, err)
	}
}

func TestStatusHighWaterCompareAndSwapRejectsStaleWriter(t *testing.T) {
	path := writeCLIFile(t, "high-water.json", []byte(`{"current":2}`))
	err := persistStatusHighWater(path, []byte(`{"current":1}`), []byte(`{"current":3}`))
	if cliErrorCode(err) != nkderr.CodeWriteConflict {
		t.Fatalf("code = %q, want %q (%v)", cliErrorCode(err), nkderr.CodeWriteConflict, err)
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(raw) != `{"current":2}` {
		t.Fatalf("stale writer changed high-water: %s", raw)
	}
}

func TestStatusHighWaterRejectsSymlinkAtBothReadBoundaries(t *testing.T) {
	dir := t.TempDir()
	referent := filepath.Join(dir, "referent.json")
	initial := []byte(`{"current":1}`)
	if err := os.WriteFile(referent, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "high-water.json")
	if err := os.Symlink(referent, path); err != nil {
		t.Fatal(err)
	}
	var decoded statusHighWaterFile
	if _, err := readProtectedHighWater(path, &decoded); cliErrorCode(err) != nkderr.CodeStatusCheckpoint {
		t.Fatalf("initial symlink read code = %q, want %q (%v)",
			cliErrorCode(err), nkderr.CodeStatusCheckpoint, err)
	}
	if err := persistStatusHighWater(path, initial, []byte(`{"current":2}`)); cliErrorCode(err) != nkderr.CodeWriteConflict {
		t.Fatalf("under-lock symlink read code = %q, want %q (%v)",
			cliErrorCode(err), nkderr.CodeWriteConflict, err)
	}
	got, err := os.ReadFile(referent)
	if err != nil || string(got) != string(initial) {
		t.Fatalf("symlink race changed referent: %s (%v)", got, err)
	}
}

func TestStatusStateIORejectsCancellationAndFIFOs(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "must-not-exist")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var decoded statusHighWaterFile
	if err := readCanonicalJSONContext(ctx, missing, &decoded); cliErrorCode(err) != nkderr.CodeTimeout || !nkderr.Retryable(err) {
		t.Fatalf("pre-canceled canonical JSON error = %v, want retryable timeout", err)
	}
	if _, err := readProtectedHighWaterContext(ctx, missing, &decoded); cliErrorCode(err) != nkderr.CodeTimeout || !nkderr.Retryable(err) {
		t.Fatalf("pre-canceled high-water read error = %v, want retryable timeout", err)
	}
	if err := persistStatusHighWaterContext(ctx, missing, nil, []byte(`{"next":1}`)); cliErrorCode(err) != nkderr.CodeTimeout || !nkderr.Retryable(err) {
		t.Fatalf("pre-canceled high-water write error = %v, want retryable timeout", err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("pre-canceled status I/O touched its target: %v", err)
	}

	fifo := filepath.Join(root, "attacker-fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := readCanonicalJSONContext(context.Background(), fifo, &decoded); cliErrorCode(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("FIFO canonical JSON error = %v, want invalid-catalog", err)
	}
	if _, err := readProtectedHighWaterContext(context.Background(), fifo, &decoded); cliErrorCode(err) != nkderr.CodeStatusCheckpoint {
		t.Fatalf("FIFO high-water error = %v, want status-checkpoint", err)
	}
	if _, err := acquireStatusHighWaterLockContext(context.Background(), fifo); cliErrorCode(err) != nkderr.CodeWriteConflict {
		t.Fatalf("FIFO lease error = %v, want write-conflict", err)
	}
}

func TestReleaseAuthoringRejectsCancellationBeforeAuthorityInputIO(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "authority-input.pipe")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name    string
		command *cli.Command
		args    []string
	}{
		{
			name:    "checkpoint declaration",
			command: releaseCheckpointCmd(),
			args: []string{"checkpoint", "--target", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"--authority", "status@example.test", "--declaration", fifo,
				"--issued-epoch", "1", "--valid-until-epoch", "2", "--out", filepath.Join(root, "checkpoint.json")},
		},
		{
			name:    "release set inventory",
			command: releaseFromVectorsCmd(),
			args:    []string{"from-vectors", "--inventory", fifo, "--version", "v1", "member.json", "--out", filepath.Join(root, "release-set.json")},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := &cli.App{Commands: []*cli.Command{test.command}}
			err := app.RunContext(ctx, append([]string{"nkdctl"}, test.args...))
			if cliErrorCode(err) != nkderr.CodeTimeout || !nkderr.Retryable(err) {
				t.Fatalf("error = %v, want retryable %s before path inspection", err, nkderr.CodeTimeout)
			}
		})
	}
}

func TestStatusHighWaterRecoversWhenLeaseOwnerDies(t *testing.T) {
	path := writeCLIFile(t, "high-water.json", []byte(`{"current":1}`))
	leasePath := path + ".lease"
	command := exec.Command(os.Args[0], "-test.run=^TestStatusHighWaterCrashLeaseHelper$")
	command.Env = append(os.Environ(), "NKD_STATUS_CRASH_LEASE_HELPER="+leasePath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("crash lease helper: %v: %s", err, output)
	}

	next := []byte(`{"current":2}`)
	if err := persistStatusHighWater(path, []byte(`{"current":1}`), next); err != nil {
		t.Fatalf("kernel-released crash-stale lock prevented recovery: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(next) {
		t.Fatalf("recovered high-water = %s, want %s", got, next)
	}
	if info, err := os.Lstat(leasePath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("reusable advisory lock inode was removed or replaced: %v (%v)", info, err)
	}
}

func TestStatusHighWaterCrashLeaseHelper(t *testing.T) {
	leasePath := os.Getenv("NKD_STATUS_CRASH_LEASE_HELPER")
	if leasePath == "" {
		return
	}
	lease, err := os.OpenFile(leasePath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(lease.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	// Deliberately bypass defer/Close/LOCK_UN. Process exit is the recovery event
	// and must leave only an unlocked, safely reusable directory entry.
	os.Exit(0)
}

func TestStatusHighWaterRejectsConcurrentLockOwner(t *testing.T) {
	path := writeCLIFile(t, "high-water.json", []byte(`{"current":1}`))
	lease, err := os.OpenFile(path+".lease", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err := unix.Flock(int(lease.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer unix.Flock(int(lease.Fd()), unix.LOCK_UN)

	err = persistStatusHighWater(path, []byte(`{"current":1}`), []byte(`{"current":2}`))
	if cliErrorCode(err) != nkderr.CodeWriteConflict {
		t.Fatalf("active cross-process lock code = %q, want %q (%v)",
			cliErrorCode(err), nkderr.CodeWriteConflict, err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil || string(got) != `{"current":1}` {
		t.Fatalf("contending writer changed high-water: %s (%v)", got, readErr)
	}
}

func TestAuthorityFabricatingLegacyCommandsAreExplicitlyDisabled(t *testing.T) {
	tests := []struct {
		name    string
		command *cli.Command
		args    []string
	}{
		{name: "release-promote", command: cmdRelease(), args: []string{"release", "promote"}},
		{name: "release-rollback", command: cmdRelease(), args: []string{"release", "rollback"}},
		{name: "release-advisory", command: cmdRelease(), args: []string{"release", "advisory"}},
		{name: "publish", command: cmdPublish(), args: []string{"publish", "--verified", "--release", "--complete"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := &cli.App{Commands: []*cli.Command{test.command}}
			err := app.Run(append([]string{"nkdctl"}, test.args...))
			if cliErrorCode(err) != nkderr.CodeUnsupported {
				t.Fatalf("code = %q, want %q (%v)", cliErrorCode(err), nkderr.CodeUnsupported, err)
			}
		})
	}
}

func TestVulnerabilityPathIssuesOnlyQualifiedReport(t *testing.T) {
	artifactDigest := testArtifactDigest
	resolved := &recipe.Resolved{
		RecipeID: "qualified-vuln", Criteria: criteria.Criteria{OS: "ubuntu", Arch: "amd64"},
		Components: []catalog.NamedComponent{{Name: "artifact", Component: catalog.Component{
			Tag: "v1.0.0", OutputType: "raw", Prebuilt: &catalog.Prebuilt{
				URL: "file:///artifact", Digests: map[string]string{"amd64": artifactDigest},
				Evidence: &catalog.ExternalEvidence{None: true},
			},
		}}},
	}
	graph, err := recipe.Compile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	recipeDigest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	scannerDigest := "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	databaseDigest := "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	output := filepath.Join(t.TempDir(), "vuln.json")
	var gotDigest string
	command := &cli.Command{
		Name: "scan",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "vuln-artifact"}, &cli.StringFlag{Name: "vuln-scanner"},
			&cli.StringFlag{Name: "vuln-scanner-digest"}, &cli.StringFlag{Name: "vuln-db"},
			&cli.StringFlag{Name: "vuln-out"},
		},
		Action: func(c *cli.Context) error {
			var scanErr error
			gotDigest, scanErr = runVulnScanWithScanner(c, resolved, recipeDigest,
				map[string]string{"artifact": artifactDigest},
				vulnerabilityScannerFunc(func(ctx context.Context, request vuln.QualifiedScanRequest) (vuln.ScanObservation, error) {
					if ctx != c.Context {
						t.Fatal("scanner did not receive command context")
					}
					return vuln.ScanObservation{ScannerDigest: request.ScannerDigest,
						DatabaseDigest: request.DatabaseDigest, Result: []byte(`{"matches":[]}`)}, nil
				}),
			)
			return scanErr
		},
	}
	app := &cli.App{Commands: []*cli.Command{command}}
	err = app.Run([]string{"nkdctl", "scan", "--vuln-artifact", "artifact", "--vuln-scanner", "grype",
		"--vuln-scanner-digest", scannerDigest, "--vuln-db", databaseDigest, "--vuln-out", output,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotDigest == "" {
		t.Fatal("qualified scan returned no report digest")
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var report vuln.Report
	if err := canonical.UnmarshalExact(raw, &report); err != nil {
		t.Fatalf("report is not exact canonical JSON: %v", err)
	}
	if report.ScannerDigest != scannerDigest || report.DatabaseDigest != databaseDigest || report.Artifact != artifactDigest {
		t.Fatalf("qualified report lost execution binding: %+v", report)
	}
}

func TestCommandSBOMReaderIssuesExternalReceiptOnlyAfterRehash(t *testing.T) {
	evidence := []byte(`{"bomFormat":"CycloneDX","specVersion":"1.5","metadata":{"component":{"name":"artifact","hashes":[{"alg":"SHA-256","content":"` + strings.TrimPrefix(testArtifactDigest, "sha256:") + `"}]}}}`)
	sum := sha256.Sum256(evidence)
	evidenceDigest := "sha256:" + hex.EncodeToString(sum[:])
	evidencePath := writeCLIFile(t, "external-sbom.json", evidence)
	resolved := &recipe.Resolved{
		RecipeID: "external-evidence", Criteria: criteria.Criteria{OS: "ubuntu", Arch: "amd64"},
		Components: []catalog.NamedComponent{{Name: "artifact", Component: catalog.Component{
			Tag: "v1.0.0", OutputType: "raw", Prebuilt: &catalog.Prebuilt{
				URL: "file:///artifact", Digests: map[string]string{"amd64": testArtifactDigest},
				Evidence: &catalog.ExternalEvidence{SBOMRef: "file://" + evidencePath, SBOMDigest: evidenceDigest},
			},
		}}},
	}
	graph, err := recipe.Compile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	recipeDigest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	set, err := buildVerifiedSBOM(context.Background(), resolved, recipeDigest,
		map[string]string{"artifact": testArtifactDigest})
	if err != nil {
		t.Fatal(err)
	}
	receipts, err := set.ExternalReceipts()
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 1 || receipts[0].EvidenceDigest() != evidenceDigest || receipts[0].ArtifactDigest() != testArtifactDigest {
		t.Fatalf("external evidence receipt lost exact bindings: %+v", receipts)
	}
	resolved.Components[0].Prebuilt.Evidence.SBOMDigest = testInputDigest
	if _, err := buildVerifiedSBOM(context.Background(), resolved, recipeDigest,
		map[string]string{"artifact": testArtifactDigest}); cliErrorCode(err) == "" {
		t.Fatalf("expected digest mismatch to fail closed, got %v", err)
	}
}

func TestVulnerabilityPathRejectsMissingQualifiedScanner(t *testing.T) {
	if _, err := runVulnScan(nil, nil, "", nil); cliErrorCode(err) != nkderr.CodeUnsupported {
		t.Fatalf("stock CLI code = %q, want unsupported (%v)", cliErrorCode(err), err)
	}
	_, _, err := selectVulnerabilitySubject("", map[string]string{"a": testArtifactDigest, "b": testInputDigest})
	if cliErrorCode(err) != nkderr.CodeUsage {
		t.Fatalf("code = %q, want usage (%v)", cliErrorCode(err), err)
	}
}

func TestAttestRejectsUnavailableAuthorityOptionsBeforeBuild(t *testing.T) {
	for _, args := range [][]string{
		{"nkdctl", "attest", "--conformance", "conformant"},
		{"nkdctl", "attest", "--validation-vector", testInputDigest},
		{"nkdctl", "attest", "--aicr-bundle"},
		{"nkdctl", "attest", "--vuln"},
		{"nkdctl", "attest", "--release-member", "member.json"},
		{"nkdctl", "attest", "--evidence-out", "observation.json"},
		{"nkdctl", "attest", "--sign", "--offline"},
	} {
		app := &cli.App{Commands: []*cli.Command{cmdAttest()}}
		if err := app.Run(args); cliErrorCode(err) != nkderr.CodeUnsupported {
			t.Fatalf("attest authority option did not fail closed before resolution/build: args=%v err=%v", args, err)
		}
	}
}

func TestPublishAuthorizedRejectsNilCapability(t *testing.T) {
	err := publishAuthorized(context.Background(), nil, transport.Seams{}, transport.Targets{}, nil)
	if cliErrorCode(err) == "" {
		t.Fatalf("expected coded fail-closed error, got %v", err)
	}
}
