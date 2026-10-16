// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func TestPreparedBuildRecompilesBeforeProducers(t *testing.T) {
	profile, build := nightlyIdentityFixture()
	plan, _ := nightlyCompiledFixture(t, profile, build)
	digest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	key := func() string {
		private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		der, err := x509.MarshalPKIXPublicKey(&private.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	}
	policy := lifecycle.LocalQualificationPolicy{
		SchemaVersion: lifecycle.LocalQualificationPolicySchema, Lane: "unqualified-local-execution",
		DefinitionDigest: plan.DefinitionDigest, EnvironmentDigest: plan.EnvironmentDigest, PlanDigest: digest,
		Platform: "linux/arm64", CosignDigest: "sha256:" + strings.Repeat("a", 64), BuilderKey: key(), SmokeKey: key(), QualifierKey: key(),
		Smoke: lifecycle.QualificationSmokePolicy{Images: map[string]string{"linux/arm64": "registry.example.test/smoke@sha256:" + strings.Repeat("b", 64)}, Tools: map[string]string{"nvkectl": "nvkectl-version-json"}},
	}
	if err := policy.RequirePlan(plan); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"exact", "changed inputs", "changed compiler", "invalid output", "unknown field", "duplicate field", "failed compiler"} {
		t.Run(mode, func(t *testing.T) {
			root := privateTestDir(t)
			cfg := BuildConfig{Repository: root, AttemptPrefix: "prepared", Jobs: 4, Prepared: true, QualificationPolicy: filepath.Join(root, "policy.json")}
			writeInvocationFixture(t, cfg, preparedInvocationFixture(cfg))
			bootstrap := filepath.Dir(builtInputs(root, cfg.AttemptPrefix).NVKR)
			write := func(path string, value any) {
				raw, err := canonical.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			write(cfg.QualificationPolicy, policy)
			write(filepath.Join(bootstrap, "selected-plan.json"), plan)
			fresh := *plan
			if mode == "changed inputs" {
				fresh.DefinitionDigest = "sha256:" + strings.Repeat("c", 64)
			}
			if mode == "changed compiler" {
				fresh.GeneratorVersion = "changed-compiler"
			}
			write(filepath.Join(bootstrap, "fresh-plan.json"), fresh)
			if mode == "invalid output" {
				if err := os.WriteFile(filepath.Join(bootstrap, "fresh-plan.json"), []byte("invalid"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "unknown field" || mode == "duplicate field" {
				raw, err := os.ReadFile(filepath.Join(bootstrap, "fresh-plan.json"))
				if err != nil {
					t.Fatal(err)
				}
				prefix := `{"unrecognized":true,`
				if mode == "duplicate field" {
					prefix = `{"definitionDigest":"` + plan.DefinitionDigest + `",`
				}
				if err := os.WriteFile(filepath.Join(bootstrap, "fresh-plan.json"), append([]byte(prefix), raw[1:]...), 0600); err != nil {
					t.Fatal(err)
				}
			}
			body := "#!/bin/sh\nset -eu\nif [ \"$2\" = plan ]; then\n  printf plan >> \"$CALLS\"\n  cat \"$(dirname \"$0\")/fresh-plan.json\"\n"
			if mode == "failed compiler" {
				body += "  exit 1\n"
			}
			body += "else\n  printf producer >> \"$CALLS\"\nfi\n"
			if err := os.WriteFile(filepath.Join(bootstrap, "nvkr"), []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			calls := filepath.Join(root, "calls")
			err := buildLocalArtifacts(t.Context(), Runner{Env: []string{"PATH=/usr/bin:/bin", "CALLS=" + calls}}, filepath.Join(root, "must-not-bootstrap"), cfg)
			if (err == nil) != (mode == "exact") {
				t.Fatalf("mode=%s error=%v", mode, err)
			}
			if mode != "exact" {
				code := nvkrerr.CodeBuildFailed
				if mode == "changed inputs" {
					code = nvkrerr.CodeUnverifiedRef
				}
				if mode == "changed compiler" {
					code = nvkrerr.CodeTrustPolicyMissing
				}
				assertNightlyCode(t, err, code)
			}
			raw, readErr := os.ReadFile(calls)
			want := "plan"
			if mode == "exact" {
				want += "producer"
			}
			if readErr != nil || string(raw) != want {
				t.Fatalf("calls=%q want=%q error=%v", raw, want, readErr)
			}
		})
	}
}
