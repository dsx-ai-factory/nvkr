// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

const (
	ociTestDigest    = "sha256:5555555555555555555555555555555555555555555555555555555555555555"
	ociTestRepo      = "127.0.0.1:5000/nke/nkectl"
	ociTestRef       = ociTestRepo + "@" + ociTestDigest
	ociTestIdentity  = "^https://github\\.com/NVIDIA/nke/\\.github/workflows/nkd-factory\\.yml@refs/heads/main$"
	ociTestIssuer    = "https://token.actions.githubusercontent.com"
	ociTestPredicate = `{"buildDefinition":{"buildType":"https://nkd.dev/buildtypes/wrapped-build/v1"}}`
)

func ociSubject() Subject { return Subject{Ref: ociTestRef, Digest: ociTestDigest} }

func requireSignCode(t *testing.T, err error, want nkderr.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %s, got nil", want)
	}
	var coded *nkderr.Error
	if !errors.As(err, &coded) {
		t.Fatalf("error %v is not coded", err)
	}
	if coded.Code != want {
		t.Fatalf("error code = %s, want %s (%v)", coded.Code, want, err)
	}
}

// ---------------------------------------------------------------------------
// Subject
// ---------------------------------------------------------------------------

func TestSubjectRefusesUnpinnedOrDivergentReferences(t *testing.T) {
	requireSignCode(t, Subject{Ref: "", Digest: ociTestDigest}.Validate(), nkderr.CodeUsage)
	requireSignCode(t, Subject{Ref: ociTestRepo + ":v1", Digest: ociTestDigest}.Validate(), nkderr.CodeUnpinnedSource)
	requireSignCode(t, Subject{Ref: ociTestRef, Digest: "deadbeef"}.Validate(), nkderr.CodeUnverifiedRef)
	requireSignCode(t, Subject{
		Ref:    ociTestRepo + "@sha256:" + strings.Repeat("6", 64),
		Digest: ociTestDigest,
	}.Validate(), nkderr.CodeVerifyFailed)
	if err := ociSubject().Validate(); err != nil {
		t.Fatalf("valid subject rejected: %v", err)
	}
	if ociSubject().Repository() != ociTestRepo {
		t.Fatalf("repository = %q", ociSubject().Repository())
	}
}

// ---------------------------------------------------------------------------
// DryRunSigner
// ---------------------------------------------------------------------------

func TestNewDryRunSignerRequiresIdentityAndIssuer(t *testing.T) {
	_, err := NewDryRunSigner("", ociTestIssuer)
	requireSignCode(t, err, nkderr.CodeTrustPolicyMissing)
	_, err = NewDryRunSigner(ociTestIdentity, "   ")
	requireSignCode(t, err, nkderr.CodeTrustPolicyMissing)
}

// The dry-run record is the artifact an operator reads to see exactly what a
// real signing would have executed, so its bytes are pinned.
const goldenDryRunSigningRecord = `{"digest":"sha256:5555555555555555555555555555555555555555555555555555555555555555","dryRun":true,"identity":"^https://github\\.com/NVIDIA/nke/\\.github/workflows/nkd-factory\\.yml@refs/heads/main$","issuer":"https://token.actions.githubusercontent.com","predicateSha256":"sha256:bcdd6c21cad365013c923099b02a83f925bc5eb3d702766d63e2e82a4b898b5a","predicateType":"https://slsa.dev/provenance/v1","ref":"127.0.0.1:5000/nke/nkectl@sha256:5555555555555555555555555555555555555555555555555555555555555555","schemaVersion":"nkd.dev/oci-signing-record/v1","wouldRun":[["cosign","sign","--yes","--new-bundle-format","127.0.0.1:5000/nke/nkectl@sha256:5555555555555555555555555555555555555555555555555555555555555555"],["cosign","attest","--yes","--new-bundle-format","--type","slsaprovenance1","--predicate","${NKD_PREDICATE_FILE}","127.0.0.1:5000/nke/nkectl@sha256:5555555555555555555555555555555555555555555555555555555555555555"]]}`

