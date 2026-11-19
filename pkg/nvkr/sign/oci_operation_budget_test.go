// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/readbudget"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	v1 "github.com/google/go-containerregistry/pkg/v1"
)

func TestRegistryBundleUsesOperationReadBudget(t *testing.T) {
	fixture := newBundleRegistryFixture(t)
	raw := []byte(testOCIBundle(t, ociTestDigest, CosignSignaturePredicateType, ociObservedIdentity, ociTestIssuer))
	fixture.index(t, []v1.Descriptor{fixture.add(t, raw, CosignSignaturePredicateType, nil)})
	ctx, budget, err := readbudget.WithLimit(t.Context(), 64)
	if err != nil {
		t.Fatal(err)
	}
	source, err := newRegistryBundleSourceTransport(ctx, fixture.subject, Credential{}, nil, true)
	if source != nil {
		source.close()
	}
	requireSignCode(t, err, e.CodeEngineCapability)
	if err == nil || source != nil || budget.Check() == nil || budget.Used() != 64 {
		t.Fatal("registry proof discovery escaped shared operation allowance", err, budget.Check(), budget.Used())
	}
}

func TestRegistryBundleLoadUsesSameOperationReadBudget(t *testing.T) {
	fixture := newBundleRegistryFixture(t)
	predicate := []byte(`{"padding":"` + strings.Repeat("x", 8192) + `"}`)
	raw := []byte(qualificationBundle(t, ociTestDigest, ProvenancePredicateType, ociObservedIdentity, ociTestIssuer, predicate))
	descriptor := fixture.add(t, raw, ProvenancePredicateType, nil)
	fixture.index(t, []v1.Descriptor{descriptor})
	ctx, budget, err := readbudget.WithLimit(t.Context(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	source, err := newRegistryBundleSourceTransport(ctx, fixture.subject, Credential{}, nil, true)
	if err != nil {
		t.Fatal("fixture allowance must admit discovery", err)
	}
	defer source.close()
	before := budget.Used()
	got, err := source.bundle(ctx, descriptor)
	requireSignCode(t, err, e.CodeEngineCapability)
	if err == nil || len(got) != 0 || before == 0 || budget.Used() != 4096 || budget.Check() == nil {
		t.Fatal("bundle body did not share discovery allowance", err, before, budget.Used())
	}
}
