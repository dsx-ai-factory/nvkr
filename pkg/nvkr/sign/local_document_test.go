// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestLocalCosignAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, binary, digest string
		code                 e.Code
	}{
		{"missing executable", "", "sha256:" + strings.Repeat("1", 64), e.CodeUnpinnedInput},
		{"missing pin", "cosign", "", e.CodeUnpinnedInput},
		{"invalid pin", "cosign", "latest", e.CodeUnpinnedInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			signer, err := NewLocalCosign(tc.binary, tc.digest)
			var coded *e.Error
			if signer != nil || !errors.As(err, &coded) || coded.Code != tc.code {
				t.Fatalf("signer=%v error=%v; want %s", signer, err, tc.code)
			}
		})
	}
	if runtime.GOOS != "linux" {
		_, err := NewLocalCosign("missing", "sha256:"+strings.Repeat("1", 64))
		var coded *e.Error
		if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
			t.Fatalf("unsupported platform performed executable lookup: %v", err)
		}
	}
}

func TestLocalSignatureDoesNotAcceptDescriptionsOrCanceledUse(t *testing.T) {
	var receipt LocalSignedDocument
	if _, err := receipt.Signature(); err == nil {
		t.Fatal("zero-value receipt supplied a signature")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := receipt.RequireFor(ctx, []byte("data"), []byte("key")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled receipt use: %v", err)
	}
}
