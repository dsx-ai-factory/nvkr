// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/provenance"
)

// SavedQualificationPredicate is original producer transport, not authenticated
// consumer evidence. The consumer must verify its exact bytes and invocation.
type SavedQualificationPredicate struct {
	Reference string
	Predicate []byte
}

// AdmitQualificationHandoff checks the producer contract needed for complete
// original qualification evidence. Legacy aggregate publication remains usable
// for builds and publication, but has neither per-output predicates nor invocation-bound
// release metadata and therefore cannot support a qualification handoff.
// Development publication has a separate disposable namespace and trust policy;
// it cannot supply the default-branch production handoff for qualification.
// Callers must still validate the Plan and authenticate the source and evidence.
func AdmitQualificationHandoff(plan *Plan) error {
	if plan == nil {
		return e.New(e.CodeUsage, "qualification handoff requires an original Plan")
	}
	if plan.Composition != nil {
		return e.New(e.CodeEngineCapability, "qualification of a composite Definition Run is deferred (ADR-26 §7)")
	}
	if plan.Environment.GitHubDevelopment {
		return e.New(e.CodeEngineCapability, "qualification handoff requires production publication; development publication has a separate trust policy")
	}
	if plan.Environment.Signing == nil || plan.Environment.Signing.Mode != SigningModeCosign {
		return e.New(e.CodeEngineCapability, "qualification handoff requires live signed publication")
	}
	if !PerOutputPublication(plan) {
		return e.New(e.CodeEngineCapability, "qualification requires publication.mode per-output-v1; legacy aggregate publication does not preserve the complete original qualification evidence")
	}
	return nil
}

// CollectQualificationEvidence collects the original root and output predicates
// from restored producer records. It never recreates, resigns, or relabels them.
// Signature-record agreement detects an incomplete handoff; it grants no
// authority and cannot substitute for the consumer's cryptographic verification.
func CollectQualificationEvidence(ctx context.Context, workspace string, plan *Plan) (string, []SavedQualificationPredicate, error) {
	if err := snapshotContext(ctx); err != nil {
		return "", nil, err
	}
	if workspace == "" || plan == nil {
		return "", nil, e.New(e.CodeUsage, "qualification handoff requires the original Plan and restored workspace")
	}
	if err := plan.Validate(); err != nil {
		return "", nil, err
	}
	if err := AdmitQualificationHandoff(plan); err != nil {
		return "", nil, err
	}
	_, artifacts := planIndex(plan)
	paths := make(map[string]string, len(artifacts))
	for id, artifact := range artifacts {
		const prefix = ".nvkr/factory/"
		if !strings.HasPrefix(artifact.Path, prefix) || filepath.ToSlash(filepath.Clean(artifact.Path)) != artifact.Path {
			return "", nil, e.New(e.CodePipelineDrift, "qualification artifact has an unsafe workspace path")
		}
		paths[id] = filepath.Join(workspace, strings.TrimPrefix(artifact.Path, prefix))
	}
	routes := []string{"release"}
	for _, output := range plan.LifecycleOutputs {
		routes = append(routes, outputPublicationRoute(output.Name))
	}
	var reference string
	saved := make(map[string]SavedQualificationPredicate, len(routes))
	seen := make(map[string]bool, len(routes))
	for _, route := range routes {
		for _, kind := range []string{"publication/", "signature/", "predicate/"} {
			if paths[kind+route] == "" {
				return "", nil, e.New(e.CodeProvenanceMissing, "qualification handoff omits %s%s", kind, route)
			}
		}
		subject, err := readPublicationSubject(ctx, paths["publication/"+route])
		if err != nil {
			return "", nil, err
		}
		target, ok := plan.Targets[route]
		if !ok || subject.Ref != target.Ref+"@"+subject.Digest || seen[subject.Ref] {
			return "", nil, e.New(e.CodeVerifyFailed, "qualification handoff has a duplicate or substituted publication")
		}
		seen[subject.Ref] = true
		predicate, err := readSmallFactoryFile(ctx, paths["predicate/"+route], "original qualification predicate")
		if err != nil {
			return "", nil, err
		}
		signed, err := readSigningRecord(ctx, paths["signature/"+route])
		if err != nil {
			return "", nil, err
		}
		if signed.DryRun || signed.Ref != subject.Ref || signed.Digest != subject.Digest ||
			signed.PredicateSHA256 != pipeline.FileDigest(predicate) || signed.Identity != plan.Environment.Signing.Identity ||
			signed.Issuer != plan.Environment.Signing.Issuer {
			return "", nil, e.New(e.CodeVerifyFailed, "qualification handoff differs from the original signed predicate")
		}
		if err := validateHostedProductionPredicate(predicate, plan, plan.Environment.Signing); err != nil {
			return "", nil, err
		}
		if route == "release" {
			reference = subject.Ref
		}
		saved[route] = SavedQualificationPredicate{Reference: subject.Ref, Predicate: predicate}
	}
	if paths["metadata/release"] == "" {
		return "", nil, e.New(e.CodeProvenanceMissing, "qualification handoff omits original release metadata")
	}
	predicates, err := qualificationPayloadPredicates(ctx, plan, paths["metadata/release"], saved)
	if err != nil {
		return "", nil, err
	}
	sort.Slice(predicates, func(i, j int) bool { return predicates[i].Reference < predicates[j].Reference })
	if err := snapshotContext(ctx); err != nil {
		return "", nil, err
	}
	return reference, predicates, nil
}

