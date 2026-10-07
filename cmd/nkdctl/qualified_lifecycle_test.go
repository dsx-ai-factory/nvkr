// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/buildgraph"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/release"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/status"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/trust"
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
			args: []string{"status", "--target", testBuildDigest, "--authority", "status.example.test", "--documents", missing,
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
		"--sign", "--profile", "must-not-be-read.json", "--runner", testBuildDigest,
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
		"--target", testBuildDigest,
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
	target := testBuildDigest
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
	highWaterRaw, err := canonical.Marshal(statusHighWaterFixture{
		SchemaVersion: "nkd.dev/status-high-water/v1", Target: target, Authority: "status.example.test",
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
			Name: "kubelet", RequiredArtifacts: []string{artifactDigest},
			ArtifactKinds: []release.ArtifactKindBinding{{ArtifactDigest: artifactDigest, Kind: release.ArtifactHostPackage}},
		}},
	}
	inventoryRaw, err := canonical.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	inventoryPath := writeCLIFile(t, "inventory.json", inventoryRaw)
	member := release.Member{
		Component: "kubelet", Arch: "amd64", Version: "1",
		BuildDigest:         "sha256:" + strings.Repeat("b", 64),
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
	err := app.Run([]string{"nkdctl", "release", "select", "release.example/stable@" + testBuildDigest})
	if cliErrorCode(err) != nkderr.CodeUnsupported {
		t.Fatalf("unauthenticated selector produced a release decision: %v", err)
	}
}

func TestQualifiedStatusPersistsWithdrawalCheckpointBeforeFailClosedDecision(t *testing.T) {
	target := testBuildDigest
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
	highWaterRaw, err := canonical.Marshal(statusHighWaterFixture{SchemaVersion: "nkd.dev/status-high-water/v1",
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
	var accepted statusHighWaterFixture
	if err := readCanonicalJSONContext(context.Background(), highWaterPath, &accepted); err != nil {
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
		"--target", testBuildDigest, "--authority", "status.example.test", "--documents", path,
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
			Component: "node", Arch: "amd64", BuildDigest: testBuildDigest,
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

func TestCommandSBOMReaderBindsExternalEvidenceOnlyAfterRehash(t *testing.T) {
	evidence := []byte(`{"bomFormat":"CycloneDX","specVersion":"1.5","metadata":{"component":{"name":"artifact","hashes":[{"alg":"SHA-256","content":"` + strings.TrimPrefix(testArtifactDigest, "sha256:") + `"}]}}}`)
	sum := sha256.Sum256(evidence)
	evidenceDigest := "sha256:" + hex.EncodeToString(sum[:])
	evidencePath := writeCLIFile(t, "external-sbom.json", evidence)
	resolved := &buildgraph.Resolved{
		Platform: "linux/amd64",
		Components: []catalog.NamedComponent{{Name: "artifact", Component: catalog.Component{Platforms: []string{"linux/amd64"},
			Tag: "v1.0.0", OutputType: "raw", Prebuilt: &catalog.Prebuilt{
				URL: "https://example.invalid/artifact", Digests: map[string]string{"amd64": testArtifactDigest},
				Evidence: &catalog.ExternalEvidence{SBOMRef: "file://" + evidencePath, SBOMDigest: evidenceDigest},
			},
		}}},
	}
	graph, err := buildgraph.Compile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	buildDigest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	set, err := buildVerifiedSBOM(context.Background(), resolved, buildDigest,
		map[string]string{"artifact": testArtifactDigest})
	if err != nil {
		t.Fatal(err)
	}
	documents, index, err := set.PerArtifact()
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Entries) != 1 || index.Entries[0].ArtifactSHA != testArtifactDigest {
		t.Fatalf("verified SBOM lost artifact binding: %+v", index)
	}
	properties := map[string]string{}
	for _, property := range documents["artifact"].Metadata.Component.Properties {
		properties[property.Name] = property.Value
	}
	if properties["nkd:externalSbomDigest"] != evidenceDigest || properties["nkd:externalSbomRef"] != "file://"+evidencePath {
		t.Fatalf("verified SBOM lost exact external evidence bindings: %+v", properties)
	}
	resolved.Components[0].Prebuilt.Evidence.SBOMDigest = testInputDigest
	if _, err := buildVerifiedSBOM(context.Background(), resolved, buildDigest,
		map[string]string{"artifact": testArtifactDigest}); cliErrorCode(err) == "" {
		t.Fatalf("expected digest mismatch to fail closed, got %v", err)
	}
}

func TestAttestRejectsUnavailableAuthorityOptionsBeforeBuild(t *testing.T) {
	for _, args := range [][]string{
		{"nkdctl", "attest", "--conformance", "conformant"},
		{"nkdctl", "attest", "--validation-vector", testInputDigest},
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

// A test-only description of the persisted protocol. Production state is private
// to status; the CLI neither decodes it nor authorizes writes to it.
type statusHighWaterFixture struct {
	SchemaVersion    string `json:"schemaVersion"`
	Target           string `json:"target"`
	Authority        string `json:"authority"`
	Sequence         uint64 `json:"sequence"`
	CheckpointDigest string `json:"checkpointDigest"`
}

func TestCanonicalInputRejectsCancellationAndFIFOs(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var decoded any
	if err := readCanonicalJSONContext(ctx, missing, &decoded); cliErrorCode(err) != nkderr.CodeTimeout || !nkderr.Retryable(err) {
		t.Fatalf("canceled canonical input: %v", err)
	}
	fifo := filepath.Join(root, "fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := readCanonicalJSONContext(context.Background(), fifo, &decoded); cliErrorCode(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("FIFO canonical input: %v", err)
	}
}
