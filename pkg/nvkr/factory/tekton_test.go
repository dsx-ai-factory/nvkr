// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http/httptest"
	"os"
	"regexp"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationpolicy"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

func tektonTestPlan(t *testing.T) *Plan {
	t.Helper()
	plan := signedTestPlan(t, cosignSigningProfile(), Target{Kind: TargetOCI, Ref: "registry.example.invalid/test", Anonymous: true})
	server := httptest.NewTLSServer(nil)
	defer server.Close()
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
	env := plan.Environment.Clone()
	env.RunsOn = ""
	env.Publication.Mode = "per-output-v1"
	env.Runtime.Runtime = "docker"
	env.Tekton = &definition.TektonExecution{Namespace: "nvkr", Platform: "linux/amd64",
		APIServer: "https://kubernetes.default.svc", CertificateAuthority: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})),
		RunnerImage: env.UtilityContainer, RuntimeImage: env.UtilityContainer, StorageClass: "standard", WorkspaceGiB: 20,
		PublisherPublicKey: key(), QualifierPublicKey: key(), SmokePublicKey: key(),
		Smoke: qualificationpolicy.Smoke{Images: map[string]string{"linux/amd64": "index.docker.io/library/" + env.UtilityContainer}, Tools: map[string]string{"nvkectl": "nvkectl-version-json"}}}
	env.Signing = &definition.Signing{Mode: SigningModeCosign, Issuer: env.Tekton.APIServer,
		Identity: "^" + regexp.QuoteMeta(env.Tekton.APIServer+"/api/v1/namespaces/nvkr/serviceaccounts/nvkr-publisher") + "$"}
	stack, err := catalog.ApplyStack(plan.CatalogSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Compile(t.Context(), plan.Definition, env, stack, plan.GeneratorVersion)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestTektonResourcesUseRunnerAndIsolateCredentials(t *testing.T) {
	plan := tektonTestPlan(t)
	first, err := renderTekton(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderTekton(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Files[tektonResourcePath], second.Files[tektonResourcePath]) {
		t.Fatal("Tekton rendering is nondeterministic")
	}
	var resources struct {
		Items []struct {
			Kind string `json:"kind"`
			Spec struct {
				PipelineSpec struct {
					Tasks []struct {
						Name     string
						TaskSpec struct {
							Steps []struct {
								Command, Args []string
								VolumeMounts  []struct{ Name string }
							}
							Sidecars []struct {
								Args         []string
								VolumeMounts []struct{ Name, MountPath string }
							}
							Volumes []struct {
								Name   string
								Secret *struct{ SecretName string }
							}
						}
					}
				} `json:"pipelineSpec"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(first.Files[tektonResourcePath], &resources); err != nil {
		t.Fatal(err)
	}
	var count int
	for _, item := range resources.Items {
		if item.Kind == "Secret" {
			t.Fatal("generated bundle included private credentials")
		}
		if item.Kind != "PipelineRun" {
			continue
		}
		count++
		for _, task := range item.Spec.PipelineSpec.Tasks {
			if len(task.TaskSpec.Steps) != 1 {
				t.Fatalf("task %s bypasses the single runner", task.Name)
			}
			step := task.TaskSpec.Steps[0]
			if len(step.Command) != 1 || step.Command[0] != "/usr/local/bin/nvkr" || len(step.Args) < 3 || step.Args[0] != "factory" || step.Args[1] != "tekton" {
				t.Fatal("task embeds build policy instead of invoking the runner")
			}
			for _, sidecar := range task.TaskSpec.Sidecars {
				if len(sidecar.Args) == 0 || sidecar.Args[0] != "--host=unix:///var/run/docker.sock" {
					t.Fatal("private daemon does not serve the held runtime endpoint")
				}
				for _, mount := range sidecar.VolumeMounts {
					if mount.Name == "docker-socket" && mount.MountPath != "/var/run" {
						t.Fatal("private socket is not mounted at the held runtime endpoint")
					}
					if mount.Name != "work" && mount.Name != "docker-data" && mount.Name != "docker-socket" {
						t.Fatal("container daemon received provider credentials or transported inputs")
					}
				}
			}
		}
	}
	if count != 1 {
		t.Fatal("expected exactly one embedded PipelineRun")
	}
	// An explicit diagnostic export lets the pinned kind acceptance validate the
	// same rendered bytes against its CRDs without committing machine material.
	if destination := os.Getenv("NVKR_TEKTON_RENDER_PROBE"); destination != "" {
		if err := os.WriteFile(destination, first.Files[tektonResourcePath], 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTektonSourceVendoringReceivesPrivateRuntime(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command []string
	}{
		{"plain fetch", nil},
		{"dependency vendoring", []string{"go", "mod", "vendor"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := tektonTestPlan(t)
			base := original.CatalogSnapshot
			for name, component := range base.Components {
				if component.FromSource {
					component.VendorCommand = tc.command
					base.Components[name] = component
				}
			}
			stack, err := catalog.ApplyStack(base)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := Compile(t.Context(), original.Definition, original.Environment, stack, original.GeneratorVersion)
			if err != nil {
				t.Fatal(err)
			}
			bundle, err := renderTekton(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			var resources struct {
				Items []struct {
					Spec struct {
						PipelineSpec struct {
							Tasks []struct {
								Name     string
								TaskSpec struct {
									Sidecars []json.RawMessage
									Steps    []struct {
										Env []struct{ Name, Value string }
									}
								}
							}
						}
					}
				}
			}
			if err := json.Unmarshal(bundle.Files[tektonResourcePath], &resources); err != nil {
				t.Fatal(err)
			}
			projection, err := projectTektonExecution(t.Context(), plan, "linux/amd64")
			if err != nil {
				t.Fatal(err)
			}
			checked := 0
			for _, node := range plan.Graph.Nodes {
				if node.Kind != NodeFetch || node.IRNode == "" {
					continue
				}
				for _, resource := range resources.Items {
					for _, task := range resource.Spec.PipelineSpec.Tasks {
						if task.Name != projection.owners[node.ID] {
							continue
						}
						checked++
						wantRuntime := len(tc.command) != 0
						if (len(task.TaskSpec.Sidecars) == 1) != wantRuntime {
							t.Fatalf("fetch %s runtime sidecars = %d, vendoring = %t", node.ID, len(task.TaskSpec.Sidecars), wantRuntime)
						}
						hasSocket := false
						for _, env := range task.TaskSpec.Steps[0].Env {
							if env.Name == "DOCKER_HOST" && env.Value == "unix:///var/run/docker.sock" {
								hasSocket = true
							}
						}
						if hasSocket != wantRuntime {
							t.Fatalf("fetch %s private socket = %t, vendoring = %t", node.ID, hasSocket, wantRuntime)
						}
					}
				}
			}
			if checked == 0 {
				t.Fatal("fixture did not exercise source fetching")
			}
		})
	}
}

func TestTektonPublicVerificationDoesNotRequireSigningAuthority(t *testing.T) {
	plan := tektonTestPlan(t)
	_, artifacts := planIndex(plan)
	for _, tc := range []struct {
		kind string
		code e.Code
	}{
		{NodeVerifyPublication, ""},
		{NodePublish, ""},
		{NodeSign, e.CodeVerifyFailed},
		{NodeAttestEvidence, e.CodeVerifyFailed},
		{NodePackage, e.CodeEngineCapability},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			node := Node{Kind: tc.kind, Target: "release"}
			// The Plan has no authenticated execution receipt. Verification may
			// select public trust, but it must not acquire a private key capability.
			opts := sign.CosignOCIOptions{PrivateKeyPath: "/synthetic/unselected-key.pem"}
			err := configureTektonOCI(plan, node, &opts)
			if tc.code != "" {
				assertFactoryErrorCode(t, err, tc.code)
				return
			}
			if err != nil || opts.KeyTrust == nil || opts.PrivateKeyPath != "" {
				t.Fatalf("public verification acquired signing authority: %v", err)
			}
			if _, err := productionOCISigner(t.Context(), plan, node, artifacts); err != nil {
				t.Fatalf("nested publication verifier requires another task's receipt: %v", err)
			}
			opts.Identity, opts.Issuer = plan.Environment.Signing.Identity, plan.Environment.Signing.Issuer
			opts.Runner = sign.RunnerFunc(func(context.Context, string, []string, io.Writer, io.Writer) error {
				t.Fatal("public-only verifier attempted a signing subprocess")
				return nil
			})
			verifier, err := sign.NewCosignOCISigner(opts)
			if err != nil {
				t.Fatal(err)
			}
			_, err = verifier.SignAndAttest(t.Context(), sign.Subject{
				Ref: "registry.example.invalid/test@" + testCosignArchiveDigest, Digest: testCosignArchiveDigest,
			}, []byte(`{"buildDefinition":{}}`))
			assertFactoryErrorCode(t, err, e.CodeTrustPolicyMissing)
		})
	}
}