// The producer attests both an output's payload and its metadata root with the
// same original predicate. Qualification consumes the payload subject, so join
// its reference through the exact signed aggregate metadata and original route
// record. Reading bounded control/evidence blobs requires no payload access.
func qualificationPayloadPredicates(ctx context.Context, plan *Plan, metadataPath string, saved map[string]SavedQualificationPredicate) ([]SavedQualificationPredicate, error) {
	root := saved["release"]
	inspection, err := delivery.InspectMetadata(ctx, metadataPath)
	if err != nil {
		return nil, err
	}
	if root.Reference != plan.Targets["release"].Ref+"@"+inspection.RootDigest {
		return nil, e.New(e.CodeVerifyFailed, "qualification metadata differs from original signed root")
	}
	planDigest, err := plan.IdentityDigest()
	if err != nil {
		return nil, err
	}
	metadata := inspection.Root.Release
	if metadata == nil || metadata.PlanDigest != planDigest || metadata.DefinitionDigest != plan.DefinitionDigest || metadata.EnvironmentDigest != plan.EnvironmentDigest || !metadata.Composition.Equal(plan.Composition) {
		return nil, e.New(e.CodeVerifyFailed, "qualification metadata differs from the original Plan")
	}
	rootInvocation, err := qualificationPredicateInvocation(root.Predicate)
	if err != nil {
		return nil, err
	}
	if err := admitReferenceInvocation(metadata.InvocationID, rootInvocation); err != nil {
		return nil, err
	}
	outputs, err := delivery.MetadataOutputs(inspection.Root)
	if err != nil {
		return nil, err
	}
	if err := ValidateLifecycleOutputs(plan.LifecycleOutputs, outputs); err != nil {
		return nil, err
	}
	references, err := delivery.OutputReferences(root.Reference, inspection)
	if err != nil {
		return nil, err
	}
	predicates := []SavedQualificationPredicate{root}
	seen := map[string]bool{root.Reference: true}
	for _, object := range inspection.Root.Objects {
		if object.Output == nil {
			continue
		}
		original, found := saved[outputPublicationRoute(object.Name)]
		origin := object.Output.Publication
		if !found || origin == nil || original.Reference != origin.Ref {
			return nil, e.New(e.CodeVerifyFailed, "qualification output %q differs from its original publication route", object.Name)
		}
		invocation, err := qualificationPredicateInvocation(original.Predicate)
		if err != nil {
			return nil, err
		}
		if origin.SignedInvocationID != invocation {
			return nil, e.New(e.CodeVerifyFailed, "qualification output %q differs from its original signing invocation", object.Name)
		}
		if err := admitReferenceInvocation(origin.PreparedInvocationID, invocation); err != nil {
			return nil, err
		}
		if err := admitReferenceInvocation(invocation, metadata.InvocationID); err != nil {
			return nil, err
		}
		reference := references[object.Name]
		if reference == "" || seen[reference] {
			return nil, e.New(e.CodeVerifyFailed, "qualification output %q has no distinct payload subject", object.Name)
		}
		seen[reference] = true
		predicates = append(predicates, SavedQualificationPredicate{Reference: reference, Predicate: original.Predicate})
	}
	return predicates, nil
}

func qualificationPredicateInvocation(raw []byte) (string, error) {
	var predicate provenance.Predicate
	if err := canonical.UnmarshalExact(bytes.TrimSuffix(raw, []byte{'\n'}), &predicate); err != nil {
		return "", e.Wrap(e.CodeProvenanceMissing, err, "decoding original qualification invocation")
	}
	return predicate.RunDetails.Metadata["invocationId"], nil
}
