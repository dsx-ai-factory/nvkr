// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/development"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/provenance"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// runtimeTarget projects transport locations into the current disposable attempt.
// The complete Plan and logical Build identity remain unchanged. All subjects and
// signed metadata bind the resulting immutable runtime reference instead.
func runtimeTarget(plan *Plan, name string) (Target, error) {
	target, ok := plan.Targets[name]
	if !ok {
		return Target{}, e.New(e.CodePipelineDrift, "publication target %q is missing", name)
	}
	if !plan.Environment.GitHubDevelopment {
		return target, nil
	}
	if err := admitDevelopmentEnvironment(); err != nil {
		return Target{}, err
	}
	digest, err := plan.IdentityDigest()
	if err != nil {
		return Target{}, err
	}
	run, err := factoryRunContext(digest, time.Now().UTC(), time.Now().UTC())
	if err != nil {
		return Target{}, err
	}
	namespace, runID, attempt, err := developmentRun(plan, run)
	if err != nil {
		return Target{}, err
	}
	if !strings.HasPrefix(target.Ref, namespace.Prefix()+"/") {
		return Target{}, e.New(e.CodePipelineDrift, "development target is outside its authored namespace")
	}
	ref, err := namespace.Reference(runID, attempt, strings.TrimPrefix(target.Ref, namespace.Prefix()+"/"))
	if err != nil {
		return Target{}, err
	}
	target.Ref = ref
	return target, nil
}

func admitDevelopmentHostedContext(plan *Plan) error {
	if !plan.Environment.GitHubDevelopment || plan.Environment.Publication == nil {
		return nil
	}
	_, err := runtimeTarget(plan, "release")
	return err
}

func admitDevelopmentEnvironment() error {
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("GITHUB_EVENT_NAME") != "workflow_dispatch" || !strings.HasPrefix(os.Getenv("GITHUB_REF"), "refs/heads/") ||
		!strings.HasSuffix(os.Getenv("GITHUB_WORKFLOW_REF"), "@"+os.Getenv("GITHUB_REF")) {
		return e.New(e.CodeEngineCapability, "development publication requires manual dispatch of its exact branch workflow")
	}
	return nil
}

// developmentRun joins the existing provider Run description to the admitted
// repository and fixed workflow identity before any development publication work.
func developmentRun(plan *Plan, run provenance.RunContext) (development.Namespace, string, string, error) {
	if plan.Environment.Publication == nil || run == nil {
		return development.Namespace{}, "", "", e.New(e.CodeEngineCapability, "development publication requires a hosted run context")
	}
	namespace, err := development.ParseNamespace(plan.Environment.Publication.RepositoryPrefix)
	if err != nil {
		return development.Namespace{}, "", "", err
	}
	if !regexp.MustCompile(namespace.Identity()).MatchString(run.BuilderID()) {
		return development.Namespace{}, "", "", e.New(e.CodeEngineCapability, "development publication requires its exact repository branch workflow identity")
	}
	prefix := "https://github.com/" + namespace.Repository() + "/actions/runs/"
	if !strings.HasPrefix(run.InvocationID(), prefix) {
		return development.Namespace{}, "", "", e.New(e.CodeEngineCapability, "development publication run belongs to another repository or host")
	}
	parts := strings.Split(strings.TrimPrefix(run.InvocationID(), prefix), "/")
	if len(parts) != 3 || parts[1] != "attempts" {
		return development.Namespace{}, "", "", e.New(e.CodeEngineCapability, "development publication requires an exact run attempt")
	}
	if _, err := namespace.Reference(parts[0], parts[2], "releases"); err != nil {
		return development.Namespace{}, "", "", err
	}
	return namespace, parts[0], parts[2], nil
}

func publicationRunContext(plan *Plan, digest string, startedOn, finishedOn time.Time) (provenance.RunContext, error) {
	if plan.Environment.GitHubDevelopment {
		if err := admitDevelopmentEnvironment(); err != nil {
			return nil, err
		}
	}
	run, err := factoryRunContext(digest, startedOn, finishedOn)
	if err != nil {
		return nil, err
	}
	if plan.Environment.GitHubDevelopment {
		if _, _, _, err := developmentRun(plan, run); err != nil {
			return nil, err
		}
	}
	return run, nil
}

func admitDevelopmentSubject(plan *Plan, targetName string, subject sign.Subject) error {
	if !plan.Environment.GitHubDevelopment {
		return nil
	}
	target, err := runtimeTarget(plan, targetName)
	if err != nil {
		return err
	}
	repository, digest, ok := strings.Cut(subject.Ref, "@")
	if !ok || digest != subject.Digest {
		return e.New(e.CodeVerifyFailed, "development publication subject differs from its digest")
	}
	namespace, err := development.ParseNamespace(plan.Environment.Publication.RepositoryPrefix)
	if err != nil {
		return err
	}
	current, err := namespace.ParseReference(target.Ref)
	if err != nil {
		return err
	}
	invocation := "https://github.com/" + namespace.Repository() + "/actions/runs/" + current.RunID() + "/attempts/" + current.Attempt()
	if err := validateQualificationOutputRepository(plan, plan.Targets[targetName], repository, invocation); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "development subject differs from its same-run publication scope")
	}
	return nil
}

// ValidatePublicationInvocation checks same-run, non-future attempt scope.
// It grants no authority without exact Plan, subject and signature verification.
func ValidatePublicationInvocation(candidate, current string) error {
	return admitReferenceInvocation(candidate, current)
}

func admitDevelopmentMetadata(plan *Plan, inspection delivery.Inspection) error {
	if !plan.Environment.GitHubDevelopment {
		return nil
	}
	digest, err := plan.IdentityDigest()
	if err != nil {
		return err
	}
	run, err := publicationRunContext(plan, digest, time.Now().UTC(), time.Now().UTC())
	if err != nil {
		return err
	}
	metadata := inspection.Root.Release
	if metadata == nil || metadata.PlanDigest != digest || metadata.EnvironmentDigest != plan.EnvironmentDigest || metadata.DefinitionDigest != plan.DefinitionDigest || !metadata.Composition.Equal(plan.Composition) {
		return e.New(e.CodeVerifyFailed, "development publication metadata differs from this exact Plan")
	}
	return admitReferenceInvocation(metadata.InvocationID, run.InvocationID())
}
