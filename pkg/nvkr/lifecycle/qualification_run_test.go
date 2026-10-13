// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

func qualificationHandoffFixture(t *testing.T) (string, QualificationHandoff, *factory.Plan) {
	t.Helper()
	root := t.TempDir()
	definition := testDefinition(t)
	addTestSigningTool(t, &definition)
	for _, name := range []string{catalog.RegistryFile, catalog.LockFile} {
		raw, err := os.ReadFile(filepath.Join(definition.Catalog.Base, name))
		if err != nil {
			t.Fatal(err)
		}
		writeQualificationHandoffFile(t, filepath.Join(root, "catalog", name), raw)
	}
	definition.Catalog.Base = "catalog"
	writeQualificationHandoffFile(t, filepath.Join(root, "definition.json"), qualificationHandoffJSON(t, definition))
	environment := testEnvironment()
	environment.Signing = &Signing{Mode: factory.SigningModeCosign, Identity: "^" + regexp.QuoteMeta(qualificationSourceFixture().value.BuilderID) + "$", Issuer: "https://token.actions.githubusercontent.com"}
	environment.Publication.Mode = "per-output-v1"
	writeQualificationHandoffFile(t, filepath.Join(root, "environment.json"), qualificationHandoffJSON(t, environment))
	plan, snapshot, err := factory.CaptureCompileFiles(t.Context(), root, "definition.json", "environment.json", "qualification-handoff-test")
	if err != nil {
		t.Fatal(err)
	}
	reference := "registry.example.test/product@sha256:" + strings.Repeat("a", 64)
	// This fixture exercises saved compiler identity and transport admission only.
	// Artifact checks separately authenticate predicates and published bytes.
	handoff := QualificationHandoff{SchemaVersion: QualificationHandoffSchema,
		Source: qualificationSourceFixture().value, Plan: qualificationHandoffJSON(t, plan), Inputs: snapshot,
		Candidate: QualificationCandidate{SchemaVersion: QualificationCandidateSchema, Reference: reference,
			Predicates: []QualificationPredicate{{Reference: reference, Predicate: []byte(`{}`)}}}}
	return root, handoff, plan
}

func qualificationHandoffJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := canonical.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func writeQualificationHandoffFile(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestQualificationHandoffRejectsLegacyAggregateBeforeReplay(t *testing.T) {
	_, handoff, original := qualificationHandoffFixture(t)
	handoff.Plan = qualificationHandoffJSON(t, legacyQualificationPlan(t, original))
	// Missing captured inputs would fail replay with a different code. Mode
	// admission must reject this original Plan before replay can read anything.
	handoff.Inputs = factory.InputSnapshot{}
	_, plan, err := prepareQualificationHandoff(t.Context(), qualificationHandoffJSON(t, handoff), handoff.Source)
	assertCode(t, err, e.CodeEngineCapability)
	if plan != nil {
		t.Fatal("unsupported legacy handoff returned a compiled Plan")
	}
}

func TestQualificationHandoffPreservesSavedPlanAfterRelocation(t *testing.T) {
	root, handoff, original := qualificationHandoffFixture(t)
	raw := qualificationHandoffJSON(t, handoff)
	want, err := original.Digest()
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(t.TempDir(), "relocated")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	got, compiled, err := prepareQualificationHandoff(t.Context(), raw, handoff.Source)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := compiled.Digest()
	if err != nil || digest != want || !bytes.Equal(got.Plan, handoff.Plan) || !reflect.DeepEqual(got.Candidate, handoff.Candidate) {
		t.Fatalf("saved compiler/predicate bytes changed: digest=%s want=%s error=%v", digest, want, err)
	}
	relocated, snapshot, err := factory.CaptureCompileFiles(t.Context(), moved, "definition.json", "environment.json", original.GeneratorVersion)
	if err != nil {
		t.Fatal(err)
	}
	digest, err = relocated.Digest()
	if err != nil || digest != want || !reflect.DeepEqual(snapshot, handoff.Inputs) {
		t.Fatalf("relocation changed captured identity: digest=%s want=%s error=%v", digest, want, err)
	}
}

func TestQualificationHandoffRejectsAlteredClosure(t *testing.T) {
	_, original, _ := qualificationHandoffFixture(t)
	for _, tc := range []struct {
		name string
		edit func(*QualificationHandoff)
		code e.Code
	}{
		{"handoff schema", func(h *QualificationHandoff) { h.SchemaVersion += "-future" }, e.CodeVerifyFailed},
		{"source attempt", func(h *QualificationHandoff) { h.Source.Attempt = "3" }, e.CodeVerifyFailed},
		{"source commit", func(h *QualificationHandoff) { h.Source.Commit = strings.Repeat("b", 40) }, e.CodeVerifyFailed},
		{"source workflow", func(h *QualificationHandoff) { h.Source.Workflow = ".github/workflows/other.yml" }, e.CodeVerifyFailed},
		{"candidate schema", func(h *QualificationHandoff) { h.Candidate.SchemaVersion += "-future" }, e.CodeProvenanceMissing},
		{"missing publication", func(h *QualificationHandoff) { h.Candidate.Reference = "" }, e.CodeProvenanceMissing},
		{"missing original predicates", func(h *QualificationHandoff) { h.Candidate.Predicates = nil }, e.CodeProvenanceMissing},
		{"missing Plan", func(h *QualificationHandoff) { h.Plan = nil }, e.CodeProvenanceMissing},
		{"noncanonical Plan", func(h *QualificationHandoff) { h.Plan = append(h.Plan, '\n') }, e.CodeProvenanceMissing},
		{"Plan schema", func(h *QualificationHandoff) {
			editQualificationHandoffPlan(t, h, func(p *factory.Plan) { p.SchemaVersion += "-future" })
		}, e.CodeInvalidCatalog},
		{"Plan identity", func(h *QualificationHandoff) {
			editQualificationHandoffPlan(t, h, func(p *factory.Plan) { p.DefinitionDigest = "sha256:" + strings.Repeat("b", 64) })
		}, e.CodeUnverifiedRef},
		{"different valid Plan", func(h *QualificationHandoff) {
			editQualificationHandoffPlan(t, h, func(p *factory.Plan) { p.DefinitionPath = "other-definition.json" })
		}, e.CodePipelineDrift},
		{"captured input schema", func(h *QualificationHandoff) { h.Inputs.SchemaVersion += "-future" }, e.CodeInvalidCatalog},
		{"missing captured file", func(h *QualificationHandoff) { h.Inputs.Files = h.Inputs.Files[1:] }, e.CodeUnpinnedSource},
		{"different valid captured input", func(h *QualificationHandoff) {
			for i := range h.Inputs.Files {
				if h.Inputs.Files[i].Path == "environment.json" {
					var environment Environment
					if err := canonical.UnmarshalExact(h.Inputs.Files[i].Data, &environment); err != nil {
						t.Fatal(err)
					}
					environment.UtilityContainer = "golang@sha256:" + strings.Repeat("b", 64)
					h.Inputs.Files[i].Data = qualificationHandoffJSON(t, environment)
				}
			}
		}, e.CodePipelineDrift},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var handoff QualificationHandoff
			if err := canonical.UnmarshalExact(qualificationHandoffJSON(t, original), &handoff); err != nil {
				t.Fatal(err)
			}
			tc.edit(&handoff)
			_, plan, err := prepareQualificationHandoff(t.Context(), qualificationHandoffJSON(t, handoff), original.Source)
			assertCode(t, err, tc.code)
			if plan != nil {
				t.Fatal("refused handoff returned a usable compiled Plan")
			}
		})
	}
}

func editQualificationHandoffPlan(t *testing.T, handoff *QualificationHandoff, edit func(*factory.Plan)) {
	t.Helper()
	var plan factory.Plan
	if err := canonical.UnmarshalExact(handoff.Plan, &plan); err != nil {
		t.Fatal(err)
	}
	edit(&plan)
	handoff.Plan = qualificationHandoffJSON(t, plan)
}

func TestQualificationHandoffRequiresExactCanonicalDocument(t *testing.T) {
	_, handoff, _ := qualificationHandoffFixture(t)
	raw := qualificationHandoffJSON(t, handoff)
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"empty", nil},
		{"trailing whitespace", append(append([]byte(nil), raw...), '\n')},
		{"unknown field", append([]byte(`{"unknown":true,`), raw[1:]...)},
		{"duplicate field", append([]byte(`{"schemaVersion":"nvkr.dev/qualification-handoff/v1",`), raw[1:]...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, plan, err := prepareQualificationHandoff(t.Context(), tc.raw, handoff.Source)
			assertCode(t, err, e.CodeProvenanceMissing)
			if plan != nil {
				t.Fatal("invalid document returned a Plan")
			}
		})
	}
}

