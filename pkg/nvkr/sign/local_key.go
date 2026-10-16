// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// NormalizeLocalPublicKey admits the P-256 public-key format used by the local
// cosign rehearsal. It removes PEM presentation differences so one key cannot
// impersonate distinct signer roles. This authenticates no signature or origin.
func NormalizeLocalPublicKey(raw []byte) ([]byte, error) {
	invalid := func() ([]byte, error) {
		return nil, e.New(e.CodeTrustPolicyMissing, "local rehearsal requires one bounded P-256 PUBLIC KEY PEM block")
	}
	if len(raw) == 0 || len(raw) > maxLocalKeyBytes {
		return invalid()
	}
	raw = bytes.TrimSpace(raw)
	if bytes.Count(raw, []byte("-----BEGIN ")) != 1 || bytes.Count(raw, []byte("-----END ")) != 1 {
		return invalid()
	}
	if !bytes.HasPrefix(raw, []byte("-----BEGIN PUBLIC KEY-----\n")) && !bytes.HasPrefix(raw, []byte("-----BEGIN PUBLIC KEY-----\r\n")) {
		return invalid()
	}
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "PUBLIC KEY" || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return invalid()
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return invalid()
	}
	key, ok := parsed.(*ecdsa.PublicKey)
	// ParsePKIXPublicKey validates the encoded point before returning the key.
	if !ok || key.Curve != elliptic.P256() {
		return invalid()
	}
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return invalid()
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}
