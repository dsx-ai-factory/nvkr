// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/provenance"
)

func TestQualificationProductionPredicateBindsOriginAndInputs(t *testing.T) {
	plan := signedTestPlan(t, cosignSigningProfile(), ghcrTarget())
	run := hostedPredicateRun{builderID: "https://github.com/example/nvkr/.github/workflows/nvkr-factory.yml@refs/heads/main",
		invocationID: "https://github.com/example/nvkr/actions/runs/42/attempts/2",
		startedOn:    time.Unix(100, 0).UTC(), finishedOn: time.Unix(101, 0).UTC()}
	raw, err := ProductionPredicate(plan, run)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		builder    string
		invocation string
		mutate     func(*provenance.Predicate)
		code       e.Code
	}{
		{name: "exact"},
		{name: "same run later consuming attempt", invocation: "https://github.com/example/nvkr/actions/runs/42/attempts/3"},
		{name: "other workflow", builder: "https://github.com/example/nvkr/.github/workflows/other.yml@refs/heads/main", code: e.CodeVerifyFailed},
		{name: "other run", invocation: "https://github.com/example/nvkr/actions/runs/43/attempts/2", code: e.CodeVerifyFailed},
		{name: "future predicate attempt", invocation: "https://github.com/example/nvkr/actions/runs/42/attempts/1", code: e.CodeVerifyFailed},
		{name: "different server", invocation: "https://github.example.test/example/nvkr/actions/runs/42/attempts/2", code: e.CodeVerifyFailed},
		{name: "different repository", invocation: "https://github.com/other/nvkr/actions/runs/42/attempts/2", code: e.CodeVerifyFailed},
		{name: "noncanonical attempt", invocation: "https://github.com/example/nvkr/actions/runs/42/attempts/02", code: e.CodeVerifyFailed},
		{name: "wrong definition", mutate: func(p *provenance.Predicate) {
			p.BuildDefinition.ExternalParameters["definitionDigest"] = "sha256:" + strings.Repeat("f", 64)
		}, code: e.CodeVerifyFailed},
		{name: "wrong dependency", mutate: func(p *provenance.Predicate) {
			p.BuildDefinition.ResolvedDependencies[0].URI = "https://substituted.invalid"
		}, code: e.CodeUnpinnedInput},
	} {
		t.Run(test.name, func(t *testing.T) {
			var predicate provenance.Predicate
			if err := canonical.UnmarshalExact(raw, &predicate); err != nil {
				t.Fatal(err)
			}
			if test.mutate != nil {
				test.mutate(&predicate)
			}
			candidate, err := canonical.Marshal(predicate)
			if err != nil {
				t.Fatal(err)
			}
			builder, invocation := test.builder, test.invocation
			if builder == "" {
				builder = run.builderID
			}
			if invocation == "" {
				invocation = run.invocationID
			}
			err = ValidateProductionPredicate(candidate, plan, builder, invocation)
			if test.code != "" {
				assertFactoryErrorCode(t, err, test.code)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDevelopmentQualificationPredicateUsesSourceNotQualifierInvocation(t *testing.T) {
	plan := githubDevelopmentPublicationTestPlan(t)
	setDevelopmentRun(t)
	source := hostedPredicateRun{builderID: "https://github.com/example/nvkr/.github/workflows/nvkr-dev.yml@refs/heads/feature/pipeline", invocationID: "https://github.com/example/nvkr/actions/runs/12345/attempts/1", startedOn: time.Unix(100, 0).UTC(), finishedOn: time.Unix(101, 0).UTC()}
	raw, err := ProductionPredicate(plan, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_WORKFLOW_REF", "example/nvkr/.github/workflows/nvkr-dev-qualify.yml@refs/heads/feature/qualify")
	t.Setenv("GITHUB_REF", "refs/heads/feature/qualify")
	t.Setenv("GITHUB_RUN_ID", "99999")
	if err := ValidateProductionPredicate(raw, plan, source.builderID, source.invocationID); err != nil {
		t.Fatal(err)
	}
	// The publication owner still refuses to execute producer work in a qualifier.
	assertFactoryErrorCode(t, validateHostedProductionPredicate(raw, plan, plan.Environment.Signing), e.CodeEngineCapability)
	for _, invocation := range []string{"https://github.com/example/nvkr/actions/runs/99999/attempts/1", "https://github.com/example/nvkr/actions/runs/12345/attempts/2"} {
		assertFactoryErrorCode(t, ValidateProductionPredicate(raw, plan, source.builderID, invocation), e.CodeVerifyFailed)
	}
}

func TestDevelopmentQualificationPublicationOriginsRequireExactSourceAttempt(t *testing.T) {
	plan := githubDevelopmentPublicationTestPlan(t)
	setDevelopmentRun(t)
	const invocation = "https://github.com/example/nvkr/actions/runs/12345/attempts/2"
	const previous = "https://github.com/example/nvkr/actions/runs/12345/attempts/1"
	source := hostedPredicateRun{builderID: "https://github.com/example/nvkr/.github/workflows/nvkr-dev.yml@refs/heads/feature/pipeline",
		invocationID: invocation, startedOn: time.Unix(100, 0).UTC(), finishedOn: time.Unix(101, 0).UTC()}
	raw, err := ProductionPredicate(plan, source)
	if err != nil {
		t.Fatal(err)
	}
	// The qualifier's environment must never project the source's repositories.
	t.Setenv("GITHUB_WORKFLOW_REF", "example/nvkr/.github/workflows/nvkr-dev-qualify.yml@refs/heads/feature/qualify")
	t.Setenv("GITHUB_REF", "refs/heads/feature/qualify")
	t.Setenv("GITHUB_RUN_ID", "99999")
	for _, tc := range []struct {
		name      string
		prepared  string
		consuming string
		code      e.Code
	}{
		{name: "exact source"},
		{name: "earlier aggregate preparation", prepared: previous, code: e.CodeVerifyFailed},
		{name: "later finalizer", consuming: "https://github.com/example/nvkr/actions/runs/12345/attempts/3", code: e.CodeVerifyFailed},
	} {
		t.Run("root/"+tc.name, func(t *testing.T) {
			prepared, consuming := tc.prepared, tc.consuming
			if prepared == "" {
				prepared = invocation
			}
			if consuming == "" {
				consuming = invocation
			}
			err := ValidateQualificationRootInvocation(raw, plan, prepared, consuming)
			if tc.code != "" {
				assertFactoryErrorCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, tc := range []struct {
		name string
		edit func(*delivery.PublicationOrigin)
		code e.Code
	}{
		{name: "exact source"},
		{name: "earlier preparation", edit: func(o *delivery.PublicationOrigin) { o.PreparedInvocationID = previous }, code: e.CodeVerifyFailed},
		{name: "earlier signature", edit: func(o *delivery.PublicationOrigin) { o.SignedInvocationID = previous }, code: e.CodeVerifyFailed},
		{name: "earlier namespace", edit: func(o *delivery.PublicationOrigin) { o.Ref = strings.Replace(o.Ref, "/attempts/2/", "/attempts/1/", 1) }, code: e.CodeVerifyFailed},
		{name: "qualifier namespace", edit: func(o *delivery.PublicationOrigin) { o.Ref = strings.Replace(o.Ref, "/runs/12345/", "/runs/99999/", 1) }, code: e.CodeVerifyFailed},
		{name: "different output route", edit: func(o *delivery.PublicationOrigin) {
			o.Ref = strings.Replace(o.Ref, "/releases-", "/releases-other-", 1)
		}, code: e.CodeVerifyFailed},
	} {
		t.Run("output/"+tc.name, func(t *testing.T) {
			output := plan.LifecycleOutputs[0].Name
			origin := &delivery.PublicationOrigin{
				Ref:                  "ghcr.io/example/nvkr/development/runs/12345/attempts/2/releases-" + output + "@sha256:" + strings.Repeat("b", 64),
				PreparedInvocationID: invocation, SignedInvocationID: invocation,
			}
			if tc.edit != nil {
				tc.edit(origin)
			}
			err := ValidateQualificationOutputOrigin(raw, plan, delivery.PublicationOutput{Output: delivery.Output{Name: output}, Publication: origin}, invocation)
			if tc.code != "" {
				assertFactoryErrorCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
