// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/provenance"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// This receipt can only be issued after exact remote signature/provenance
// verification and full control-document/evidence validation for this GitHub run.
type authenticatedOutputMetadata struct {
	inspection delivery.Inspection
	source     string
	origin     delivery.PublicationOrigin
}

// authenticateReferenceMetadata uses persisted records only as expectations.
// The registry's signature and production predicate authenticate the exact root;
// caller-written JSON or a successful provider job cannot issue this receipt.
func authenticateReferenceMetadata(ctx context.Context, plan *Plan, route Route, artifacts map[string]Artifact, newSigner signerFor) (authenticatedOutputMetadata, error) {
	fail := func(err error) (authenticatedOutputMetadata, error) { return authenticatedOutputMetadata{}, err }
	subject, err := readPublicationSubject(ctx, artifacts["publication/"+route.Name].Path)
	if err != nil {
		return fail(err)
	}
	target, err := runtimeTarget(plan, route.Target)
	if err != nil {
		return fail(err)
	}
	if subject.Ref != target.Ref+"@"+subject.Digest {
		return fail(e.New(e.CodeVerifyFailed, "publication differs from planned repository"))
	}
	signed, err := readSigningRecord(ctx, artifacts["signature/"+route.Name].Path)
	if err != nil {
		return fail(err)
	}
	predicate, err := readSmallFactoryFile(ctx, artifacts["predicate/"+route.Name].Path, "exact production predicate")
	if err != nil {
		return fail(err)
	}
	if signed.Ref != subject.Ref || signed.Digest != subject.Digest || signed.DryRun || signed.PredicateSHA256 != pipeline.FileDigest(predicate) ||
		signed.Identity != plan.Environment.Signing.Identity || signed.Issuer != plan.Environment.Signing.Issuer {
		return fail(e.New(e.CodeVerifyFailed, "publication record differs from exact production expectation"))
	}
	if err := validateHostedProductionPredicate(predicate, plan, plan.Environment.Signing); err != nil {
		return fail(err)
	}
	source := artifacts["metadata/"+route.Name].Path
	inspection, err := delivery.InspectMetadata(ctx, source)
	if err != nil {
		return fail(err)
	}
	if inspection.RootDigest != subject.Digest {
		return fail(e.New(e.CodeVerifyFailed, "metadata differs from signed publication"))
	}
	digest, err := plan.IdentityDigest()
	if err != nil {
		return fail(err)
	}
	run, err := publicationRunContext(plan, digest, time.Now().UTC(), time.Now().UTC())
	if err != nil {
		return fail(err)
	}
	var claimed provenance.Predicate
	if err := canonical.UnmarshalExact(bytes.TrimSuffix(predicate, []byte{'\n'}), &claimed); err != nil || claimed.RunDetails.Builder.ID != run.BuilderID() {
		return fail(e.New(e.CodeVerifyFailed, "production predicate belongs to another workflow Run"))
	}
	invocation := claimed.RunDetails.Metadata["invocationId"]
	if err := admitPlanReferenceInvocation(plan, invocation, run.InvocationID()); err != nil {
		return fail(err)
	}
	metadata := inspection.Root.Release
	if metadata == nil || metadata.PlanDigest != digest ||
		metadata.DefinitionDigest != plan.DefinitionDigest || metadata.EnvironmentDigest != plan.EnvironmentDigest {
		return fail(e.New(e.CodeVerifyFailed, "publication metadata belongs to another Plan or Run"))
	}
	if err := admitPlanReferenceInvocation(plan, metadata.InvocationID, invocation); err != nil {
		return fail(err)
	}
	for _, object := range inspection.Root.Objects {
		if object.Output == nil || object.Output.Publication == nil {
			continue
		}
		origin := object.Output.Publication
		target, err := runtimeTarget(plan, outputPublicationRoute(object.Name))
		if err != nil || !strings.HasPrefix(origin.Ref, target.Ref+"@") {
			return fail(e.New(e.CodeVerifyFailed, "output origin differs from planned publication repository"))
		}
		if err := admitPlanReferenceInvocation(plan, origin.PreparedInvocationID, origin.SignedInvocationID); err != nil {
			return fail(err)
		}
		if err := admitPlanReferenceInvocation(plan, origin.SignedInvocationID, metadata.InvocationID); err != nil {
			return fail(err)
		}
	}
	node := planNode(plan, NodeVerifyPublication+"/"+route.Name)
	signer, err := newSigner(ctx, plan, node, artifacts)
	if err != nil {
		return fail(err)
	}
	if signer == nil {
		return fail(e.New(e.CodeEngineCapability, "metadata authentication requires a signer"))
	}
	record, err := signer.Verify(ctx, subject, sign.VerifyPolicy{IdentityRegexp: plan.Environment.Signing.Identity, Issuer: plan.Environment.Signing.Issuer, RequireAttestation: true, ExpectedPredicate: predicate})
	if err != nil {
		return fail(err)
	}
	if err := record.Validate(); err != nil {
		return fail(err)
	}
	if record.DryRun || !record.SignatureVerified || !record.AttestationVerified || record.Ref != subject.Ref || record.Digest != subject.Digest ||
		record.Identity != plan.Environment.Signing.Identity || record.Issuer != plan.Environment.Signing.Issuer ||
		record.ObservedIssuer != plan.Environment.Signing.Issuer || !regexp.MustCompile(plan.Environment.Signing.Identity).MatchString(record.ObservedIdentity) {
		return fail(e.New(e.CodeSignatureInvalid, "metadata publication lacks authenticated signature and exact provenance"))
	}
	if err := verifyMetadataSBOMs(ctx, signer, source, inspection); err != nil {
		return fail(err)
	}
	return authenticatedOutputMetadata{inspection: inspection, source: source, origin: delivery.PublicationOrigin{
		Ref: subject.Ref, PreparedInvocationID: metadata.InvocationID, SignedInvocationID: invocation,
	}}, nil
}

