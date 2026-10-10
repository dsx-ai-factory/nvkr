// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"testing"

	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestRunPlanSourceAccessBindingAndEngineAdmission(t *testing.T) {
	plan := buildOnlyPlan(t, "")
	source := ""
	for _, node := range plan.Builds[0].Graph.Nodes {
		if node.Kind == ir.KindFetch && node.Repo != "" {
			source = node.Repo
			break
		}
	}
	if source == "" {
		t.Fatal("test plan has no fetch source")
	}
	plan.Environment.SourceAccess = map[string]authored.SourceAccess{source: {
		Mirror: "https://github.com/example/nvke-mirror", CredentialRef: "github:NVKE_SOURCE_TOKEN",
	}}
	digest, err := plan.Environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	plan.EnvironmentDigest = digest
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}

	if err := AdmitLocal(context.Background(), plan, LocalRunOptions{}); err == nil {
		t.Fatal("local execution accepted source authentication it cannot enforce")
	} else {
		assertSourceAccessCode(t, err, nvkrerr.CodeEngineCapability)
	}
	plan.Environment.SourceAccess = map[string]authored.SourceAccess{"https://github.com/unknown/source": {
		Mirror: "https://github.com/example/nvke-mirror", CredentialRef: "github:NVKE_SOURCE_TOKEN",
	}}
	digest, err = plan.Environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	plan.EnvironmentDigest = digest
	if err := plan.Validate(); err == nil {
		t.Fatal("unmatched source access accepted")
	} else {
		assertSourceAccessCode(t, err, nvkrerr.CodeInvalidCatalog)
	}
}

func assertSourceAccessCode(t *testing.T, err error, want nvkrerr.Code) {
	t.Helper()
	coded, ok := err.(*nvkrerr.Error)
	if !ok || coded.Code != want {
		t.Fatalf("error = %T %v, want code %s", err, err, want)
	}
}
