// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/development"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// ValidateProductionPredicate compares a descriptive predicate with the exact
// admitted Plan and externally authenticated producing workflow run. Production
// and development retries retain earlier same-run predicates. Publication-origin checks must additionally bind the predicate
// to the exact selected metadata.
// Only a subsequent signature verification authenticates these predicate bytes.
func ValidateProductionPredicate(raw []byte, plan *Plan, builder, invocation string) error {
	if plan == nil || plan.Environment.Signing == nil || builder == "" || invocation == "" {
		return e.New(e.CodeProvenanceMissing, "qualification requires an exact Plan, builder and invocation")
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	signed, err := qualificationPredicateInvocation(raw)
	if err != nil {
		return err
	}
	if err := validateProductionPredicateOrigin(raw, plan, plan.Environment.Signing, builder, signed); err != nil {
		return err
	}
	return admitPlanReferenceInvocation(plan, signed, invocation)
}

// ValidateQualificationRootInvocation checks the original aggregate preparation
// and signing against the selected successful source attempt. These descriptive
// bindings grant no authority without exact Plan and signature verification.
// Both lanes preserve preparation <= signing <= consumption within one run.
func ValidateQualificationRootInvocation(raw []byte, plan *Plan, prepared, consuming string) error {
	if plan == nil {
		return e.New(e.CodeProvenanceMissing, "qualification root requires an exact Plan")
	}
	signed, err := qualificationPredicateInvocation(raw)
	if err != nil {
		return err
	}
	if err := admitPlanReferenceInvocation(plan, prepared, signed); err != nil {
		return err
	}
	return admitPlanReferenceInvocation(plan, signed, consuming)
}

// ValidateQualificationOutputOrigin binds an output predicate to the original
// publication recorded in aggregate metadata. The caller must authenticate that
// metadata and exact predicate bytes before issuing qualification authority.
func ValidateQualificationOutputOrigin(raw []byte, plan *Plan, output delivery.PublicationOutput, assembled string) error {
	origin := output.Publication
	if plan == nil || origin == nil {
		return e.New(e.CodeVerifyFailed, "qualification output %q has no original publication binding", output.Name)
	}
	target, found := plan.Targets[outputPublicationRoute(output.Name)]
	repository, _, pinned := strings.Cut(origin.Ref, "@")
	if !found || !pinned || delivery.ValidatePublicationReference(origin.Ref) != nil {
		return e.New(e.CodeVerifyFailed, "qualification output %q differs from its planned publication repository", output.Name)
	}
	if err := validateQualificationOutputRepository(plan, target, repository, assembled); err != nil {
		return err
	}
	signed, err := qualificationPredicateInvocation(raw)
	if err != nil {
		return err
	}
	if signed != origin.SignedInvocationID {
		return e.New(e.CodeVerifyFailed, "qualification output %q differs from its original signing invocation", output.Name)
	}
	if err := admitPlanReferenceInvocation(plan, origin.PreparedInvocationID, signed); err != nil {
		return err
	}
	return admitPlanReferenceInvocation(plan, signed, assembled)
}

// Development targets are projected from the authenticated source attempt, never
// from the qualifier's own environment. Production targets remain unprojected.
func validateQualificationOutputRepository(plan *Plan, target Target, repository, assembled string) error {
	if !plan.Environment.GitHubDevelopment {
		if repository != target.Ref {
			return e.New(e.CodeVerifyFailed, "qualification output differs from its planned publication repository")
		}
		return nil
	}
	if plan.Environment.Publication == nil {
		return e.New(e.CodeVerifyFailed, "development qualification requires a publication namespace")
	}
	namespace, err := development.ParseNamespace(plan.Environment.Publication.RepositoryPrefix)
	if err != nil {
		return err
	}
	reference, err := namespace.ParseReference(repository)
	if err != nil {
		return err
	}
	if target.Ref != namespace.Prefix()+"/"+reference.Leaf() {
		return e.New(e.CodeVerifyFailed, "development qualification output differs from its planned publication repository")
	}
	invocation := "https://github.com/" + namespace.Repository() + "/actions/runs/" + reference.RunID() + "/attempts/" + reference.Attempt()
	return admitPlanReferenceInvocation(plan, invocation, assembled)
}
