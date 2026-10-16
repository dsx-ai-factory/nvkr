// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

func TestLoadLocalQualificationPolicy(t *testing.T) {
	p := localPolicyFixture(t)
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		raw  []byte
		code e.Code
	}{
		{"formatted JSON", raw, ""},
		{"unknown field", append([]byte(`{"source":{},`), raw[1:]...), e.CodeTrustPolicyMissing},
		{"duplicate field", append([]byte(`{"platform":"linux/amd64",`), raw[1:]...), e.CodeTrustPolicyMissing},
		{"second document", append(append([]byte(nil), raw...), raw...), e.CodeTrustPolicyMissing},
		{"oversized", []byte(strings.Repeat(" ", maxQualificationPolicyBytes+1)), e.CodeTrustPolicyMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "policy.json")
			if err := os.WriteFile(path, tc.raw, 0o600); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadLocalQualificationPolicy(t.Context(), path)
			if tc.code != "" {
				assertCode(t, err, tc.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want, _ := p.Digest()
			got, err := loaded.Digest()
			if err != nil || got != want {
				t.Fatalf("loaded identity changed: %v", err)
			}
			canonicalBytes, err := canonical.Marshal(loaded)
			if err != nil || len(canonicalBytes) == 0 {
				t.Fatal(err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = LoadLocalQualificationPolicy(ctx, "missing")
	assertCode(t, err, e.CodeTimeout)
	_, err = LoadLocalQualificationPolicy(nil, "missing") //nolint:staticcheck // Exercise the public boundary's nil-context refusal.
	assertCode(t, err, e.CodeUsage)
}

func localPolicyFixture(t *testing.T) LocalQualificationPolicy {
	t.Helper()
	key := func() string {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	}
	smoke := *nativeSmokeTestPolicy().Smoke
	smoke.Workflow = ""
	return LocalQualificationPolicy{SchemaVersion: LocalQualificationPolicySchema, Lane: factory.TierLocalExecution.String(),
		DefinitionDigest: nativeSmokeTestDigest("1"), EnvironmentDigest: nativeSmokeTestDigest("2"), PlanDigest: nativeSmokeTestDigest("3"),
		Platform: "linux/amd64", CosignDigest: nativeSmokeTestDigest("4"), BuilderKey: key(), SmokeKey: key(), QualifierKey: key(), Smoke: smoke}
}

func TestLocalQualificationPolicyAdmission(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*LocalQualificationPolicy)
		code e.Code
	}{
		{"exact", func(*LocalQualificationPolicy) {}, ""},
		{"hosted schema", func(p *LocalQualificationPolicy) { p.SchemaVersion = QualificationPolicySchema }, e.CodeTrustPolicyMissing},
		{"hosted lane", func(p *LocalQualificationPolicy) { p.Lane = LanePipeline }, e.CodeTrustPolicyMissing},
		{"hosted worker", func(p *LocalQualificationPolicy) { p.Smoke.Workflow = ".github/workflows/smoke.yml" }, e.CodeTrustPolicyMissing},
		{"no pin", func(p *LocalQualificationPolicy) { p.CosignDigest = "" }, e.CodeTrustPolicyMissing},
		{"no Plan", func(p *LocalQualificationPolicy) { p.PlanDigest = "" }, e.CodeTrustPolicyMissing},
		{"no builder key", func(p *LocalQualificationPolicy) { p.BuilderKey = "" }, e.CodeTrustPolicyMissing},
		{"same role", func(p *LocalQualificationPolicy) { p.SmokeKey = p.BuilderKey }, e.CodeTrustPolicyMissing},
		{"same key different PEM", func(p *LocalQualificationPolicy) {
			p.QualifierKey = "\n" + strings.ReplaceAll(p.BuilderKey, "\n", "\r\n")
		}, e.CodeTrustPolicyMissing},
		{"wrong platform image", func(p *LocalQualificationPolicy) { p.Platform = "linux/arm64" }, e.CodeTrustPolicyMissing},
		{"extra platform", func(p *LocalQualificationPolicy) { p.Smoke.Images["linux/arm64"] = p.Smoke.Images["linux/amd64"] }, e.CodeTrustPolicyMissing},
		{"unsupported profile", func(p *LocalQualificationPolicy) { p.Smoke.Tools["tool"] = "sh" }, e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := localPolicyFixture(t)
			tc.edit(&p)
			err := p.Validate()
			if tc.code != "" {
				assertCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLocalQualificationPolicyIdentityAndOwnership(t *testing.T) {
	p := localPolicyFixture(t)
	want, err := p.Digest()
	if err != nil {
		t.Fatal(err)
	}
	p.BuilderKey = "\n" + strings.ReplaceAll(p.BuilderKey, "\n", "\r\n")
	got, err := p.Digest()
	if err != nil || got != want {
		t.Fatalf("key presentation changed policy identity: %v", err)
	}
	frozen, err := p.freeze()
	if err != nil {
		t.Fatal(err)
	}
	p.Smoke.Tools["tool"] = "kubectl-client-version-json"
	if frozen.Smoke.Tools["tool"] != "helm-version" {
		t.Fatal("frozen policy aliases caller map")
	}
	p = frozen
	p.BuilderKey, p.QualifierKey = p.QualifierKey, p.BuilderKey
	got, err = p.Digest()
	if err != nil || got == want {
		t.Fatalf("role change did not change policy identity: %v", err)
	}
}

func TestLocalQualificationPolicyRequiresExactPlan(t *testing.T) {
	_, prepared := capturedLocalFixture(t)
	p := localPolicyFixture(t)
	p.PlanDigest, p.DefinitionDigest, p.EnvironmentDigest = prepared.run.PlanDigest, prepared.run.DefinitionDigest, prepared.run.EnvironmentDigest
	p.Platform = prepared.plan.Builds[0].Platform
	p.Smoke.Images = map[string]string{p.Platform: "registry.example.test/smoke@" + nativeSmokeTestDigest("1")}
	p.Smoke.Tools = map[string]string{}
	for _, output := range prepared.plan.LifecycleOutputs {
		for _, component := range output.Components {
			p.Smoke.Tools[component] = "helm-version"
		}
	}
	if err := p.RequirePlan(prepared.plan); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"plan", "definition", "environment", "scope"} {
		t.Run(field, func(t *testing.T) {
			changed := p
			switch field {
			case "plan":
				changed.PlanDigest = nativeSmokeTestDigest("9")
			case "definition":
				changed.DefinitionDigest = nativeSmokeTestDigest("9")
			case "environment":
				changed.EnvironmentDigest = nativeSmokeTestDigest("9")
			case "scope":
				changed.Smoke.Tools = map[string]string{"other": "helm-version"}
			}
			code := e.CodeTrustPolicyMissing
			if field == "scope" {
				code = e.CodeGateUnmet
			}
			assertCode(t, changed.RequirePlan(prepared.plan), code)
		})
	}
}
