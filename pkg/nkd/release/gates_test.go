package release

import (
	"context"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestReproductionReceiptBindsProfileResolvedInputAndPassingVerdict(t *testing.T) {
	inv := inventory()
	set := releaseSet(t, inv)
	req := authorizationRequest(t, inv, set)
	authenticator := receiptAuthenticator{signers: map[ReceiptRole]string{RoleReproduction: "reproduction-signer"}}
	base := req.Reproduction[0].result

	for _, test := range []struct {
		name   string
		mutate func(*ReproductionResult)
	}{
		{"profile", func(result *ReproductionResult) {
			result.ProfileDigest = digest('a')
			result.ProfileIndependent = false
		}},
		{"resolved-input", func(result *ReproductionResult) { result.ResolvedInputDigest = digest('b') }},
		{"artifact", func(result *ReproductionResult) { result.ArtifactDigests[0] = digest('c') }},
		{"release", func(result *ReproductionResult) { result.ReleaseSetDigest = digest('d') }},
	} {
		t.Run(test.name, func(t *testing.T) {
			copy := base
			copy.ArtifactDigests = append([]string(nil), base.ArtifactDigests...)
			test.mutate(&copy)
			if _, err := VerifyReproduction(context.Background(), copy, []byte("sig"), req.Release, authenticator); code(err) != nkderr.CodeGateUnmet {
				t.Fatalf("reproduction substitution must fail exact binding: %v", err)
			}
		})
	}

	failed := base
	failed.ArtifactDigests = append([]string(nil), base.ArtifactDigests...)
	failed.Verdict = ReproductionFailed
	receipt, err := VerifyReproduction(context.Background(), failed, []byte("sig"), req.Release, authenticator)
	if err != nil {
		t.Fatalf("authenticated failed result should remain auditable: %v", err)
	}
	req.Reproduction[0] = receipt
	if _, err := Authorize(context.Background(), req); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("authenticated failed reproduction must not promote: %v", err)
	}

	variant := base
	variant.ArtifactDigests = append([]string(nil), base.ArtifactDigests...)
	variant.Verdict = "validated-variant"
	if _, err := VerifyReproduction(context.Background(), variant, []byte("sig"), req.Release, authenticator); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("variant without authenticated semantic-equivalence and expiry bindings must not mint release authority: %v", err)
	}
}

func TestCompatibilityReceiptIsExhaustivePolicyBoundAndCurrent(t *testing.T) {
	inv := inventory()
	set := releaseSet(t, inv)
	req := authorizationRequest(t, inv, set)
	authenticator := receiptAuthenticator{signers: map[ReceiptRole]string{RoleCompatibility: "compatibility-signer"}}
	base := req.Compatibility.result

	omitted := base
	omitted.ArtifactDigests = append([]string(nil), base.ArtifactDigests[1:]...)
	if _, err := VerifyCompatibility(context.Background(), omitted, []byte("sig"), req.Release, inv, authenticator); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("non-exhaustive compatibility receipt must fail: %v", err)
	}

	replayed := req
	replayed.PromotionPolicy = verifiedPromotionPolicy(t, digest('a'),
		req.PromotionPolicy.policy.ConformancePolicyDigest,
		[]PromotionRule{vulnerabilityPromotionRule(ArtifactHostPackage)})
	if _, err := Authorize(context.Background(), replayed); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("compatibility receipt replay under another policy must fail: %v", err)
	}
	expiredResult := base
	expiredResult.ExpiresEpoch = 5
	expiredReceipt, err := VerifyCompatibility(context.Background(), expiredResult, []byte("sig"), req.Release, inv, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	expired := req
	expired.Compatibility = expiredReceipt
	if _, err := Authorize(context.Background(), expired); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("expired compatibility receipt must fail: %v", err)
	}

	mutated := req
	mutated.Compatibility.result.ArtifactDigests = append([]string(nil), req.Compatibility.result.ArtifactDigests...)
	mutated.Compatibility.result.ArtifactDigests[0] = digest('b')
	if _, err := Authorize(context.Background(), mutated); code(err) != nkderr.CodeSignatureInvalid {
		t.Fatalf("post-authentication compatibility mutation must fail: %v", err)
	}
}

func TestConformanceReceiptIsExhaustivePolicyBoundAndCurrent(t *testing.T) {
	inv := inventory()
	set := releaseSet(t, inv)
	req := authorizationRequest(t, inv, set)
	authenticator := receiptAuthenticator{signers: map[ReceiptRole]string{RoleConformance: "conformance-signer"}}
	base := req.Conformance[0].result

	omitted := base
	omitted.VectorDigests = append([]string(nil), base.VectorDigests[1:]...)
	if _, err := AuthenticateConformance(context.Background(), omitted, []byte("sig"), req.Release, authenticator); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("non-exhaustive conformance receipt must fail: %v", err)
	}
	replayed := req
	replayed.PromotionPolicy = verifiedPromotionPolicy(t,
		req.PromotionPolicy.policy.CompatibilityPolicyDigest, digest('a'),
		[]PromotionRule{vulnerabilityPromotionRule(ArtifactHostPackage)})
	if _, err := Authorize(context.Background(), replayed); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("conformance receipt replay under another suite policy must fail: %v", err)
	}
	expiredResult := base
	expiredResult.ExpiresEpoch = 5
	expiredReceipt, err := AuthenticateConformance(context.Background(), expiredResult, []byte("sig"), req.Release, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	expired := req
	expired.Conformance = []ConformanceReceipt{expiredReceipt}
	if _, err := Authorize(context.Background(), expired); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("expired conformance receipt must fail: %v", err)
	}
	failed := base
	failed.Verdict = ConformanceFailed
	failedReceipt, err := AuthenticateConformance(context.Background(), failed, []byte("sig"), req.Release, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	req.Conformance = []ConformanceReceipt{failedReceipt}
	if _, err := Authorize(context.Background(), req); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("authenticated failed conformance must not promote: %v", err)
	}
}

