// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func TestDefinitionCommandsAdmitComposites(t *testing.T) {
	t.Chdir(t.TempDir())
	member := definition.Definition{APIVersion: definition.DefinitionAPIVersion, Kind: definition.DefinitionKind, Metadata: definition.Metadata{Name: "alpha"},
		Catalog: definition.Catalog{Base: "catalog"}, Outputs: []definition.DefinitionOutput{{Name: "cli", Platform: "linux/amd64", Format: definition.OutputExecutable, Components: []string{"cli"}}}}
	write := func(name string, value any) {
		raw, err := canonical.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("alpha.yaml", member)
	digest, err := member.Digest()
	if err != nil {
		t.Fatal(err)
	}
	composite := definition.CompositeDefinition{APIVersion: definition.CompositeAPIVersion, Kind: definition.CompositeKind,
		Metadata: definition.Metadata{Name: "product"}, Members: []definition.CompositeMember{{Name: "alpha", Definition: "alpha.yaml", Digest: digest}}}
	write("composite.yaml", composite)
	compositeDigest, err := composite.Digest()
	if err != nil {
		t.Fatal(err)
	}

	output, err := runCLIWithCapturedStdout(t, newApp(), "nvkr", "definition", "validate", "--definition", "composite.yaml")
	if err != nil || !strings.Contains(string(output), "composite definition: product\ndigest: "+compositeDigest+"\nmembers: 1\noutputs: 1\n") {
		t.Fatalf("validate = %q, %v", output, err)
	}
	output, err = runCLIWithCapturedStdout(t, newApp(), "nvkr", "definition", "inspect", "--definition", "composite.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var view lifecycle.CompositeView
	if err := canonical.UnmarshalExact(bytes.TrimSuffix(output, []byte("\n")), &view); err != nil || view.Digest != compositeDigest || view.Outputs[0].Member != "alpha" {
		t.Fatalf("inspect = %q, %v", output, err)
	}
	output, err = runCLIWithCapturedStdout(t, newApp(), "nvkr", "definition", "pin", "--definition", "composite.yaml", "--check")
	if err != nil || !strings.Contains(string(output), "member pins current: composite.yaml") {
		t.Fatalf("pin --check = %q, %v", output, err)
	}
	member.Metadata.Description = "edited"
	write("alpha.yaml", member)
	before, _ := os.ReadFile("composite.yaml")
	output, err = runCLIWithCapturedStdout(t, newApp(), "nvkr", "definition", "pin", "--definition", "composite.yaml", "--check")
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeUnverifiedRef || !strings.Contains(string(output), "alpha: "+digest+" -> ") {
		t.Fatalf("pin --check drift = %q, %v", output, err)
	}
	if after, _ := os.ReadFile("composite.yaml"); !bytes.Equal(before, after) {
		t.Fatal("pin --check wrote the composite")
	}
	if _, err = runCLIWithCapturedStdout(t, newApp(), "nvkr", "definition", "pin", "--definition", "composite.yaml"); err != nil {
		t.Fatal(err)
	}
	if _, err = runCLIWithCapturedStdout(t, newApp(), "nvkr", "definition", "validate", "--definition", "composite.yaml"); err != nil {
		t.Fatalf("validate after pin: %v", err)
	}
}
