// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"sync"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

const ArtifactCheckGateVersion = "nvkr.dev/artifact-checks/v1"

// QualificationPredicate is untrusted input until its exact bytes pass both
// Plan semantics and cryptographic verification for the independently selected
// reference. One predicate is required for the root and each named output.
type QualificationPredicate struct {
	Reference string `json:"reference"`
	Predicate []byte `json:"predicate"`
}

type QualificationRequest struct {
	Reference   string
	Definition  Definition
	Environment Environment
	Plan        *factory.Plan
	Origin      *qualificationorigin.SourceRun
	Predicates  []QualificationPredicate
}

// ArtifactCheckResult is diagnostic data. Passing strings supplied by a caller
// cannot issue an ArtifactCheckReceipt or any signing authority.
type ArtifactCheckResult struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type ArtifactCheckOutput struct {
	Name          string                      `json:"name"`
	Kind          string                      `json:"kind"`
	Reference     string                      `json:"reference"`
	Subject       string                      `json:"subject"`
	BuildDigest   string                      `json:"buildDigest"`
	Platform      string                      `json:"platform,omitempty"`
	ImageIndex    *delivery.ImageIndexBinding `json:"imageIndex,omitempty"`
	BuildSubjects []delivery.BuildSubject     `json:"buildSubjects"`
	Checks        []ArtifactCheckResult       `json:"checks"`
	Overall       string                      `json:"overall"`
	DiskEvidence  string                      `json:"diskEvidence,omitempty"`
}

type ArtifactCheckDescription struct {
	GateVersion   string                          `json:"gateVersion"`
	Origin        qualificationorigin.Description `json:"origin"`
	Inputs        ExactInputDescription           `json:"inputs"`
	CatalogDigest string                          `json:"catalogDigest"`
	Outputs       []ArtifactCheckOutput           `json:"outputs"`
}

// ArtifactCheckReceipt binds the complete successful gate to an authenticated
// source Run and immutable check bytes. It grants no qualification signing or
// smoke execution authority and cannot be restored by deserialization.
type ArtifactCheckReceipt struct {
	origin   qualificationSource
	raw      string
	lifetime *artifactCheckLifetime
}

type artifactCheckLifetime struct {
	mu               sync.RWMutex
	closed           bool
	closeSource      func() error
	closeErr         error
	closeDone        chan struct{}
	verified         *VerifiedRelease
	plan             *factory.Plan
	members          []QualificationReleaseMember
	operationContext context.Context
	stopOperations   context.CancelFunc
	operations       sync.WaitGroup
}

// Private test seam; the public request accepts only the provider-issued type.
type qualificationSource interface {
	Description() (qualificationorigin.Description, error)
	Require() error
}

func (receipt *ArtifactCheckReceipt) Description() (ArtifactCheckDescription, error) {
	if receipt == nil || receipt.origin == nil || receipt.raw == "" || receipt.lifetime == nil {
		return ArtifactCheckDescription{}, e.New(e.CodeGateUnmet, "artifact checks have not authenticated the complete set")
	}
	receipt.lifetime.mu.RLock()
	defer receipt.lifetime.mu.RUnlock()
	if receipt.lifetime.closed {
		return ArtifactCheckDescription{}, e.New(e.CodeGateUnmet, "artifact check receipt is closed")
	}
	if _, err := receipt.origin.Description(); err != nil {
		return ArtifactCheckDescription{}, err
	}
	var description ArtifactCheckDescription
	if err := canonical.UnmarshalExact([]byte(receipt.raw), &description); err != nil {
		return ArtifactCheckDescription{}, e.Wrap(e.CodeGateUnmet, err, "reading immutable artifact checks")
	}
	return description, nil
}

// Close revokes this issuance and all shallow copies. Consumer.CheckQualification
// borrows its SourceRun, so closing that receipt leaves the caller's source alive.
// CheckQualificationFiles owns its source and releases it with the receipt.
func (receipt *ArtifactCheckReceipt) Close() error {
	if receipt == nil || receipt.lifetime == nil {
		return nil
	}
	state := receipt.lifetime
	state.mu.Lock()
	if state.closed {
		done := state.closeDone
		state.mu.Unlock()
		if done != nil {
			<-done
		}
		state.mu.RLock()
		defer state.mu.RUnlock()
		return state.closeErr
	}
	state.closed = true
	state.closeDone = make(chan struct{})
	stop := state.stopOperations
	state.mu.Unlock()
	if stop != nil {
		stop()
	}
	// No operation can register after closed is set. Join its streaming and
	// process cleanup before releasing any verified publication bytes.
	state.operations.Wait()
	var closeErr error
	if state.verified != nil {
		closeErr = state.verified.Close()
	}
	if state.closeSource != nil {
		closeErr = errors.Join(closeErr, state.closeSource())
	}
	state.mu.Lock()
	state.closeErr = closeErr
	state.plan = nil
	close(state.closeDone)
	state.mu.Unlock()
	return closeErr
}

