// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package client_test

import (
	"errors"
	"os"
	"reflect"
	"testing"

	client "github.com/dsx-ai-factory/nvkr/client/v1"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func writeSDKDocument(t *testing.T, name string, value any) {
	t.Helper()
	raw, err := canonical.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSDKCompositeDefinitionReachesLifecycleOwner(t *testing.T) {
	t.Chdir(t.TempDir())
	member := definition.Definition{APIVersion: definition.DefinitionAPIVersion, Kind: definition.DefinitionKind, Metadata: definition.Metadata{Name: "alpha"},
		Catalog: definition.Catalog{Base: "catalog"}, Outputs: []definition.DefinitionOutput{{Name: "cli", Platform: "linux/amd64", Format: definition.OutputExecutable, Components: []string{"cli"}}}}
	writeSDKDocument(t, "alpha.yaml", member)
	digest, err := member.Digest()
	if err != nil {
		t.Fatal(err)
	}
	writeSDKDocument(t, "composite.yaml", definition.CompositeDefinition{APIVersion: definition.CompositeAPIVersion, Kind: definition.CompositeKind,
		Metadata: definition.Metadata{Name: "product"}, Members: []definition.CompositeMember{{Name: "alpha", Definition: "alpha.yaml", Digest: digest}}})
	sdk := client.New(client.WithRuntime("podman"))
	want, err := lifecycle.LoadDefinitionInput(t.Context(), "composite.yaml")
	if err != nil {
		t.Fatal(err)
	}
	got, err := sdk.LoadDefinitionInput(t.Context(), "composite.yaml")
	if err != nil || got.Digest != want.Digest || !reflect.DeepEqual(got.Definition, want.Definition) {
		t.Fatalf("SDK input = %+v, %v", got, err)
	}
	if _, err := sdk.LoadDefinition(t.Context(), "composite.yaml"); !hasCode(err, e.CodeEngineCapability) {
		t.Fatalf("plain-only SDK load admitted a composite: %v", err)
	}
	report, err := sdk.PinDefinition(t.Context(), "composite.yaml", true)
	if err != nil || len(report.Drift) != 0 || report.Written {
		t.Fatalf("SDK pin check = %+v, %v", report, err)
	}
}

func hasCode(err error, code e.Code) bool {
	var coded *e.Error
	return errors.As(err, &coded) && coded.Code == code
}