func verifyMetadataSBOMs(ctx context.Context, signer sign.OCISigner, source string, inspection delivery.Inspection) error {
	if err := delivery.RequireAttestedEvidence(inspection); err != nil {
		return err
	}
	attester, ok := signer.(sign.SBOMAttester)
	if !ok {
		return e.New(e.CodeEngineCapability, "metadata verification requires signed SBOM support")
	}
	return delivery.VisitMetadataEvidence(ctx, source, inspection.RootDigest, attester.VerifySBOM)
}

func executeReferenceRelease(ctx context.Context, plan *Plan, node Node, artifacts map[string]Artifact, newSigner signerFor) error {
	if !PerOutputPublication(plan) || node.Kind != NodeAssembleRelease {
		return e.New(e.CodeEngineCapability, "reference release not admitted")
	}
	var receipts []authenticatedOutputMetadata
	for _, output := range plan.LifecycleOutputs {
		route := Route{Name: outputPublicationRoute(output.Name), Target: outputPublicationRoute(output.Name)}
		receipt, err := authenticateReferenceMetadata(ctx, plan, route, artifacts, newSigner)
		if err != nil {
			return err
		}
		observed, err := delivery.MetadataOutputs(receipt.inspection.Root)
		if err != nil {
			return err
		}
		if err := ValidateLifecycleOutputs([]LifecycleOutputPlan{output}, observed); err != nil {
			return err
		}
		receipts = append(receipts, receipt)
	}
	return assembleReferenceRelease(ctx, plan, artifacts[node.Outputs[0]].Path, receipts)
}