// beginOperation is private: holding an artifact receipt never exposes payload
// paths or grants execution. The owning gate registers work here so Close can
// revoke, cancel and join before releasing its held publication.
func (receipt *ArtifactCheckReceipt) beginOperation(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil {
		return nil, nil, e.New(e.CodeUsage, "artifact operation requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, e.Wrap(e.CodeTimeout, err, "artifact operation canceled")
	}
	if receipt == nil || receipt.lifetime == nil || receipt.origin == nil || receipt.raw == "" {
		return nil, nil, e.New(e.CodeGateUnmet, "artifact checks have not authenticated the complete set")
	}
	state := receipt.lifetime
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.verified == nil || state.plan == nil {
		return nil, nil, e.New(e.CodeGateUnmet, "artifact check receipt has no live verified payload")
	}
	if err := receipt.origin.Require(); err != nil {
		return nil, nil, err
	}
	if state.operationContext == nil {
		state.operationContext, state.stopOperations = context.WithCancel(context.WithoutCancel(ctx))
	}
	operation, cancel := context.WithCancel(ctx)
	stopCancellation := context.AfterFunc(state.operationContext, cancel)
	state.operations.Add(1)
	var finished sync.Once
	return operation, func() { finished.Do(func() { stopCancellation(); cancel(); state.operations.Done() }) }, nil
}

// CheckQualification authenticates the full delivered closure against the
// producing Run and exact inputs. Diagnostics survive failed output checks;
// only an entirely successful set produces the opaque receipt. All payloads
// are read from the existing publication; this operation never rebuilds them.
func (consumer *Consumer) CheckQualification(ctx context.Context, request QualificationRequest) (*ArtifactCheckReceipt, *ArtifactCheckDescription, error) {
	if ctx == nil {
		return nil, nil, e.New(e.CodeUsage, "artifact qualification requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, e.Wrap(e.CodeTimeout, err, "artifact qualification canceled")
	}
	return consumer.checkQualification(ctx, request, request.Origin)
}

func (consumer *Consumer) checkQualification(ctx context.Context, request QualificationRequest, source qualificationSource) (*ArtifactCheckReceipt, *ArtifactCheckDescription, error) {
	// Invalid source and Plan inputs take diagnostic precedence over missing
	// consumer adapters. Every admission check still precedes external work.
	if source == nil {
		return nil, nil, e.New(e.CodeProvenanceMissing, "artifact qualification requires an authenticated producing Run")
	}
	// A nil request.Origin becomes a typed-nil source here. SourceRun.Description
	// rejects it through the source owner's nil-safe Require guard.
	origin, err := source.Description()
	if err != nil {
		return nil, nil, err
	}
	// Freeze the complete graph before external work. Caller mutation cannot
	// change later pin, provenance or membership checks.
	if request.Plan == nil {
		return nil, nil, e.New(e.CodeUsage, "artifact qualification requires a compiled Plan")
	}
	if err := request.Plan.Validate(); err != nil {
		return nil, nil, err
	}
	raw, err := canonical.Marshal(request.Plan)
	if err != nil {
		return nil, nil, err
	}
	var plan factory.Plan
	if err := canonical.UnmarshalExact(raw, &plan); err != nil {
		return nil, nil, e.Wrap(e.CodeVerifyFailed, err, "freezing qualification Plan")
	}
	if err := factory.AdmitQualificationHandoff(&plan); err != nil {
		return nil, nil, err
	}
	expectation, err := PrepareExactInputs(request.Definition, request.Environment, &plan)
	if err != nil {
		return nil, nil, err
	}
	signing := plan.Environment.Signing
	if signing == nil || signing.Mode != factory.SigningModeCosign || signing.Issuer != "https://token.actions.githubusercontent.com" {
		return nil, nil, e.New(e.CodeEngineCapability, "artifact qualification requires live GitHub build signing")
	}
	pattern, err := regexp.Compile(signing.Identity)
	if err != nil || !pattern.MatchString(origin.BuilderID) {
		return nil, nil, e.New(e.CodeUntrustedSigner, "originating workflow differs from the expected build identity")
	}
	policy := VerificationPolicy{Lane: LanePipeline, Scope: ScopeArtifactPublication,
		IdentityRegexp: "^" + regexp.QuoteMeta(origin.BuilderID) + "$", Issuer: signing.Issuer,
		MinimumDiskEvidence: DiskEvidenceBuildProvenance}
	if consumer == nil || (consumer.verifier == nil && consumer.verifierFor == nil) || consumer.materialize == nil {
		return nil, nil, e.New(e.CodeEngineCapability, "artifact qualification requires signature and registry adapters")
	}
	verifier := consumer.verifier
	if verifier == nil {
		verifier, err = consumer.verifierFor(policy)
		if err != nil {
			return nil, nil, err
		}
	}
	attester, ok := verifier.(sign.SBOMAttester)
	if !ok {
		return nil, nil, e.New(e.CodeEngineCapability, "artifact qualification requires authenticated SBOM verification")
	}
	predicates, err := qualificationPredicates(request.Predicates, &plan, origin)
	if err != nil {
		return nil, nil, err
	}
	if _, found := predicates[request.Reference]; !found {
		return nil, nil, e.New(e.CodeProvenanceMissing, "qualification omits the exact release production predicate")
	}
	verified, inputs, err := consumer.VerifyExactInputs(ctx, request.Reference, policy, expectation)
	if err != nil {
		return nil, nil, err
	}
	transferred := false
	defer func() {
		if !transferred {
			_ = verified.Close()
		}
	}()
	inputDescription, err := inputs.Description()
	if err != nil {
		return nil, nil, err
	}
	publication, err := verified.publication.Description(request.Reference)
	if err != nil {
		return nil, nil, err
	}
	if publication.Metadata.InvocationID != origin.InvocationID {
		return nil, nil, e.New(e.CodeVerifyFailed, "publication belongs to another producing Run attempt")
	}
	if len(predicates) != len(publication.Outputs)+1 {
		return nil, nil, e.New(e.CodeReleaseIncomplete, "qualification predicates do not cover the exact root and output set")
	}
	for _, output := range publication.Outputs {
		if _, found := predicates[output.Reference]; !found {
			return nil, nil, e.New(e.CodeProvenanceMissing, "qualification omits production provenance for output %q", output.Name)
		}
	}
	report := &ArtifactCheckDescription{GateVersion: ArtifactCheckGateVersion, Origin: origin,
		Inputs: inputDescription, CatalogDigest: plan.CatalogDigest}
	var failures []error
	rootErr := verifyQualificationSubject(ctx, verifier, request.Reference, predicates[request.Reference], policy, origin)
	evidenceFailures := map[string][]error{}
	evidenceErr := verified.publication.VisitEvidence(ctx, func(ctx context.Context, binding delivery.EvidenceBinding, bundle []byte) error {
		if err := attester.VerifySBOM(ctx, binding.SubjectDigest, bundle); err != nil {
			for _, output := range binding.Outputs {
				evidenceFailures[output] = append(evidenceFailures[output], err)
			}
		}
		return nil // Every evidence slot receives its own verification attempt.
	})
	if evidenceErr != nil {
		// A traversal failure leaves complete-set coverage unproved, including
		// outputs with no slot-specific verification failure. Preserve its code
		// and cause while distinguishing its scope in every affected diagnostic.
		evidenceErr = fmt.Errorf("incomplete evidence traversal for the complete publication: %w", evidenceErr)
	}
	for i, output := range publication.Outputs {
		checks := []ArtifactCheckResult{{Name: "schema-and-membership", Status: "pass"}}
		memberEvidenceErrs := evidenceFailures[output.Name]
		outputEvidenceErr := errors.Join(errors.Join(memberEvidenceErrs...), evidenceErr)
		var outputRunOriginErr error
		if output.Publication != nil && (output.Publication.PreparedInvocationID != origin.InvocationID || output.Publication.SignedInvocationID != origin.InvocationID) {
			outputRunOriginErr = e.New(e.CodeVerifyFailed, "output %q was prepared or signed by another producing attempt; qualification reuse is not admitted", output.Name)
		}
		outputRunErr := errors.Join(rootErr, outputRunOriginErr)
		checks = append(checks, qualificationCheck("run-and-inputs", outputRunErr), qualificationCheck("signed-evidence", outputEvidenceErr))
		signatureErr := verifyQualificationSubject(ctx, verifier, output.Reference, predicates[output.Reference], policy, origin)
		checks = append(checks, qualificationCheck("build-signature-and-provenance", signatureErr))
		pinErr := checkAcquiredQualificationSubjects(&plan, output)
		checks = append(checks, qualificationCheck("payload-pins", pinErr))
		embeddedStatus, embeddedErr := checkQualificationEmbedded(ctx, &plan, output.Name)
		embeddedCheck := qualificationCheck("embedded-declarations", embeddedErr)
		if embeddedErr == nil {
			embeddedCheck.Status = embeddedStatus
		}
		checks = append(checks, embeddedCheck)
		sort.Slice(checks, func(i, j int) bool { return checks[i].Name < checks[j].Name })
		verdict := ArtifactCheckOutput{Name: output.Name, Kind: output.Kind, Reference: output.Reference,
			Subject: output.Digest, BuildDigest: output.BuildDigest, BuildSubjects: output.BuildSubjects,
			Checks: checks, Overall: "pass", ImageIndex: output.ImageIndex.Clone()}
		if output.OS != "" && output.Arch != "" {
			verdict.Platform = output.OS + "/" + output.Arch
		}
		if output.Kind == OutputDiskImage {
			verdict.DiskEvidence = "producer-only; content inspection not performed"
		}
		for _, check := range checks {
			if check.Status == "fail" {
				verdict.Overall = "fail"
			}
		}
		failures = append(failures, signatureErr, pinErr, embeddedErr)
		failures = append(failures, memberEvidenceErrs...)
		if i == 0 {
			// Keep shared failures at their first original position so coded-error
			// precedence is unchanged, while every output retains its diagnostics.
			failures = append(failures, evidenceErr, rootErr)
		}
		failures = append(failures, outputRunOriginErr)
		report.Outputs = append(report.Outputs, verdict)
	}
	if len(publication.Outputs) == 0 {
		failures = append(failures, rootErr, evidenceErr)
	}
	if err := errors.Join(failures...); err != nil {
		return nil, report, err
	}
	if err := source.Require(); err != nil {
		// Retain completed artifact checks as facts while making the final
		// source-lifetime refusal explicit for every output awaiting a receipt.
		for i := range report.Outputs {
			output := &report.Outputs[i]
			output.Checks = append(output.Checks, qualificationCheck("source-lifetime", err))
			sort.Slice(output.Checks, func(i, j int) bool { return output.Checks[i].Name < output.Checks[j].Name })
			output.Overall = "fail"
		}
		return nil, report, err
	}
	members, err := qualificationReleaseMembers(report.Outputs, predicates)
	if err != nil {
		return nil, report, err
	}
	raw, err = canonical.Marshal(report)
	if err != nil {
		return nil, report, err
	}
	if cause := ctx.Err(); cause != nil {
		err := e.Wrap(e.CodeTimeout, cause, "artifact qualification canceled before receipt issuance")
		// Completed checks remain facts, but a canceled operation cannot issue
		// authority. The failure defer releases the retained publication bytes.
		for i := range report.Outputs {
			output := &report.Outputs[i]
			output.Checks = append(output.Checks, qualificationCheck("operation-lifetime", err))
			sort.Slice(output.Checks, func(i, j int) bool { return output.Checks[i].Name < output.Checks[j].Name })
			output.Overall = "fail"
		}
		return nil, report, err
	}
	transferred = true
	return &ArtifactCheckReceipt{origin: source, raw: string(raw), lifetime: &artifactCheckLifetime{verified: verified, plan: &plan, members: members}}, report, nil
}

func qualificationPredicates(values []QualificationPredicate, plan *factory.Plan, origin qualificationorigin.Description) (map[string][]byte, error) {
	if len(values) == 0 || len(values) > len(plan.LifecycleOutputs)+1 {
		return nil, e.New(e.CodeReleaseIncomplete, "qualification requires exactly one predicate per selected subject")
	}
	result := make(map[string][]byte, len(values))
	for _, value := range values {
		if err := delivery.ValidatePublicationReference(value.Reference); err != nil {
			return nil, err
		}
		if _, exists := result[value.Reference]; exists {
			return nil, e.New(e.CodeReleaseIncomplete, "qualification repeats production provenance for %s", value.Reference)
		}
		if len(value.Predicate) > 4<<20 {
			return nil, e.New(e.CodeProvenanceMissing, "qualification production predicate exceeds its byte bound")
		}
		frozen := append([]byte(nil), value.Predicate...)
		if err := factory.ValidateProductionPredicate(frozen, plan, origin.BuilderID, origin.InvocationID); err != nil {
			return nil, err
		}
		result[value.Reference] = frozen
	}
	return result, nil
}

func verifyQualificationSubject(ctx context.Context, verifier sign.OCISigner, reference string, predicate []byte, policy VerificationPolicy, origin qualificationorigin.Description) error {
	subject := sign.Subject{Ref: reference, Digest: referenceDigest(reference)}
	record, err := verifier.Verify(ctx, subject, sign.VerifyPolicy{IdentityRegexp: policy.IdentityRegexp,
		Issuer: policy.Issuer, RequireAttestation: true, ExpectedPredicate: predicate})
	if err != nil {
		return err
	}
	if err := record.Validate(); err != nil {
		return err
	}
	if record.DryRun || !record.SignatureVerified || !record.AttestationVerified || record.Ref != subject.Ref ||
		record.Digest != subject.Digest || record.ObservedIdentity != origin.BuilderID || record.ObservedIssuer != policy.Issuer ||
		record.Identity != policy.IdentityRegexp || record.Issuer != policy.Issuer || record.PredicateType != sign.ProvenancePredicateType {
		return e.New(e.CodeSignatureInvalid, "build signatures and provenance do not authenticate the exact qualification subject")
	}
	return nil
}

func qualificationCheck(name string, err error) ArtifactCheckResult {
	result := ArtifactCheckResult{Name: name, Status: "pass"}
	if err != nil {
		result.Status, result.Code, result.Message = "fail", string(e.CodeInternal), err.Error()
		var coded *e.Error
		if errors.As(err, &coded) {
			result.Code = string(coded.Code)
		}
	}
	return result
}

func checkAcquiredQualificationSubjects(plan *factory.Plan, output delivery.PublicationOutput) error {
	// BuildSubjects identify producer/acquisition payloads; output.Digest is the
	// separate delivery manifest. An acquired file pin must never be compared
	// with that wrapper digest. Native image children retain their own subjects.
	subjects := make(map[string]string, len(output.BuildSubjects))
	for _, subject := range output.BuildSubjects {
		subjects[subject.Component] = subject.Digest
	}
	found := false
	for _, build := range plan.Builds {
		belongs := build.Name == output.Name
		if output.ImageIndex != nil {
			for _, child := range output.ImageIndex.Children {
				if build.Name == output.Name+"/"+child.Platform && child.BuildDigest == build.BuildDigest && child.Platform == build.Platform {
					belongs = true
				}
			}
		}
		if !belongs {
			continue
		}
		found = true
		for _, node := range build.Graph.Nodes {
			if node.Kind != ir.KindPull {
				continue
			}
			observed := subjects[node.Component]
			if output.ImageIndex != nil {
				for _, child := range output.ImageIndex.Children {
					if child.Platform == build.Platform && child.Component == node.Component {
						observed = child.BuildSubject
					}
				}
			}
			if observed == "" || observed != node.Digest {
				return e.New(e.CodeVerifyFailed, "output %q acquisition %q differs from its exact payload pin", output.Name, node.Component)
			}
		}
	}
	if !found {
		return e.New(e.CodeReleaseIncomplete, "output %q has no producing build", output.Name)
	}
	return nil
}

func checkQualificationEmbedded(ctx context.Context, plan *factory.Plan, outputName string) (string, error) {
	var selected []factory.BuildPlan
	for _, build := range plan.Builds {
		if build.Name == outputName {
			selected = append(selected, build)
		}
	}
	if len(selected) == 0 {
		for _, output := range plan.LifecycleOutputs {
			if output.Name != outputName || output.ImageIndex == nil {
				continue
			}
			for _, build := range plan.Builds {
				for _, child := range output.ImageIndex.Children {
					if child.BuildTarget == build.Name && child.BuildDigest == build.BuildDigest && child.Platform == build.Platform {
						selected = append(selected, build)
					}
				}
			}
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Name < selected[j].Name })
	status := "not-applicable"
	for _, build := range selected {
		receipt, err := catalog.CheckEmbeddedComponents(ctx, build.CatalogSnapshot, build.Components)
		if err != nil {
			return "fail", err
		}
		description, err := receipt.Description()
		if err != nil {
			return "fail", err
		}
		if description.CatalogDigest != build.CatalogDigest {
			return "fail", e.New(e.CodeVerifyFailed, "embedded check differs from producing catalog")
		}
		for _, component := range description.Components {
			if component.Status == "pass" {
				status = "pass"
			}
		}
	}
	return status, nil
}