func TestQualificationRunAdmissionPrecedesIO(t *testing.T) {
	policy := qualificationPolicyFixture()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		code e.Code
	}{
		{"missing context", nil, e.CodeUsage},
		{"canceled", ctx, e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receipt, report, err := CheckQualificationRun(tc.ctx, "invalid", "/missing/policy", QualificationRuntimeOptions{})
			assertCode(t, err, tc.code)
			if receipt != nil || report != nil {
				t.Fatal("admission failure returned a result")
			}
			receipt, report, err = CheckQualificationHandoff(tc.ctx, nil, nil, policy, QualificationRuntimeOptions{})
			assertCode(t, err, tc.code)
			if receipt != nil || report != nil {
				t.Fatal("admission failure returned a result")
			}
			if tc.ctx != nil && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation cause lost")
			}
		})
	}
	if runtime.GOOS != "linux" {
		return
	}
	for _, source := range []*qualificationorigin.SourceRun{nil, {}} {
		receipt, report, err := CheckQualificationHandoff(t.Context(), nil, source, policy, QualificationRuntimeOptions{})
		assertCode(t, err, e.CodeProvenanceMissing)
		if receipt != nil || report != nil {
			t.Fatal("unauthenticated source returned a result")
		}
	}
}

func TestQualificationConsumerScopesExplicitRegistryCredentials(t *testing.T) {
	pin := "@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, reference, host string
		registry              delivery.RegistryOptions
		code                  e.Code
	}{
		{"explicit same host", "ghcr.io/product/releases" + pin, "ghcr.io", delivery.RegistryOptions{Username: "user", Password: "private"}, ""},
		{"exact custom port", "registry.example.test:8443/product/releases" + pin, "registry.example.test:8443", delivery.RegistryOptions{Username: "user", Password: "private"}, ""},
		{"mirrored signed digest", "attacker.example.test/product/releases" + pin, "ghcr.io", delivery.RegistryOptions{Username: "user", Password: "private"}, e.CodeUntrustedSigner},
		{"host suffix", "ghcr.io.attacker.example.test/product/releases" + pin, "ghcr.io", delivery.RegistryOptions{Username: "user", Password: "private"}, e.CodeUntrustedSigner},
		{"different port", "ghcr.io:8443/product/releases" + pin, "ghcr.io", delivery.RegistryOptions{Username: "user", Password: "private"}, e.CodeUntrustedSigner},
		{"missing independent host", "ghcr.io/product/releases" + pin, "", delivery.RegistryOptions{Username: "user", Password: "private"}, e.CodeTrustPolicyMissing},
		{"host is URL", "ghcr.io/product/releases" + pin, "https://ghcr.io", delivery.RegistryOptions{Username: "user", Password: "private"}, e.CodeTrustPolicyMissing},
		{"host includes namespace", "ghcr.io/product/releases" + pin, "ghcr.io/product", delivery.RegistryOptions{Username: "user", Password: "private"}, e.CodeTrustPolicyMissing},
		{"incomplete credential", "ghcr.io/product/releases" + pin, "ghcr.io", delivery.RegistryOptions{Password: "private"}, e.CodeUsage},
		{"anonymous with credential", "ghcr.io/product/releases" + pin, "ghcr.io", delivery.RegistryOptions{Anonymous: true, Username: "user", Password: "private"}, e.CodeUsage},
		{"anonymous transport", "mirror.example.test/product/releases" + pin, "", delivery.RegistryOptions{Anonymous: true}, ""},
		{"host scoped ambient keychain", "mirror.example.test/product/releases" + pin, "", delivery.RegistryOptions{}, ""},
		{"noncanonical locator", "https://ghcr.io/product/releases" + pin, "ghcr.io", delivery.RegistryOptions{Username: "user", Password: "private"}, e.CodeUnverifiedRef},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			options := QualificationRuntimeOptions{WorkspaceRoot: filepath.Join(root, "materialization"), Registry: tc.registry,
				Credential: sign.Credential{Registry: tc.host, Username: tc.registry.Username, Password: tc.registry.Password}}
			policy := qualificationPolicyFixture()
			policy.Cosign.Path = filepath.Join(root, "must-not-execute")
			consumer, err := newQualificationConsumer(tc.reference, policy, options)
			if tc.code != "" {
				assertCode(t, err, tc.code)
				if consumer != nil {
					t.Fatal("refused credential scope returned verification/pull adapters")
				}
				if strings.Contains(err.Error(), "private") {
					t.Fatal("credential leaked into admission error")
				}
			} else if err != nil || consumer == nil {
				t.Fatalf("valid credential scope rejected: %v", err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("credential admission touched workspace: %v, %v", entries, err)
			}
		})
	}
}

