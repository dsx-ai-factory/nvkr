// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func testOCIKeyTrust(t *testing.T) *OCIKeyTrust {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return &OCIKeyTrust{PublicKey: pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), Identity: "https://cluster.example.invalid/publisher"}
}

func TestKeyedOCIRequiresExplicitSeparatePolicy(t *testing.T) {
	key := testOCIKeyTrust(t)
	for _, tc := range []struct {
		name string
		edit func(*CosignOCIOptions)
		want e.Code
	}{
		{"exact", func(*CosignOCIOptions) {}, ""},
		{"broad identity", func(o *CosignOCIOptions) { o.Identity = ".*" }, e.CodeTrustPolicyMissing},
		{"private key without trust", func(o *CosignOCIOptions) { o.KeyTrust = nil; o.PrivateKeyPath = "synthetic.pem" }, e.CodeTrustPolicyMissing},
		{"missing public key", func(o *CosignOCIOptions) { o.KeyTrust = &OCIKeyTrust{Identity: key.Identity} }, e.CodeTrustPolicyMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := CosignOCIOptions{KeyTrust: key, Identity: "^" + regexp.QuoteMeta(key.Identity) + "$", Issuer: "https://cluster.example.invalid"}
			tc.edit(&opts)
			signer, err := NewCosignOCISigner(opts)
			if tc.want != "" {
				requireSignCode(t, err, tc.want)
				if signer != nil {
					t.Fatal("invalid key trust returned signer")
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestKeyedBundleCannotFallBackToCertificateTrust(t *testing.T) {
	key := testOCIKeyTrust(t)
	certificate := []byte(testOCIBundle(t, ociTestDigest, ProvenancePredicateType, ociObservedIdentity, ociTestIssuer))
	var object map[string]any
	if err := json.Unmarshal(certificate, &object); err != nil {
		t.Fatal(err)
	}
	object["verificationMaterial"] = map[string]any{"publicKey": map[string]string{"hint": "synthetic-key-hint"}}
	keyed, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		raw  []byte
		key  *OCIKeyTrust
		pass bool
	}{
		{"keyed under explicit key", keyed, key, true},
		{"keyed under certificate policy", keyed, nil, false},
		{"certificate under explicit key", certificate, key, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ociBundleClaims(tc.raw, ociTestDigest, ProvenancePredicateType, VerifyPolicy{IdentityRegexp: ociTestIdentity, Issuer: ociTestIssuer, keyTrust: tc.key})
			if tc.pass && err != nil {
				t.Fatal(err)
			}
			if !tc.pass {
				requireSignCode(t, err, e.CodeSignatureInvalid)
			}
		})
	}
}

func TestKeyedSigningIsolatesSecretsAndRedactsFailures(t *testing.T) {
	const secret = "SYNTHETIC-PRIVATE-KEY-BYTES"
	key := testOCIKeyTrust(t)
	path := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(path, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			calls := 0
			signer, err := NewCosignOCISigner(CosignOCIOptions{KeyTrust: key, PrivateKeyPath: path, Identity: "^" + regexp.QuoteMeta(key.Identity) + "$", Issuer: "https://cluster.example.invalid", Runner: EnvRunnerFunc(func(_ context.Context, _ string, args, env []string, _, stderr io.Writer) error {
				if args[0] == "version" {
					return nil
				}
				calls++
				index := slices.Index(args, "--key")
				if index < 0 || index+1 >= len(args) {
					t.Fatal("explicit key omitted")
				}
				private, err := os.ReadFile(args[index+1])
				if err != nil || string(private) != secret {
					t.Fatal("wrong captured key")
				}
				if !slices.Contains(args, "--tlog-upload=false") || !slices.Contains(args, "--use-signing-config=false") {
					t.Fatal("keyed invocation requested ambient signing")
				}
				for _, entry := range env {
					if strings.HasPrefix(entry, "ACTIONS_") || strings.HasPrefix(entry, "GITHUB_") || strings.HasPrefix(entry, "NVKR_CREDENTIAL=") {
						t.Fatal("unrelated credential reached keyed signer")
					}
				}
				if fail {
					_, _ = io.WriteString(stderr, secret)
					return fmt.Errorf("synthetic parser failed")
				}
				return nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			original := bytes.Clone(key.PublicKey)
			key.PublicKey[0] = 'X'
			if !bytes.Equal(signer.keyTrust.PublicKey, original) {
				t.Fatal("signer retained caller-mutable key policy")
			}
			copy(key.PublicKey, original)
			record, err := signer.SignAndAttest(t.Context(), ociSubject(), []byte(`{"buildDefinition":{}}`))
			if fail {
				requireSignCode(t, err, e.CodeSignatureInvalid)
				if strings.Contains(err.Error(), secret) {
					t.Fatal("private parser diagnostics escaped")
				}
			} else if err != nil || len(record.Ran) != 2 || calls != 2 {
				t.Fatalf("keyed signing contract: %v calls %d", err, calls)
			}
		})
	}
}
