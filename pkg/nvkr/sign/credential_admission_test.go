// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"errors"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestCredentialPublicAdmissionDelegatesOwner(t *testing.T) {
	for _, tc := range []struct {
		name       string
		credential Credential
		code       e.Code
	}{
		{name: "empty"},
		{name: "complete", credential: Credential{Registry: "ghcr.io", Username: "user", Password: "private"}},
		{name: "missing registry", credential: Credential{Username: "user", Password: "private"}, code: e.CodeUsage},
		{name: "blank registry", credential: Credential{Registry: " ", Username: "user", Password: "private"}, code: e.CodeUsage},
		{name: "missing username", credential: Credential{Registry: "ghcr.io", Password: "private"}, code: e.CodeUsage},
		{name: "blank username", credential: Credential{Registry: "ghcr.io", Username: " ", Password: "private"}, code: e.CodeUsage},
		{name: "missing password", credential: Credential{Registry: "ghcr.io", Username: "user"}, code: e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.credential.Validate()
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != tc.code {
					t.Fatalf("expected code %s, got %v", tc.code, err)
				}
			}
		})
	}
}