func assembleReferenceRelease(ctx context.Context, plan *Plan, out string, receipts []authenticatedOutputMetadata) error {
	digest, err := plan.IdentityDigest()
	if err != nil {
		return err
	}
	run, err := publicationRunContext(plan, digest, time.Now().UTC(), time.Now().UTC())
	if err != nil {
		return err
	}
	root := delivery.PublicationRoot{SchemaVersion: delivery.PublicationRootSchema, Qualification: delivery.QualificationDiagnostic}
	sources := map[string]string{}
	objects := map[string]delivery.PublishedObject{}
	var outputs []delivery.Output
	for _, receipt := range receipts {
		if receipt.source == "" || receipt.inspection.Root.Release == nil {
			return e.New(e.CodeVerifyFailed, "reference assembly requires authenticated metadata")
		}
		if root.Release == nil {
			value := *receipt.inspection.Root.Release
			root.Release = &value
		}
		if root.Release.PlanDigest != receipt.inspection.Root.Release.PlanDigest ||
			root.Release.DefinitionDigest != receipt.inspection.Root.Release.DefinitionDigest || root.Release.EnvironmentDigest != receipt.inspection.Root.Release.EnvironmentDigest {
			return e.New(e.CodeVerifyFailed, "output metadata belongs to different release plans")
		}
		if err := admitPlanReferenceInvocation(plan, receipt.origin.SignedInvocationID, run.InvocationID()); err != nil {
			return err
		}
		for _, object := range receipt.inspection.Root.Objects {
			if object.Output != nil {
				binding, origin := *object.Output, receipt.origin
				binding.Publication = &origin
				object.Output = &binding
			}
			if previous, exists := objects[object.Name]; exists {
				if previous.Output != nil || object.Output != nil || previous.Evidence == nil || object.Evidence == nil {
					return e.New(e.CodeVerifyFailed, "repeated release output")
				}
				a, b := *previous.Evidence, *object.Evidence
				a.Outputs = nil
				b.Outputs = nil
				if previous.SemanticDigest != object.SemanticDigest || !reflect.DeepEqual(a, b) {
					return e.New(e.CodeVerifyFailed, "conflicting shared evidence")
				}
				// Iterate outputs canonically: keep the first owner's already-published
				// bundle (whose manifest subject is that output), merge only membership.
				binding := *previous.Evidence
				binding.Outputs = uniqueSorted(append(append([]string(nil), binding.Outputs...), object.Evidence.Outputs...))
				previous.Evidence = &binding
				objects[object.Name] = previous
			} else {
				objects[object.Name] = object
				sources[object.Name] = receipt.source
			}
		}
		observed, err := delivery.MetadataOutputs(receipt.inspection.Root)
		if err != nil {
			return err
		}
		outputs = append(outputs, observed...)
	}
	if err := ValidateLifecycleOutputs(plan.LifecycleOutputs, outputs); err != nil {
		return err
	}
	subject, err := delivery.ContentDigest(outputs)
	if err != nil {
		return err
	}
	if root.Release == nil {
		return e.New(e.CodeReleaseIncomplete, "release has no output receipts")
	}
	root.Release.ContentSubject = subject
	root.Release.InvocationID = run.InvocationID()
	for _, name := range sortedKeys(objects) {
		root.Objects = append(root.Objects, objects[name])
	}
	_, err = delivery.BuildMetadataRoot(ctx, out, root, sources)
	return err
}

// AuthenticateHostedReferenceRelease admits the full Plan, reauthenticates the
// release's signatures and evidence, and returns only exact frozen output claims.
// Per-output payload validation is attested by this same trusted workflow Run.
func AuthenticateHostedReferenceRelease(ctx context.Context, workspace string, plan *Plan) ([]delivery.Output, error) {
	if ctx == nil || !PerOutputPublication(plan) {
		return nil, e.New(e.CodeEngineCapability, "hosted metadata release is not admitted")
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	_, artifacts := planIndex(plan)
	for id, a := range artifacts {
		prefix := ".nvkr/factory/"
		if !strings.HasPrefix(a.Path, prefix) {
			return nil, e.New(e.CodePipelineDrift, "unsafe metadata artifact path")
		}
		a.Path = filepath.Join(workspace, strings.TrimPrefix(a.Path, prefix))
		artifacts[id] = a
	}
	receipt, err := authenticateReferenceMetadata(ctx, plan, Route{Name: "release", Target: "release"}, artifacts, productionOCISigner)
	if err != nil {
		return nil, err
	}
	outputs, err := delivery.MetadataOutputs(receipt.inspection.Root)
	if err != nil {
		return nil, err
	}
	if err := ValidateLifecycleOutputs(plan.LifecycleOutputs, outputs); err != nil {
		return nil, err
	}
	// Reverify independent output signatures without fetching any output layers.
	signer, err := productionOCISigner(ctx, plan, planNode(plan, NodeVerifyPublication+"/release"), artifacts)
	if err != nil {
		return nil, err
	}
	subject, err := readPublicationSubject(ctx, artifacts["publication/release"].Path)
	if err != nil {
		return nil, err
	}
	if err := verifyPublicationOutputs(ctx, signer, subject.Ref, receipt.inspection, plan.Environment.Signing, io.Discard); err != nil {
		return nil, err
	}
	return outputs, nil
}
