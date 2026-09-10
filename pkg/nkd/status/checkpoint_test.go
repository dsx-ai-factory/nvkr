package status

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/trust"
)

const (
	checkpointAuthority = "operator-status-authority"
	checkpointSigner    = "status-current@example.test"
	checkpointIssuer    = "https://status-issuer.example.test"
)

func statusDigest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

type statusDocumentVerifier struct {
	signer string
	issuer string
}

func (verifier statusDocumentVerifier) VerifyDocument(ctx context.Context, request trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
	if err := ctx.Err(); err != nil {
		return trust.VerificationEvidence{}, err
	}
	return trust.VerificationEvidence{
		CertificateIdentities: []string{verifier.signer}, OIDCIssuer: verifier.issuer,
		CertificateSHA256: statusDigest('c'),
	}, nil
}

func authenticateStatusDocument(t *testing.T, raw []byte, signer string, role trust.SignerRole) trust.DocumentReceipt {
	t.Helper()
	return authenticateStatusDocumentWithIssuer(t, raw, signer, checkpointIssuer, role)
}

func authenticateStatusDocumentWithIssuer(t *testing.T, raw []byte, signer, issuer string, role trust.SignerRole) trust.DocumentReceipt {
	t.Helper()
	authenticator, err := trust.NewDocumentAuthenticator(statusDocumentVerifier{signer: signer, issuer: issuer},
		trust.JSONSchema[*StatusDeclaration](SchemaVersion),
		trust.JSONSchema[*StatusCheckpoint](CheckpointSchemaVersion),
	)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := trust.NewRolePolicy(role, trust.Store{OperatorIdentities: []string{signer}}, issuer)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "status-document.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	receipt, err := authenticator.Authenticate(context.Background(), trust.AuthenticateRequest{
		SourcePath: path, Role: role, Policy: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func authenticatedDeclaration(t *testing.T, declaration StatusDeclaration, signer string) trust.DocumentReceipt {
	t.Helper()
	raw, err := declaration.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return authenticateStatusDocument(t, raw, signer, trust.RoleStatus)
}

func authenticatedCheckpointDocument(t *testing.T, checkpoint StatusCheckpoint, signer string) trust.DocumentReceipt {
	t.Helper()
	raw, err := checkpoint.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return authenticateStatusDocument(t, raw, signer, trust.RoleStatus)
}

func checkpointPolicy(t *testing.T, signers ...string) CheckpointPolicy {
	t.Helper()
	policy, err := NewCheckpointPolicy(target, checkpointAuthority, checkpointIssuer, signers)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func releaseCheckpointPolicy(t *testing.T, timeAuthority string, signers ...string) CheckpointPolicy {
	t.Helper()
	policy, err := NewReleaseCheckpointPolicy(target, checkpointAuthority, checkpointIssuer,
		signers, timeAuthority)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func trustedEpoch(t *testing.T, authority string, epoch int64) TrustedTime {
	t.Helper()
	clock, err := NewTimeAuthority(authority,
		EpochSourceFunc(func(context.Context) (int64, error) { return epoch, nil }))
	if err != nil {
		t.Fatal(err)
	}
	observation, err := clock.Now(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return observation
}

func highWater(t *testing.T, sequence uint64, digest string) HighWaterMark {
	t.Helper()
	mark, err := NewHighWaterMark(target, checkpointAuthority, sequence, digest)
	if err != nil {
		t.Fatal(err)
	}
	return mark
}

func explicitAsOf(t *testing.T, epoch int64) AsOf {
	t.Helper()
	asOf, err := At(epoch)
	if err != nil {
		t.Fatal(err)
	}
	return asOf
}

func declarationDigest(t *testing.T, declaration StatusDeclaration) string {
	t.Helper()
	digest, err := declaration.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func checkpointFixture(t *testing.T) (CheckpointRequest, StatusCheckpoint, []StatusDeclaration) {
	t.Helper()
	deprecated := declaration(ActionDeprecated)
	deprecated.Reason = "maintenance"
	superseded := declaration(ActionSuperseded)
	superseded.ReplacedBy = replacement
	digests := []string{declarationDigest(t, deprecated), declarationDigest(t, superseded)}
	sort.Strings(digests)
	checkpoint := StatusCheckpoint{
		SchemaVersion: CheckpointSchemaVersion, Target: target, Authority: checkpointAuthority,
		Sequence: 10, PreviousCheckpoint: statusDigest('0'), DeclarationDigests: digests,
		IssuedEpoch: 100, ValidUntilEpoch: 300,
	}
	request := CheckpointRequest{
		Policy: checkpointPolicy(t, checkpointSigner), HighWater: highWater(t, 9, statusDigest('0')),
		AsOf: explicitAsOf(t, 200), Checkpoint: authenticatedCheckpointDocument(t, checkpoint, checkpointSigner),
		Declarations: []trust.DocumentReceipt{
			authenticatedDeclaration(t, superseded, checkpointSigner),
			authenticatedDeclaration(t, deprecated, checkpointSigner),
		},
	}
	return request, checkpoint, []StatusDeclaration{deprecated, superseded}
}

func TestCheckpointReceiptBindsAuthenticatedCompleteView(t *testing.T) {
	request, checkpoint, _ := checkpointFixture(t)
	receipt, err := VerifyCheckpoint(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := checkpoint.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := receipt.Target(); err != nil || got != target {
		t.Fatalf("target accessor: %q %v", got, err)
	}
	if got, err := receipt.Authority(); err != nil || got != checkpointAuthority {
		t.Fatalf("authority accessor: %q %v", got, err)
	}
	if got, err := receipt.Sequence(); err != nil || got != 10 {
		t.Fatalf("sequence accessor: %d %v", got, err)
	}
	if got, err := receipt.CheckpointDigest(); err != nil || got != digest {
		t.Fatalf("checkpoint digest accessor: %q %v", got, err)
	}
	if got, err := receipt.PreviousCheckpointDigest(); err != nil || got != statusDigest('0') {
		t.Fatalf("predecessor accessor: %q %v", got, err)
	}
	if got, err := receipt.AsOfEpoch(); err != nil || got != 200 {
		t.Fatalf("as-of accessor: %d %v", got, err)
	}
	if got, err := receipt.CheckpointSigner(); err != nil || got != checkpointSigner {
		t.Fatalf("signer accessor: %q %v", got, err)
	}
	if got, err := receipt.CheckpointIssuer(); err != nil || got != checkpointIssuer {
		t.Fatalf("issuer accessor: %q %v", got, err)
	}
	if binding, err := receipt.PolicyBinding(); err != nil || !isDigest(binding) {
		t.Fatalf("policy binding accessor: %q %v", binding, err)
	}
	digests, err := receipt.DeclarationDigests()
	if err != nil || len(digests) != 2 || digests[0] != checkpoint.DeclarationDigests[0] {
		t.Fatalf("complete digest set accessor: %v %v", digests, err)
	}
	digests[0] = statusDigest('f')
	again, _ := receipt.DeclarationDigests()
	if again[0] != checkpoint.DeclarationDigests[0] {
		t.Fatal("declaration digest accessor leaked mutable receipt state")
	}
	decision, err := receipt.Decision()
	if err != nil || decision.Action != ActionSuperseded || decision.ReplacedBy != replacement {
		t.Fatalf("checkpoint decision did not use its exact declarations: %+v %v", decision, err)
	}
	next, err := receipt.HighWater()
	if err != nil {
		t.Fatal(err)
	}
	if sequence, _ := next.Sequence(); sequence != 10 {
		t.Fatalf("high-water did not advance to accepted checkpoint: %d", sequence)
	}
	if nextDigest, _ := next.CheckpointDigest(); nextDigest != digest {
		t.Fatalf("high-water did not bind accepted digest: %s", nextDigest)
	}
}

func TestEmptyCheckpointSetIsCompleteAndMeaningful(t *testing.T) {
	checkpoint := StatusCheckpoint{
		SchemaVersion: CheckpointSchemaVersion, Target: target, Authority: checkpointAuthority,
		Sequence: 0, DeclarationDigests: []string{}, IssuedEpoch: 100, ValidUntilEpoch: 300,
	}
	digest, err := checkpoint.Digest()
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := VerifyCheckpoint(context.Background(), CheckpointRequest{
		Policy: checkpointPolicy(t, checkpointSigner), HighWater: highWater(t, 0, digest),
		AsOf: explicitAsOf(t, 200), Checkpoint: authenticatedCheckpointDocument(t, checkpoint, checkpointSigner),
	})
	if err != nil {
		t.Fatal(err)
	}
	digests, err := receipt.DeclarationDigests()
	if err != nil || digests == nil || len(digests) != 0 {
		t.Fatalf("empty complete set was not preserved: %#v %v", digests, err)
	}
	decision, err := receipt.Decision()
	if err != nil || decision.Effective {
		t.Fatalf("empty current status set should be effective no-op: %+v %v", decision, err)
	}
}

func TestCheckpointRejectsRollbackEquivocationAndBrokenPredecessor(t *testing.T) {
	request, checkpoint, _ := checkpointFixture(t)
	candidateDigest, err := checkpoint.Digest()
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*CheckpointRequest){
		"lower sequence replay": func(request *CheckpointRequest) {
			request.HighWater = highWater(t, 11, statusDigest('1'))
		},
		"same sequence equivocation": func(request *CheckpointRequest) {
			request.HighWater = highWater(t, checkpoint.Sequence, statusDigest('e'))
		},
		"broken predecessor": func(request *CheckpointRequest) {
			request.HighWater = highWater(t, 9, statusDigest('9'))
		},
		"skipped sequence": func(request *CheckpointRequest) {
			request.HighWater = highWater(t, 8, statusDigest('0'))
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			adversarial := request
			mutate(&adversarial)
			if _, err := VerifyCheckpoint(context.Background(), adversarial); code(err) != nkderr.CodeStatusCheckpoint {
				t.Fatalf("anti-rollback defect did not fail closed: %v", err)
			}
		})
	}

	idempotent := request
	idempotent.HighWater = highWater(t, checkpoint.Sequence, candidateDigest)
	if _, err := VerifyCheckpoint(context.Background(), idempotent); err != nil {
		t.Fatalf("same exact checkpoint should be idempotent, not equivocation: %v", err)
	}
}

func TestTwoSuccessiveCheckpointsRejectOldReplayFromEveryDiscoveryAdapter(t *testing.T) {
	firstRequest, first, _ := checkpointFixture(t)
	firstReceipt, err := VerifyCheckpoint(context.Background(), firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	firstHighWater, err := firstReceipt.HighWater()
	if err != nil {
		t.Fatal(err)
	}
	firstDigest, _ := firstReceipt.CheckpointDigest()

	second := first
	second.Sequence = 11
	second.PreviousCheckpoint = firstDigest
	second.IssuedEpoch = 150
	second.ValidUntilEpoch = 400
	secondRequest := firstRequest
	secondRequest.HighWater = firstHighWater
	secondRequest.Checkpoint = authenticatedCheckpointDocument(t, second, checkpointSigner)
	secondReceipt, err := VerifyCheckpoint(context.Background(), secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := secondReceipt.HighWater()
	if err != nil {
		t.Fatal(err)
	}

	for _, adapter := range []string{"registry-referrer", "air-gap-bundle"} {
		t.Run(adapter, func(t *testing.T) {
			replay := firstRequest
			replay.HighWater = latest
			if _, err := VerifyCheckpoint(context.Background(), replay); code(err) != nkderr.CodeStatusCheckpoint {
				t.Fatalf("%s replayed older checkpoint without rollback rejection: %v", adapter, err)
			}
		})
	}
}

func TestCheckpointRejectsIncompleteOrExtraneousDeclarationSet(t *testing.T) {
	request, _, _ := checkpointFixture(t)
	extra := declaration(ActionDeprecated)
	extra.Reason = "not named"

	tests := map[string]func(*CheckpointRequest){
		"missing": func(request *CheckpointRequest) { request.Declarations = request.Declarations[:1] },
		"duplicate": func(request *CheckpointRequest) {
			request.Declarations = append(request.Declarations, request.Declarations[0])
		},
		"extra": func(request *CheckpointRequest) {
			request.Declarations = append(request.Declarations, authenticatedDeclaration(t, extra, checkpointSigner))
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			adversarial := request
			adversarial.Declarations = append([]trust.DocumentReceipt(nil), request.Declarations...)
			mutate(&adversarial)
			if _, err := VerifyCheckpoint(context.Background(), adversarial); code(err) != nkderr.CodeStatusCheckpoint {
				t.Fatalf("incomplete/extraneous set did not fail closed: %v", err)
			}
		})
	}
}

func TestCheckpointRequiresFreshExplicitAsOf(t *testing.T) {
	request, _, _ := checkpointFixture(t)
	tests := map[string]AsOf{
		"omitted":      WithoutAsOf(),
		"before issue": explicitAsOf(t, 99),
		"expired":      explicitAsOf(t, 301),
	}
	for name, asOf := range tests {
		t.Run(name, func(t *testing.T) {
			adversarial := request
			adversarial.AsOf = asOf
			if _, err := VerifyCheckpoint(context.Background(), adversarial); code(err) != nkderr.CodeStatusCheckpoint {
				t.Fatalf("freshness defect did not fail closed: %v", err)
			}
		})
	}
}

func TestCheckpointRequiresAuthenticatedRoleAndCurrentPolicySigner(t *testing.T) {
	request, checkpoint, _ := checkpointFixture(t)

	zeroDocument := request
	zeroDocument.Checkpoint = trust.DocumentReceipt{}
	if _, err := VerifyCheckpoint(context.Background(), zeroDocument); code(err) != nkderr.CodeSignatureInvalid {
		t.Fatalf("zero document receipt was accepted: %v", err)
	}

	wrongRole := request
	raw, err := checkpoint.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	wrongRole.Checkpoint = authenticateStatusDocument(t, raw, checkpointSigner, trust.RoleConformance)
	if _, err := VerifyCheckpoint(context.Background(), wrongRole); code(err) != nkderr.CodeSignatureInvalid {
		t.Fatalf("wrong role document receipt was accepted: %v", err)
	}

	rotatedSigner := "rotated@example.test"
	unauthorized := request
	unauthorized.Checkpoint = authenticatedCheckpointDocument(t, checkpoint, rotatedSigner)
	if _, err := VerifyCheckpoint(context.Background(), unauthorized); code(err) != nkderr.CodeUntrustedSigner {
		t.Fatalf("authenticated but policy-unauthorized signer was accepted: %v", err)
	}

	authorizedRotation := unauthorized
	authorizedRotation.Policy = checkpointPolicy(t, checkpointSigner, rotatedSigner)
	if _, err := VerifyCheckpoint(context.Background(), authorizedRotation); err != nil {
		t.Fatalf("currently authorized rotated checkpoint signer was rejected: %v", err)
	}

	wrongIssuer := request
	raw, err = checkpoint.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	wrongIssuer.Checkpoint = authenticateStatusDocumentWithIssuer(t, raw, checkpointSigner,
		"https://attacker-issuer.example.test", trust.RoleStatus)
	if _, err := VerifyCheckpoint(context.Background(), wrongIssuer); code(err) != nkderr.CodeUntrustedSigner {
		t.Fatalf("same signer string authenticated under the wrong issuer was accepted: %v", err)
	}
}

func TestCheckpointAdapterRehydratesExactAuthenticatedBytes(t *testing.T) {
	request, _, _ := checkpointFixture(t)
	decoded, ok := request.Checkpoint.Decoded().(*StatusCheckpoint)
	if !ok {
		t.Fatalf("test receipt decoded as %T", request.Checkpoint.Decoded())
	}
	decoded.Target = statusDigest('f')
	decoded.DeclarationDigests = nil
	receipt, err := VerifyCheckpoint(context.Background(), request)
	if err != nil {
		t.Fatalf("adapter trusted mutable decoded pointer instead of exact authenticated bytes: %v", err)
	}
	if got, _ := receipt.Target(); got != target {
		t.Fatalf("receipt used mutated decoded target %s", got)
	}
}

func TestCheckpointZeroValuesAndPostAcceptanceMutationFailClosed(t *testing.T) {
	var zero CheckpointReceipt
	if _, err := zero.CheckpointDigest(); code(err) != nkderr.CodeStatusCheckpoint {
		t.Fatalf("zero receipt digest accessor did not fail closed: %v", err)
	}
	if _, err := zero.AsOfEpoch(); code(err) != nkderr.CodeStatusCheckpoint {
		t.Fatalf("zero receipt as-of accessor did not fail closed: %v", err)
	}
	if _, err := zero.CheckpointIssuer(); code(err) != nkderr.CodeStatusCheckpoint {
		t.Fatalf("zero receipt issuer accessor did not fail closed: %v", err)
	}
	if _, err := zero.HighWater(); code(err) != nkderr.CodeStatusCheckpoint {
		t.Fatalf("zero receipt high-water accessor did not fail closed: %v", err)
	}
	var zeroMark HighWaterMark
	if _, err := zeroMark.CheckpointDigest(); code(err) != nkderr.CodeStatusCheckpoint {
		t.Fatalf("zero high-water accessor did not fail closed: %v", err)
	}

	request, _, _ := checkpointFixture(t)
	receipt, err := VerifyCheckpoint(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	receipt.checkpoint.DeclarationDigests[0] = statusDigest('f')
	if _, err := receipt.CheckpointDigest(); code(err) != nkderr.CodeStatusCheckpoint {
		t.Fatalf("mutated receipt remained usable: %v", err)
	}
}

func TestCheckpointDecisionHonorsOnlyEffectiveDeclarations(t *testing.T) {
	immediate := declaration(ActionDeprecated)
	future := declaration(ActionWithdrawn)
	future.EffectiveEpoch = 250
	digests := []string{declarationDigest(t, immediate), declarationDigest(t, future)}
	sort.Strings(digests)
	checkpoint := StatusCheckpoint{
		SchemaVersion: CheckpointSchemaVersion, Target: target, Authority: checkpointAuthority,
		Sequence: 4, PreviousCheckpoint: statusDigest('3'), DeclarationDigests: digests,
		IssuedEpoch: 100, ValidUntilEpoch: 400,
	}
	base := CheckpointRequest{
		Policy: checkpointPolicy(t, checkpointSigner), HighWater: highWater(t, 3, statusDigest('3')),
		Checkpoint: authenticatedCheckpointDocument(t, checkpoint, checkpointSigner),
		Declarations: []trust.DocumentReceipt{
			authenticatedDeclaration(t, future, checkpointSigner),
			authenticatedDeclaration(t, immediate, checkpointSigner),
		},
	}
	base.AsOf = explicitAsOf(t, 200)
	before, err := VerifyCheckpoint(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := before.Decision()
	if err != nil || decision.Action != ActionDeprecated {
		t.Fatalf("future withdrawal applied too early: %+v %v", decision, err)
	}
	base.AsOf = explicitAsOf(t, 300)
	after, err := VerifyCheckpoint(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := after.Decision(); code(err) != nkderr.CodeWithdrawn {
		t.Fatalf("effective authenticated withdrawal did not fail closed: %v", err)
	}

	// A caller-selected historical epoch remains a useful diagnostic, but it has
	// no trusted-time capability and therefore cannot cross the release boundary.
	if _, err := before.TrustedTimeBinding(); code(err) != nkderr.CodeStatusCheckpoint {
		t.Fatalf("caller-selected historical epoch acquired release authority: %v", err)
	}
	const timeAuthority = "release-policy-clock"
	releaseReceipt, err := VerifyCheckpointForRelease(context.Background(), ReleaseCheckpointRequest{
		Policy:       releaseCheckpointPolicy(t, timeAuthority, checkpointSigner),
		HighWater:    base.HighWater,
		Time:         trustedEpoch(t, timeAuthority, 300),
		Checkpoint:   base.Checkpoint,
		Declarations: base.Declarations,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := releaseReceipt.Decision(); code(err) != nkderr.CodeWithdrawn {
		t.Fatalf("trusted current epoch failed to enforce effective withdrawal: %v", err)
	}
	if authority, err := releaseReceipt.TrustedTimeAuthority(); err != nil || authority != timeAuthority {
		t.Fatalf("release receipt lost trusted-time authority: %q %v", authority, err)
	}
	if binding, err := releaseReceipt.TrustedTimeBinding(); err != nil || !isDigest(binding) {
		t.Fatalf("release receipt lost trusted-time identity: %q %v", binding, err)
	}
}

func TestTrustedTimeFailsClosedAndMustMatchReleasePolicy(t *testing.T) {
	//nolint:staticcheck // Nil admission is the public boundary behavior under test.
	if _, err := (TimeAuthority{}).Now(nil); code(err) != nkderr.CodeUsage {
		t.Fatalf("nil trusted-time context code = %q, want usage (%v)", code(err), err)
	}
	if _, err := (TrustedTime{}).Epoch(); code(err) != nkderr.CodeStatusCheckpoint {
		t.Fatalf("zero trusted time did not fail closed: %v", err)
	}
	if _, err := NewTimeAuthority("clock", EpochSourceFunc(nil)); code(err) != nkderr.CodeTrustPolicyMissing {
		t.Fatalf("typed-nil epoch source was accepted: %v", err)
	}
	zeroClock, err := NewTimeAuthority("clock",
		EpochSourceFunc(func(context.Context) (int64, error) { return 0, nil }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zeroClock.Now(context.Background()); code(err) != nkderr.CodeStatusCheckpoint {
		t.Fatalf("zero trusted epoch was accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancelingClock, err := NewTimeAuthority("clock",
		EpochSourceFunc(func(context.Context) (int64, error) {
			cancel()
			return 0, ctx.Err()
		}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cancelingClock.Now(ctx); code(err) != nkderr.CodeTimeout || !nkderr.Retryable(err) {
		t.Fatalf("trusted-time adapter cancellation = %v, want retryable timeout", err)
	}

	request, _, _ := checkpointFixture(t)
	if _, err := VerifyCheckpointForRelease(context.Background(), ReleaseCheckpointRequest{
		Policy: request.Policy, HighWater: request.HighWater,
		Time: trustedEpoch(t, "clock", 200), Checkpoint: request.Checkpoint,
		Declarations: request.Declarations,
	}); code(err) != nkderr.CodeTrustPolicyMissing {
		t.Fatalf("diagnostic checkpoint policy authorized release time: %v", err)
	}
	if _, err := VerifyCheckpointForRelease(context.Background(), ReleaseCheckpointRequest{
		Policy:    releaseCheckpointPolicy(t, "pinned-clock", checkpointSigner),
		HighWater: request.HighWater, Time: trustedEpoch(t, "other-clock", 200),
		Checkpoint: request.Checkpoint, Declarations: request.Declarations,
	}); code(err) != nkderr.CodeTrustPolicyMissing {
		t.Fatalf("untrusted clock identity bypassed pinned release policy: %v", err)
	}

	observation := trustedEpoch(t, "clock", 200)
	binding, err := observation.Binding()
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTrustedTimeBinding("clock", 200, binding); err != nil {
		t.Fatalf("versioned trusted-time binding did not rehydrate: %v", err)
	}
	legacyBinding, err := canonical.Digest(canonical.KindTrustedTime, struct {
		Authority string `json:"authority"`
		Epoch     int64  `json:"epoch"`
	}{Authority: "clock", Epoch: 200})
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTrustedTimeBinding("clock", 200, legacyBinding); code(err) != nkderr.CodeStatusCheckpoint {
		t.Fatalf("unversioned trusted-time core was accepted: %v", err)
	}
}

func TestCheckpointConcurrentVerificationAndAccess(t *testing.T) {
	request, _, _ := checkpointFixture(t)
	const workers = 20
	const iterations = 20
	errCh := make(chan error, workers)
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < iterations; iteration++ {
				receipt, err := VerifyCheckpoint(context.Background(), request)
				if err != nil {
					errCh <- err
					return
				}
				if _, err := receipt.CheckpointDigest(); err != nil {
					errCh <- err
					return
				}
				if _, err := receipt.HighWater(); err != nil {
					errCh <- err
					return
				}
			}
		}()
	}
	wait.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent status verification failed: %v", err)
	}
}

func TestCheckpointCanonicalShapeRejectsAmbiguity(t *testing.T) {
	checkpoint := StatusCheckpoint{
		SchemaVersion: CheckpointSchemaVersion, Target: target, Authority: checkpointAuthority,
		Sequence: 1, PreviousCheckpoint: statusDigest('0'), IssuedEpoch: 1, ValidUntilEpoch: 2,
	}
	if code(checkpoint.Validate()) != nkderr.CodeStatusCheckpoint {
		t.Fatal("nil declaration set was accepted instead of explicit empty completeness")
	}
	checkpoint.DeclarationDigests = []string{statusDigest('b'), statusDigest('a')}
	if code(checkpoint.Validate()) != nkderr.CodeStatusCheckpoint {
		t.Fatal("unsorted declaration set was accepted")
	}
	checkpoint.DeclarationDigests = []string{}
	raw, err := checkpoint.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	want, err := canonical.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(want) || strings.Contains(string(raw), "\n") {
		t.Fatalf("checkpoint signing bytes are not exact canonical JSON: %q", raw)
	}
}
