// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"regexp"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationpolicy"
)

func tektonEnvironmentFixture(t *testing.T) Environment {
	t.Helper()
	publicKey := func() (string, *ecdsa.PrivateKey) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), key
	}
	publisher, caKey := publicKey()
	qualifier, _ := publicKey()
	smoke, _ := publicKey()
	template := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Unix(0, 0), NotAfter: time.Unix(2000000000, 0)}
	certificate, err := x509.CreateCertificate(rand.Reader, template, template, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	image := "example.invalid/runner@sha256:" + strings.Repeat("a", 64)
	return Environment{APIVersion: EnvironmentAPIVersion, Kind: EnvironmentKind, Metadata: Metadata{Name: "tekton-test"},
		UtilityPlatform: "linux/amd64", UtilityContainer: image,
		Runtime:     Runtime{Runtime: "docker"},
		Publication: &Publication{Mode: "per-output-v1", RepositoryPrefix: "registry.example.invalid/nvkr", Anonymous: true},
		Signing: &Signing{Mode: "cosign", Issuer: "https://cluster.example.invalid",
			Identity: "^" + regexp.QuoteMeta("https://cluster.example.invalid/api/v1/namespaces/nvkr/serviceaccounts/nvkr-publisher") + "$"},
		Tekton: &TektonExecution{Namespace: "nvkr", Platform: "linux/amd64", APIServer: "https://cluster.example.invalid",
			CertificateAuthority: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate})),
			RunnerImage:          image, RuntimeImage: image, StorageClass: "standard", WorkspaceGiB: 20,
			PublisherPublicKey: publisher, QualifierPublicKey: qualifier, SmokePublicKey: smoke,
			Smoke: qualificationpolicy.Smoke{Images: map[string]string{"linux/amd64": image}, Tools: map[string]string{"nvkectl": "nvkectl-version-json"}}}}
}

func TestTektonEnvironmentAdmission(t *testing.T) {
	base := tektonEnvironmentFixture(t)
	for _, tc := range []struct {
		name string
		edit func(*Environment)
		code e.Code
	}{
		{"explicit cluster policy", func(*Environment) {}, ""},
		{"GitHub placement", func(e *Environment) { e.RunsOn = "ubuntu-latest" }, e.CodeEngineCapability},
		{"GitHub runtime", func(e *Environment) { e.GitHubRuntimePlan = true }, e.CodeEngineCapability},
		{"platform mismatch", func(e *Environment) { e.Tekton.Platform = "linux/arm64" }, e.CodeEngineCapability},
		{"no platform", func(e *Environment) { e.Tekton.Platform = "" }, e.CodeEngineCapability},
		{"unbounded storage", func(e *Environment) { e.Tekton.WorkspaceGiB = 0 }, e.CodeEngineCapability},
		{"missing namespace", func(e *Environment) { e.Tekton.Namespace = "" }, e.CodeEngineCapability},
		{"mutable runner", func(e *Environment) { e.Tekton.RunnerImage = "example.invalid/runner:latest" }, e.CodeUnpinnedSource},
		{"mutable runtime", func(e *Environment) { e.Tekton.RuntimeImage = "docker:latest" }, e.CodeUnpinnedSource},
		{"insecure API", func(e *Environment) { e.Tekton.APIServer = "http://cluster.example.invalid" }, e.CodeTrustPolicyMissing},
		{"missing root", func(e *Environment) { e.Tekton.CertificateAuthority = "" }, e.CodeTrustPolicyMissing},
		{"missing key", func(e *Environment) { e.Tekton.PublisherPublicKey = "" }, e.CodeTrustPolicyMissing},
		{"private key field", func(e *Environment) {
			e.Tekton.PublisherPublicKey = "-----BEGIN PRIVATE KEY-----\nSYNTHETIC\n-----END PRIVATE KEY-----\n"
		}, e.CodeTrustPolicyMissing},
		{"same role key", func(e *Environment) { e.Tekton.QualifierPublicKey = e.Tekton.PublisherPublicKey }, e.CodeTrustPolicyMissing},
		{"same key with different PEM", func(e *Environment) {
			e.Tekton.QualifierPublicKey = strings.ReplaceAll(e.Tekton.PublisherPublicKey, "\n", "\r\n")
		}, e.CodeTrustPolicyMissing},
		{"ambient setup", func(e *Environment) { e.Runtime.Setup = []string{"true"} }, e.CodeEngineCapability},
		{"aggregate publication", func(e *Environment) { e.Publication.Mode = "" }, e.CodeEngineCapability},
		{"hosted credential", func(e *Environment) {
			e.Publication.Anonymous = false
			e.Publication.CredentialRef = "github:SYNTHETIC"
		}, e.CodeEngineCapability},
		{"unsigned", func(e *Environment) { e.Signing = nil }, e.CodeTrustPolicyMissing},
		{"dry run", func(e *Environment) { e.Signing.Mode = "dry-run" }, e.CodeTrustPolicyMissing},
		{"foreign issuer", func(e *Environment) { e.Signing.Issuer = "https://token.actions.githubusercontent.com" }, e.CodeTrustPolicyMissing},
		{"broad identity", func(e *Environment) { e.Signing.Identity = ".*" }, e.CodeTrustPolicyMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			environment := base.Clone()
			tc.edit(&environment)
			err := environment.Validate()
			if tc.code != "" {
				requireCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
	if base.Tekton.Platform != "linux/amd64" || base.Tekton.QualifierPublicKey == base.Tekton.PublisherPublicKey {
		t.Fatal("cloning retained mutable Tekton policy aliases")
	}
}