func TestDryRunSignerRecordGolden(t *testing.T) {
	signer, err := NewDryRunSigner(ociTestIdentity, ociTestIssuer)
	if err != nil {
		t.Fatalf("NewDryRunSigner: %v", err)
	}
	record, err := signer.SignAndAttest(context.Background(), ociSubject(), []byte(ociTestPredicate))
	if err != nil {
		t.Fatalf("SignAndAttest: %v", err)
	}
	raw, err := record.Canonical()
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	if string(raw) != goldenDryRunSigningRecord {
		t.Fatalf("dry-run signing record diverged:\n got: %s\nwant: %s", raw, goldenDryRunSigningRecord)
	}
	if len(record.Ran) != 0 || !record.DryRun {
		t.Fatal("a dry-run record must never claim an executed command")
	}
}

func TestDryRunSignerVerifyRecord(t *testing.T) {
	for _, required := range []bool{false, true} {
		t.Run(fmt.Sprint(required), func(t *testing.T) {
			signer, err := NewDryRunSigner(ociTestIdentity, ociTestIssuer)
			if err != nil {
				t.Fatal(err)
			}
			record, err := signer.Verify(t.Context(), ociSubject(), VerifyPolicy{RequireAttestation: required})
			if err != nil {
				t.Fatal(err)
			}
			want := 2
			if required {
				want = 4
			}
			if len(record.WouldRun) != want || len(record.Ran) != 0 || record.SignatureVerified || record.AttestationVerified {
				t.Fatalf("record: %+v", record)
			}
			if record.WouldRun[0][1] != "download" || record.WouldRun[1][1] != "verify-blob-attestation" {
				t.Fatalf("commands: %v", record.WouldRun)
			}
			if err := record.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDryRunSignerRefusesDigestMismatch(t *testing.T) {
	signer, err := NewDryRunSigner(ociTestIdentity, ociTestIssuer)
	if err != nil {
		t.Fatalf("NewDryRunSigner: %v", err)
	}
	mismatched := Subject{Ref: ociTestRepo + "@sha256:" + strings.Repeat("7", 64), Digest: ociTestDigest}
	_, err = signer.SignAndAttest(context.Background(), mismatched, []byte(ociTestPredicate))
	requireSignCode(t, err, nkderr.CodeVerifyFailed)
	_, err = signer.Verify(context.Background(), mismatched, VerifyPolicy{})
	requireSignCode(t, err, nkderr.CodeVerifyFailed)
}

func TestDryRunSignerRejectsUnusablePredicate(t *testing.T) {
	signer, err := NewDryRunSigner(ociTestIdentity, ociTestIssuer)
	if err != nil {
		t.Fatalf("NewDryRunSigner: %v", err)
	}
	_, err = signer.SignAndAttest(context.Background(), ociSubject(), nil)
	requireSignCode(t, err, nkderr.CodeProvenanceMissing)
	_, err = signer.SignAndAttest(context.Background(), ociSubject(), []byte("{not json"))
	requireSignCode(t, err, nkderr.CodeProvenanceMissing)
}

func TestDryRunSignerHonorsContextCancellation(t *testing.T) {
	signer, err := NewDryRunSigner(ociTestIdentity, ociTestIssuer)
	if err != nil {
		t.Fatalf("NewDryRunSigner: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = signer.SignAndAttest(ctx, ociSubject(), []byte(ociTestPredicate))
	requireSignCode(t, err, nkderr.CodeTimeout)
}

// ---------------------------------------------------------------------------
// CosignOCISigner: argv, output parsing, credentials
// ---------------------------------------------------------------------------

type recordedCall struct {
	binary string
	args   []string
	env    []string
}

// cosignStub records every invocation and replies with canned stdout.
type cosignStub struct {
	calls    []recordedCall
	stdout   map[string]string
	failWith map[string]error
	onCall   func(recordedCall) error
}

func (c *cosignStub) RunEnv(_ context.Context, binary string, args, env []string, stdout, _ io.Writer) error {
	call := recordedCall{binary: binary, args: append([]string(nil), args...), env: append([]string(nil), env...)}
	c.calls = append(c.calls, call)
	if c.onCall != nil {
		if err := c.onCall(call); err != nil {
			return err
		}
	}
	if err := c.failWith[args[0]]; err != nil {
		return err
	}
	if out, ok := c.stdout[args[0]]; ok {
		if _, err := io.WriteString(stdout, out); err != nil {
			return err
		}
	}
	return nil
}

func (c *cosignStub) Run(ctx context.Context, binary string, args []string, stdout, stderr io.Writer) error {
	return c.RunEnv(ctx, binary, args, nil, stdout, stderr)
}

func (c *cosignStub) call(t *testing.T, subcommand string) recordedCall {
	t.Helper()
	for _, call := range c.calls {
		if len(call.args) > 0 && call.args[0] == subcommand {
			return call
		}
	}
	t.Fatalf("cosign %s was never invoked (calls: %v)", subcommand, c.calls)
	return recordedCall{}
}

const cosignVersionJSON = `{"gitVersion":"v3.1.3","goVersion":"go1.23.0"}`

func cosignAttestationJSON(digest, predicateType string) string {
	statement, err := json.Marshal(map[string]any{
		"_type":         "https://in-toto.io/Statement/v1",
		"predicateType": predicateType,
		"subject": []map[string]any{
			{"name": "nkectl", "digest": map[string]string{"sha256": strings.TrimPrefix(digest, "sha256:")}},
		},
		"predicate": map[string]any{"buildDefinition": map[string]any{}},
	})
	if err != nil {
		panic(err)
	}
	envelope, err := json.Marshal(map[string]any{
		"payloadType": "application/vnd.in-toto+json",
		"payload":     base64.StdEncoding.EncodeToString(statement),
	})
	if err != nil {
		panic(err)
	}
	return string(envelope)
}

func newStubbedSigner(t *testing.T, stub *cosignStub, credential Credential) *CosignOCISigner {
	t.Helper()
	signer, err := NewCosignOCISigner(CosignOCIOptions{
		Identity:   ociTestIdentity,
		Issuer:     ociTestIssuer,
		Credential: credential,
		Runner:     stub,
	})
	if err != nil {
		t.Fatalf("NewCosignOCISigner: %v", err)
	}
	return signer
}

func TestCosignOCISignAndAttestArgvContract(t *testing.T) {
	stub := &cosignStub{stdout: map[string]string{"version": cosignVersionJSON}}
	var observedPredicate []byte
	var observedMode os.FileMode
	stub.onCall = func(call recordedCall) error {
		if call.args[0] != "attest" {
			return nil
		}
		path := call.args[len(call.args)-2]
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		observedMode = info.Mode().Perm()
		observedPredicate, err = os.ReadFile(path)
		return err
	}
	signer := newStubbedSigner(t, stub, Credential{})
	record, err := signer.SignAndAttest(context.Background(), ociSubject(), []byte(ociTestPredicate))
	if err != nil {
		t.Fatalf("SignAndAttest: %v", err)
	}

	sign := stub.call(t, "sign")
	if strings.Join(sign.args, " ") != "sign --yes --new-bundle-format "+ociTestRef {
		t.Fatalf("sign argv = %v", sign.args)
	}
	attest := stub.call(t, "attest")
	if attest.args[0] != "attest" || attest.args[1] != "--yes" || attest.args[2] != "--new-bundle-format" ||
		attest.args[3] != "--type" || attest.args[4] != "slsaprovenance1" || attest.args[5] != "--predicate" ||
		attest.args[len(attest.args)-1] != ociTestRef || len(attest.args) != 8 {
		t.Fatalf("attest argv = %v", attest.args)
	}
	if string(observedPredicate) != ociTestPredicate {
		t.Fatalf("cosign saw predicate %q", observedPredicate)
	}
	if observedMode != 0o600 {
		t.Fatalf("predicate file mode = %v, want 0600", observedMode)
	}
	if _, err := os.Stat(attest.args[6]); !os.IsNotExist(err) {
		t.Fatalf("the private predicate file outlived the operation: %v", err)
	}

	if record.DryRun || len(record.WouldRun) != 0 || len(record.Ran) != 2 {
		t.Fatalf("record = %+v", record)
	}
	if record.Ran[0][0] != DefaultBinary || record.Ran[1][0] != DefaultBinary {
		t.Fatalf("recorded argv must start with the executable: %v", record.Ran)
	}
	if record.CosignVersion != "v3.1.3" {
		t.Fatalf("cosign version = %q", record.CosignVersion)
	}
	if record.PredicateSHA256 != digestBytes([]byte(ociTestPredicate)) {
		t.Fatalf("predicate digest = %q", record.PredicateSHA256)
	}
	if _, err := record.Canonical(); err != nil {
		t.Fatalf("Canonical: %v", err)
	}
}

func TestCosignOCISignAndAttestMapsFailureToCodedError(t *testing.T) {
	stub := &cosignStub{failWith: map[string]error{"sign": fmt.Errorf("exit status 1")}}
	signer := newStubbedSigner(t, stub, Credential{})
	_, err := signer.SignAndAttest(context.Background(), ociSubject(), []byte(ociTestPredicate))
	requireSignCode(t, err, nkderr.CodeSignatureInvalid)
}

// Credentials must reach cosign only through a private Docker config file.
func TestCosignOCICredentialsNeverReachArgv(t *testing.T) {
	stub := &cosignStub{stdout: map[string]string{
		"version":  cosignVersionJSON,
		"download": testOCIBundle(t, ociTestDigest, CosignSignaturePredicateType, ociObservedIdentity, ociTestIssuer),
	}}
	credential := Credential{Registry: "ghcr.io", Username: "publisher-bot", Password: "s3cr3t-token"}
	var configPath string
	var configMode, dirMode os.FileMode
	var document []byte
	stub.onCall = func(call recordedCall) error {
		dir := ""
		for _, entry := range call.env {
			if strings.HasPrefix(entry, "DOCKER_CONFIG=") {
				dir = strings.TrimPrefix(entry, "DOCKER_CONFIG=")
			}
		}
		if dir == "" {
			return fmt.Errorf("DOCKER_CONFIG missing from env")
		}
		info, err := os.Stat(dir)
		if err != nil {
			return err
		}
		dirMode = info.Mode().Perm()
		configPath = filepath.Join(dir, "config.json")
		configInfo, err := os.Stat(configPath)
		if err != nil {
			return err
		}
		configMode = configInfo.Mode().Perm()
		document, err = os.ReadFile(configPath)
		return err
	}
	signer := newStubbedSigner(t, stub, credential)
	if _, err := signer.Verify(context.Background(), ociSubject(), VerifyPolicy{}); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if dirMode != 0o700 {
		t.Fatalf("DOCKER_CONFIG directory mode = %v, want 0700", dirMode)
	}
	if configMode != 0o600 {
		t.Fatalf("config.json mode = %v, want 0600", configMode)
	}
	var config struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(document, &config); err != nil {
		t.Fatalf("config.json is not JSON: %v (%s)", err, document)
	}
	decoded, err := base64.StdEncoding.DecodeString(config.Auths["ghcr.io"].Auth)
	if err != nil {
		t.Fatalf("auth is not base64: %v", err)
	}
	if string(decoded) != "publisher-bot:s3cr3t-token" {
		t.Fatalf("auth = %q", decoded)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("the private credential file outlived the operation: %v", err)
	}
	for _, call := range stub.calls {
		for _, arg := range call.args {
			if strings.Contains(arg, credential.Password) || strings.Contains(arg, credential.Username) {
				t.Fatalf("credential leaked into argv: %v", call.args)
			}
		}
	}
}

func TestCosignOCINoCredentialLeavesDockerConfigUnset(t *testing.T) {
	stub := &cosignStub{stdout: map[string]string{
		"version":  cosignVersionJSON,
		"download": testOCIBundle(t, ociTestDigest, CosignSignaturePredicateType, ociObservedIdentity, ociTestIssuer),
	}}
	signer := newStubbedSigner(t, stub, Credential{})
	if _, err := signer.Verify(context.Background(), ociSubject(), VerifyPolicy{}); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	for _, call := range stub.calls {
		for _, entry := range call.env {
			if strings.HasPrefix(entry, "DOCKER_CONFIG=") {
				t.Fatalf("unexpected DOCKER_CONFIG: %q", entry)
			}
		}
	}
}

func TestOCICosignEnvironmentScrub(t *testing.T) {
	got := ociCosignEnvironment([]string{
		"PATH=/usr/bin",
		"HOME=/home/runner",
		"ACTIONS_ID_TOKEN_REQUEST_URL=https://token/request",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN=abc",
		"COSIGN_EXPERIMENTAL=1",
		"SIGSTORE_REKOR_URL=http://evil",
		"TUF_ROOT=/tmp/tuf",
		"SOURCE_DATE_EPOCH=0",
		"LD_PRELOAD=/tmp/x.so",
		"DOCKER_CONFIG=/home/runner/.docker",
		"REGISTRY_AUTH_FILE=/tmp/auth.json",
		"DOCKER_AUTH_CONFIG={}",
	}, "/private/nkd/docker")
	joined := strings.Join(got, "\n")
	for _, want := range []string{
		"PATH=/usr/bin", "HOME=/home/runner",
		"ACTIONS_ID_TOKEN_REQUEST_URL=https://token/request",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN=abc",
		"DOCKER_CONFIG=/private/nkd/docker",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("environment must preserve %q: %v", want, got)
		}
	}
	for _, forbidden := range []string{
		"COSIGN_EXPERIMENTAL", "SIGSTORE_REKOR_URL", "TUF_ROOT", "SOURCE_DATE_EPOCH",
		"LD_PRELOAD", "/home/runner/.docker", "REGISTRY_AUTH_FILE", "DOCKER_AUTH_CONFIG",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("environment must scrub %q: %v", forbidden, got)
		}
	}
}

func TestNewCosignOCISignerRejectsIncompleteConfiguration(t *testing.T) {
	_, err := NewCosignOCISigner(CosignOCIOptions{Issuer: ociTestIssuer, Runner: &cosignStub{}})
	requireSignCode(t, err, nkderr.CodeTrustPolicyMissing)
	_, err = NewCosignOCISigner(CosignOCIOptions{
		Identity: ociTestIdentity, Issuer: ociTestIssuer, Runner: &cosignStub{},
		Credential: Credential{Registry: "ghcr.io"},
	})
	requireSignCode(t, err, nkderr.CodeUsage)
	_, err = NewCosignOCISigner(CosignOCIOptions{
		Identity: ociTestIdentity, Issuer: ociTestIssuer, Runner: &cosignStub{},
		BinaryDigest: "not-a-digest",
	})
	requireSignCode(t, err, nkderr.CodeUnpinnedInput)
}

// The stock adapter must never execute an ambient cosign, and off Linux it has
// no exact execution primitive at all.
func TestCosignOCIStockExecutionFailsClosed(t *testing.T) {
	signer, err := NewCosignOCISigner(CosignOCIOptions{Identity: ociTestIdentity, Issuer: ociTestIssuer})
	if err != nil {
		t.Fatalf("NewCosignOCISigner: %v", err)
	}
	_, err = signer.SignAndAttest(context.Background(), ociSubject(), []byte(ociTestPredicate))
	if runtime.GOOS == "linux" {
		requireSignCode(t, err, nkderr.CodeUnpinnedInput)
		return
	}
	requireSignCode(t, err, nkderr.CodeEngineCapability)
}

// ---------------------------------------------------------------------------
// records
// ---------------------------------------------------------------------------

func TestSigningRecordValidateRefusesInconsistentRecords(t *testing.T) {
	valid := SigningRecord{
		SchemaVersion:   SigningRecordSchemaVersion,
		DryRun:          true,
		Ref:             ociTestRef,
		Digest:          ociTestDigest,
		PredicateSHA256: digestBytes([]byte(ociTestPredicate)),
		PredicateType:   ProvenancePredicateType,
		Identity:        ociTestIdentity,
		Issuer:          ociTestIssuer,
		WouldRun:        [][]string{{"cosign", "sign"}},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid record rejected: %v", err)
	}

	bad := valid
	bad.SchemaVersion = "nkd.dev/oci-signing-record/v0"
	requireSignCode(t, bad.Validate(), nkderr.CodeInvalidCatalog)

	bad = valid
	bad.PredicateSHA256 = "abc"
	requireSignCode(t, bad.Validate(), nkderr.CodeUnverifiedRef)

	bad = valid
	bad.Identity = ""
	requireSignCode(t, bad.Validate(), nkderr.CodeTrustPolicyMissing)

	bad = valid
	bad.Ran = [][]string{{"cosign", "sign"}}
	requireSignCode(t, bad.Validate(), nkderr.CodeInvalidCatalog)

	bad = valid
	bad.DryRun = false
	requireSignCode(t, bad.Validate(), nkderr.CodeInvalidCatalog)
}

func TestVerificationRecordValidateRefusesDryRunClaims(t *testing.T) {
	record := VerificationRecord{
		SchemaVersion:     VerificationRecordSchemaVersion,
		DryRun:            true,
		Ref:               ociTestRef,
		Digest:            ociTestDigest,
		Identity:          ociTestIdentity,
		Issuer:            ociTestIssuer,
		PredicateType:     ProvenancePredicateType,
		SignatureVerified: true,
		WouldRun:          [][]string{{"cosign", "verify"}},
	}
	requireSignCode(t, record.Validate(), nkderr.CodeInvalidCatalog)
	record.SignatureVerified = false
	if err := record.Validate(); err != nil {
		t.Fatalf("valid dry-run verification record rejected: %v", err)
	}
}
