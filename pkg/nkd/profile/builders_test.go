// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package profile

import (
	"testing"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestRequireBuilders(t *testing.T) {
	p := BuilderProfile{Identity: "op", Provides: []string{"oci-hermetic"}}
	if err := p.RequireBuilders([]string{"oci-hermetic"}); err != nil {
		t.Fatalf("oci-hermetic must be provided: %v", err)
	}
	if e, ok := p.RequireBuilders([]string{"oci-hermetic", "nix"}).(*nkderr.Error); !ok || e.Code != nkderr.CodeBuilderNotProvided {
		t.Fatal("a recipe needing nix on an oci-only profile must fail closed builder-not-provided")
	}
	nixProf := BuilderProfile{Identity: "op-nix", Provides: []string{"oci-hermetic", "nix"}}
	if err := nixProf.RequireBuilders([]string{"nix"}); err != nil {
		t.Fatalf("nix-providing profile must qualify: %v", err)
	}
	// default (no Provides) supplies oci-hermetic
	if err := (BuilderProfile{Identity: "d"}).RequireBuilders([]string{"oci-hermetic"}); err != nil {
		t.Fatalf("default profile must provide oci-hermetic: %v", err)
	}
}
