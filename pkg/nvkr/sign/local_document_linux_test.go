// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// This real-key test exercises the production descriptor adapter. It does not
// assert hosted OIDC, registry publication or qualification-complete acceptance.
func TestLocalCosignRealKeyRoundTrip(t *testing.T) {
	binary, pin := os.Getenv("NVKR_TEST_COSIGN_BINARY"), os.Getenv("NVKR_TEST_COSIGN_SHA256")
	if binary == "" || pin == "" {
		t.Skip("set NVKR_TEST_COSIGN_BINARY and NVKR_TEST_COSIGN_SHA256 for real pinned Linux cosign")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	signer, err := NewLocalCosign(binary, pin)
	if err != nil {
		t.Fatal(err)
	}
	// Authenticate the test executable before using it to generate local keys.
	_, closeExecutable, err := snapshotPinnedCosignExecutableHeldContext(ctx, binary, pin)
	if err != nil {
		t.Fatal(err)
	}
	closeExecutable()
	keys := func() ([]byte, []byte) {
		t.Helper()
		root := t.TempDir()
		cmd := exec.CommandContext(ctx, binary, "generate-key-pair", "--output-key-prefix", filepath.Join(root, "cosign"))
		cmd.Env = append(qualifiedCosignEnvironment(os.Environ()), "COSIGN_PASSWORD=")
		cmd.WaitDelay = 5 * time.Second
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("generate fixture keys: %v: %s", err, out)
		}
		private, err := os.ReadFile(filepath.Join(root, "cosign.key"))
		if err != nil {
			t.Fatal(err)
		}
		public, err := os.ReadFile(filepath.Join(root, "cosign.pub"))
		if err != nil {
			t.Fatal(err)
		}
		return private, public
	}
	private, public := keys()
	document := []byte(`{"schemaVersion":"nvkr.dev/local-signing-test/v1","subject":"fixture"}`)
	receipt, err := signer.SignDocument(ctx, document, private, public)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := receipt.Signature()
	if err != nil {
		t.Fatal(err)
	}
	if err := receipt.RequireFor(ctx, document, public); err != nil {
		t.Fatal(err)
	}
	_, wrongKey := keys()
	for _, tc := range []struct {
		name          string
		document, key []byte
	}{
		{"different document", []byte("different"), public},
		{"different key", document, wrongKey},
	} {
		t.Run("receipt/"+tc.name, func(t *testing.T) {
			err := receipt.RequireFor(ctx, tc.document, tc.key)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeSignatureInvalid {
				t.Fatalf("receipt binding=%v; want %s", err, e.CodeSignatureInvalid)
			}
		})
	}
	for _, tc := range []struct {
		name                     string
		document, signature, key []byte
		pass                     bool
	}{
		{"exact", document, signature, public, true},
		{"mutated document", append(append([]byte(nil), document...), '\n'), signature, public, false},
		{"wrong key", document, signature, wrongKey, false},
		{"missing signature", document, nil, public, false},
		{"invalid signature", document, []byte("invalid"), public, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := signer.VerifyDocument(ctx, tc.document, tc.signature, tc.key)
			if (err == nil) != tc.pass {
				t.Fatalf("verification=%v pass=%v", err, tc.pass)
			}
			if !tc.pass {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeSignatureInvalid {
					t.Fatalf("verification=%v; want %s", err, e.CodeSignatureInvalid)
				}
			}
		})
	}
	if _, err := signer.SignDocument(ctx, document, private, wrongKey); err != nil {
		var coded *e.Error
		if !errors.As(err, &coded) || coded.Code != e.CodeSignatureInvalid {
			t.Fatalf("mismatched key: %v; want %s", err, e.CodeSignatureInvalid)
		}
	} else {
		t.Fatal("mismatched signer and independent public key issued a receipt")
	}
}
