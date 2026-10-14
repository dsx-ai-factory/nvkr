// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
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
		{name: "other workflow", builder: "https://github.com/example/nvkr/.github/workflows/other.yml@refs/heads/main", code: e.CodeVerifyFailed},
		{name: "other run", invocation: "https://github.com/example/nvkr/actions/runs/43/attempts/2", code: e.CodeVerifyFailed},
		{name: "other attempt", invocation: "https://github.com/example/nvkr/actions/runs/42/attempts/1", code: e.CodeVerifyFailed},
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