func TestGateAuthenticationRequiresDedicatedRoleAndHonorsCancellation(t *testing.T) {
	inv := inventory()
	set := releaseSet(t, inv)
	req := authorizationRequest(t, inv, set)
	missingRole := receiptAuthenticator{signers: map[ReceiptRole]string{RoleCompatibility: "wrong-role"}}
	if _, err := VerifyReproduction(context.Background(), req.Reproduction[0].result, []byte("sig"), req.Release, missingRole); code(err) != nkderr.CodeSignatureInvalid {
		t.Fatalf("reproduction must require dedicated authenticated role: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := VerifyCompatibility(ctx, req.Compatibility.result, []byte("sig"), req.Release, inv, missingRole); code(err) != nkderr.CodeTimeout {
		t.Fatalf("gate authentication cancellation must be timeout-coded: %v", err)
	}
}

func TestReleaseReceiptFamiliesAreDomainSeparatedAndRejectDigestSubstitution(t *testing.T) {
	inv := inventory()
	set := releaseSet(t, inv)
	req := authorizationRequest(t, inv, set)
	waiverRequest, waiver := evidenceNoneAuthorization(t)

	families := []struct {
		name     string
		kind     string
		document any
		got      string
	}{
		{"promotion-policy", canonical.KindPromotionPolicy, req.PromotionPolicy.policy, req.PromotionPolicy.digest},
		{"reproduction", canonical.KindReproductionResult, req.Reproduction[0].result, req.Reproduction[0].digest},
		{"compatibility", canonical.KindCompatibilityResult, req.Compatibility.result, req.Compatibility.digest},
		{"conformance", canonical.KindConformanceResult, req.Conformance[0].result, req.Conformance[0].digest},
		{"evidence-waiver", canonical.KindEvidenceWaiver, waiver.document, waiver.digest},
	}
	for _, family := range families {
		t.Run(family.name+"-expected-domain", func(t *testing.T) {
			want, err := canonical.Digest(family.kind, family.document)
			if err != nil {
				t.Fatal(err)
			}
			if family.got != want {
				t.Fatalf("receipt digest %s does not use family %s: want %s", family.got, family.kind, want)
			}
			legacy, err := canonical.Digest(canonical.KindEvidence, family.document)
			if err != nil {
				t.Fatal(err)
			}
			if legacy == family.got {
				t.Fatalf("receipt family %s collapsed into generic evidence domain", family.name)
			}
		})
	}

	// The same canonical bytes must have a different identity in every release
	// receipt family, including the generic evidence-bundle family.
	seen := map[string]string{}
	for _, kind := range []string{
		canonical.KindPromotionPolicy, canonical.KindReproductionResult,
		canonical.KindCompatibilityResult, canonical.KindConformanceResult,
		canonical.KindEvidenceWaiver, canonical.KindEvidence,
	} {
		digest, err := canonical.Digest(kind, req.Reproduction[0].result)
		if err != nil {
			t.Fatal(err)
		}
		if prior := seen[digest]; prior != "" {
			t.Fatalf("release receipt families %s and %s share digest %s", prior, kind, digest)
		}
		seen[digest] = kind
	}

	wrongDomain, err := canonical.Digest(canonical.KindCompatibilityResult, req.Reproduction[0].result)
	if err != nil {
		t.Fatal(err)
	}
	substituted := req
	substituted.Reproduction = append([]ReproductionReceipt(nil), req.Reproduction...)
	substituted.Reproduction[0].digest = wrongDomain
	if _, err := Authorize(context.Background(), substituted); code(err) != nkderr.CodeSignatureInvalid {
		t.Fatalf("cross-family reproduction digest substitution must fail: %v", err)
	}

	wrongWaiverDomain, err := canonical.Digest(canonical.KindPromotionPolicy, waiver.document)
	if err != nil {
		t.Fatal(err)
	}
	waiverRequest.EvidenceWaivers = append([]EvidenceWaiverReceipt(nil), waiverRequest.EvidenceWaivers...)
	waiverRequest.EvidenceWaivers[0].digest = wrongWaiverDomain
	if _, err := Authorize(context.Background(), waiverRequest); code(err) != nkderr.CodeSignatureInvalid {
		t.Fatalf("cross-family waiver digest substitution must fail: %v", err)
	}
}