func TestQualificationConsumerCredentialTransportScopes(t *testing.T) {
	for _, adapter := range []struct {
		name string
		open func(string, ConsumerOptions) (*Consumer, error)
	}{
		{"files", newQualificationReferenceConsumer},
		{"run handoff", func(reference string, options ConsumerOptions) (*Consumer, error) {
			policy := qualificationPolicyFixture()
			policy.Cosign.Path, policy.Cosign.Digest = options.CosignBinary, options.CosignDigest
			return newQualificationConsumer(reference, policy, QualificationRuntimeOptions{WorkspaceRoot: options.WorkspaceRoot,
				Credential: options.Credential, Registry: options.Registry})
		}},
	} {
		t.Run(adapter.name, func(t *testing.T) {
			for _, tc := range []struct {
				name, independentHost, ambientHost string
				explicit, anonymous, wantAuth      bool
				code                               e.Code
			}{
				{name: "missing explicit host", explicit: true, code: e.CodeTrustPolicyMissing},
				{name: "mismatched explicit host", independentHost: "other.example.test", explicit: true, code: e.CodeUntrustedSigner},
				{name: "matching explicit host", independentHost: "same", explicit: true, wantAuth: true},
				{name: "anonymous ignores ambient credentials", ambientHost: "same", anonymous: true},
				{name: "ambient credentials match host", ambientHost: "same", wantAuth: true},
				{name: "ambient credentials exclude other host", ambientHost: "other.example.test"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var requests, authenticated, unexpected atomic.Int32
					authorization := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:private"))
					registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						if auth := r.Header.Get("Authorization"); auth != "" {
							if auth == authorization {
								authenticated.Add(1)
							} else {
								unexpected.Add(1)
							}
						}
						if r.URL.Path == "/v2/" {
							w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
							w.Header().Set("WWW-Authenticate", `Basic realm="qualification-registry"`)
							w.WriteHeader(http.StatusUnauthorized)
							return
						}
						// No publication is served: this fixture proves only which
						// registry receives credentials before content verification.
						w.WriteHeader(http.StatusNotFound)
					}))
					defer registry.Close()
					host := strings.TrimPrefix(registry.URL, "http://")
					independentHost, ambientHost := tc.independentHost, tc.ambientHost
					if independentHost == "same" {
						independentHost = host
					}
					if ambientHost == "same" {
						ambientHost = host
					}
					root := t.TempDir()
					dockerConfig := filepath.Join(root, "docker")
					writeQualificationHandoffFile(t, filepath.Join(dockerConfig, "config.json"), qualificationHandoffJSON(t,
						map[string]any{"auths": map[string]any{ambientHost: map[string]string{
							"auth": base64.StdEncoding.EncodeToString([]byte("user:private")),
						}}}))
					t.Setenv("DOCKER_CONFIG", dockerConfig)
					options := ConsumerOptions{CosignBinary: filepath.Join(root, "must-not-execute"),
						CosignDigest: "sha256:" + strings.Repeat("b", 64), WorkspaceRoot: filepath.Join(root, "materialization"),
						Registry: delivery.RegistryOptions{PlainHTTP: true, Anonymous: tc.anonymous}}
					if tc.explicit {
						options.Registry.Username, options.Registry.Password = "user", "private"
						options.Credential = sign.Credential{Registry: independentHost, Username: "user", Password: "private"}
					}
					reference := host + "/product/releases@sha256:" + strings.Repeat("a", 64)
					consumer, err := adapter.open(reference, options)
					if tc.code != "" {
						assertCode(t, err, tc.code)
						if consumer != nil || requests.Load() != 0 {
							t.Fatal("refused credential host reached registry transport")
						}
						return
					}
					if err != nil || consumer == nil {
						t.Fatalf("admitted transport failed construction: %v", err)
					}
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					publication, err := consumer.materialize(ctx, reference)
					if publication != nil {
						_ = publication.Close()
						t.Fatal("empty registry issued a publication")
					}
					if err == nil || ctx.Err() != nil || requests.Load() == 0 {
						t.Fatalf("registry refusal was not exercised: requests=%d error=%v context=%v", requests.Load(), err, ctx.Err())
					}
					if unexpected.Load() != 0 || (authenticated.Load() > 0) != tc.wantAuth {
						t.Fatalf("credential routing changed: authenticated requests=%d unexpected=%d want authentication=%t", authenticated.Load(), unexpected.Load(), tc.wantAuth)
					}
				})
			}
		})
	}
}
