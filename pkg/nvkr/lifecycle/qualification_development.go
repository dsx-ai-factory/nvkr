// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/development"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"golang.org/x/sync/errgroup"
)

// DevelopmentQualificationSourceRoot contains source data checked out by the
// generated qualifier. No source code is executed. Exact signed Plan/input
// verification is still mandatory before this data can issue a receipt.
const DevelopmentQualificationSourceRoot = ".nvkr/development-source"

// CheckDevelopmentQualificationSource recovers original controls from an exact
// authenticated successful development Run, including runs predating a handoff.
func CheckDevelopmentQualificationSource(ctx context.Context, provider *qualificationorigin.GitHub, source *qualificationorigin.SourceRun, policy QualificationPolicy, options QualificationRuntimeOptions) (*ArtifactCheckReceipt, *ArtifactCheckDescription, error) {
	if err := admitQualificationRun(ctx); err != nil {
		return nil, nil, err
	}
	if err := policy.Validate(); err != nil {
		return nil, nil, err
	}
	origin, err := source.Description()
	if err != nil {
		return nil, nil, err
	}
	if !policy.Source.Development || origin.Branch == "" || origin.Repository != policy.Source.Repository || origin.Workflow != policy.Source.Workflow || origin.DefaultBranch != policy.Source.DefaultBranch {
		return nil, nil, e.New(e.CodeUntrustedSigner, "development source differs from independent policy")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	plan, err := factory.LoadPlanContext(ctx, filepath.Join(DevelopmentQualificationSourceRoot, ".github/nvkr-dev/plan.json"))
	if err != nil {
		return nil, nil, err
	}
	if err := factory.AdmitDevelopmentQualification(plan); err != nil {
		return nil, nil, err
	}
	replay, _, err := factory.CaptureCompileFiles(ctx, DevelopmentQualificationSourceRoot, plan.DefinitionPath, plan.EnvironmentPath, plan.GeneratorVersion)
	if err != nil {
		return nil, nil, err
	}
	want, err := plan.Digest()
	if err != nil {
		return nil, nil, err
	}
	got, err := replay.Digest()
	if err != nil {
		return nil, nil, err
	}
	if got != want {
		return nil, nil, e.New(e.CodePipelineDrift, "development qualification source Plan differs from captured inputs")
	}
	controls, err := factory.DevelopmentQualificationControls(plan)
	if err != nil {
		return nil, nil, err
	}
	root, err := os.MkdirTemp("", "nvkr-development-qualification-")
	if err != nil {
		return nil, nil, e.Wrap(e.CodeFetchFailed, err, "creating development control workspace")
	}
	defer func() { _ = os.RemoveAll(root) }()
	group, downloadContext := errgroup.WithContext(ctx)
	group.SetLimit(4)
	paths := make(map[string]string, len(controls))
	for _, control := range controls {
		paths[control.ID] = filepath.Join(root, control.Root)
		group.Go(func() error {
			return provider.DownloadDevelopmentMetadata(downloadContext, source, control.Name, control.Member, control.Root, root)
		})
	}
	if err := group.Wait(); err != nil {
		return nil, nil, err
	}
	candidate, err := developmentQualificationCandidate(ctx, plan, paths, origin)
	if err != nil {
		return nil, nil, err
	}
	consumer, err := newQualificationConsumer(candidate.Reference, policy, options)
	if err != nil {
		return nil, nil, err
	}
	return consumer.CheckQualification(ctx, QualificationRequest{Reference: candidate.Reference, Definition: plan.Definition, Environment: plan.Environment, Plan: plan, Origin: source, Predicates: candidate.Predicates})
}

func developmentQualificationCandidate(ctx context.Context, plan *factory.Plan, paths map[string]string, origin qualificationorigin.Description) (QualificationCandidate, error) {
	read := func(id string) ([]byte, error) {
		if paths[id] == "" {
			return nil, e.New(e.CodeProvenanceMissing, "development recovery omits %s", id)
		}
		held, err := heldfile.OpenContext(ctx, paths[id], heldfile.Options{Label: "original development control", MaxBytes: 8 << 20})
		if err != nil {
			return nil, err
		}
		defer func() { _ = held.Close() }()
		return held.ReadAllContext(ctx, 8<<20)
	}
	raw, err := read("publication/release")
	if err != nil {
		return QualificationCandidate{}, err
	}
	reference := strings.TrimSuffix(string(raw), "\n")
	if err := admitDevelopmentQualificationReference(reference, origin); err != nil {
		return QualificationCandidate{}, err
	}
	inspection, err := delivery.InspectMetadata(ctx, paths["metadata/release"])
	if err != nil {
		return QualificationCandidate{}, err
	}
	digest, err := plan.IdentityDigest()
	if err != nil {
		return QualificationCandidate{}, err
	}
	metadata := inspection.Root.Release
	if metadata == nil || metadata.PlanDigest != digest || metadata.DefinitionDigest != plan.DefinitionDigest || metadata.EnvironmentDigest != plan.EnvironmentDigest || metadata.InvocationID != origin.InvocationID {
		return QualificationCandidate{}, e.New(e.CodeVerifyFailed, "development metadata differs from exact source Plan and invocation")
	}
	outputs, err := delivery.MetadataOutputs(inspection.Root)
	if err != nil {
		return QualificationCandidate{}, err
	}
	if err := factory.ValidateLifecycleOutputs(plan.LifecycleOutputs, outputs); err != nil {
		return QualificationCandidate{}, err
	}
	references, err := delivery.OutputReferences(reference, inspection)
	if err != nil {
		return QualificationCandidate{}, err
	}
	predicate, err := read("predicate/release")
	if err != nil {
		return QualificationCandidate{}, err
	}
	candidate := QualificationCandidate{SchemaVersion: QualificationCandidateSchema, Reference: reference, Predicates: []QualificationPredicate{{Reference: reference, Predicate: predicate}}}
	seen := map[string]bool{reference: true}
	for _, output := range plan.LifecycleOutputs {
		ref := references[output.Name]
		if seen[ref] {
			return QualificationCandidate{}, e.New(e.CodeVerifyFailed, "development output references are duplicated")
		}
		if err := admitDevelopmentQualificationReference(ref, origin); err != nil {
			return QualificationCandidate{}, err
		}
		seen[ref] = true
		predicate, err := read("predicate/output-" + output.Name)
		if err != nil {
			return QualificationCandidate{}, err
		}
		candidate.Predicates = append(candidate.Predicates, QualificationPredicate{Reference: ref, Predicate: predicate})
	}
	sort.Slice(candidate.Predicates, func(i, j int) bool { return candidate.Predicates[i].Reference < candidate.Predicates[j].Reference })
	return candidate, nil
}

func admitDevelopmentQualificationReference(reference string, origin qualificationorigin.Description) error {
	if err := delivery.ValidatePublicationReference(reference); err != nil {
		return err
	}
	namespace, err := development.ParseNamespace("ghcr.io/" + strings.ToLower(origin.Repository) + "/development")
	if err != nil {
		return err
	}
	repository, _, ok := strings.Cut(reference, "@")
	if !ok {
		return e.New(e.CodeUnverifiedRef, "development qualification requires an immutable reference")
	}
	parsed, err := namespace.ParseReference(repository)
	if err != nil {
		return err
	}
	if parsed.RunID() != origin.RunID || parsed.Attempt() != origin.Attempt {
		return e.New(e.CodeVerifyFailed, "development qualification reference belongs to a different source attempt")
	}
	return nil
}
