// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestNormalizeLocalPublicKey(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	valid := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	other, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherDER, err := x509.MarshalPKIXPublicKey(&other.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		raw  []byte
		pass bool
	}{
		{"exact", valid, true},
		{"presentation", append([]byte("\n\t"), bytes.ReplaceAll(valid, []byte("\n"), []byte("\r\n"))...), true},
		{"empty", nil, false},
		{"oversized", make([]byte, maxLocalKeyBytes+1), false},
		{"wrong curve", pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: otherDER}), false},
		{"invalid DER", pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("invalid")}), false},
		{"private label", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), false},
		{"headers", pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der, Headers: map[string]string{"extra": "value"}}), false},
		{"two keys", append(bytes.Clone(valid), valid...), false},
		{"prefix", append([]byte("junk\n"), valid...), false},
		{"suffix", append(bytes.Clone(valid), []byte("junk")...), false},
		{"skipped malformed block", append([]byte("-----BEGIN PUBLIC KEY-----\ninvalid\n-----END PUBLIC KEY-----\n"), valid...), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeLocalPublicKey(tc.raw)
			if tc.pass {
				if err != nil || !bytes.Equal(got, valid) {
					t.Fatalf("normalized key mismatch: %v", err)
				}
				return
			}
			var coded *e.Error
			if got != nil || !errors.As(err, &coded) || coded.Code != e.CodeTrustPolicyMissing {
				t.Fatalf("unexpected admission: %v", err)
			}
		})
	}
}
