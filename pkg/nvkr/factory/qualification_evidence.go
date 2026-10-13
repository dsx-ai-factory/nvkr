// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// SavedQualificationPredicate is original producer transport, not authenticated
// consumer evidence. The consumer must verify its exact bytes and invocation.
type SavedQualificationPredicate struct {
	Reference string
	Predicate []byte
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
	if plan.Environment.Signing == nil || plan.Environment.Signing.Mode != SigningModeCosign {
		return "", nil, e.New(e.CodeEngineCapability, "qualification handoff requires live signed publication")
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
	if PerOutputPublication(plan) {
		for _, output := range plan.LifecycleOutputs {
			routes = append(routes, outputPublicationRoute(output.Name))
		}
	}
	var reference string
	predicates := make([]SavedQualificationPredicate, 0, len(routes))
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
		predicates = append(predicates, SavedQualificationPredicate{Reference: subject.Ref, Predicate: predicate})
	}
	sort.Slice(predicates, func(i, j int) bool { return predicates[i].Reference < predicates[j].Reference })
	if err := snapshotContext(ctx); err != nil {
		return "", nil, err
	}
	return reference, predicates, nil
}
