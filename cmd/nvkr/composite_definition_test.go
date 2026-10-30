// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/urfave/cli/v2"
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

// release verify dispatches --definition by kind for exact inputs: a composite
// reaches verification, and a member Definition cannot stand in for it.
func TestReleaseVerifyExactInputsAdmitComposites(t *testing.T) {
	_, source, _, _ := runtime.Caller(0)
	root := t.TempDir()
	t.Chdir(root)
	for _, name := range []string{"registry.yaml", "pins.lock.yaml"} {
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(source), "..", "..", "testdata", "catalogs", "kubernetes-layered", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll("catalog", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join("catalog", name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write := func(name string, value any) {
		raw, err := canonical.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	composite := definition.CompositeDefinition{APIVersion: definition.CompositeAPIVersion, Kind: definition.CompositeKind, Metadata: definition.Metadata{Name: "product"}}
	for _, member := range []struct{ name, component string }{{"alpha", "containerd"}, {"beta", "kubelet"}} {
		doc := definition.Definition{APIVersion: definition.DefinitionAPIVersion, Kind: definition.DefinitionKind, Metadata: definition.Metadata{Name: member.name},
			Catalog: definition.Catalog{Base: "../catalog"}, Outputs: []definition.DefinitionOutput{{Name: member.name + "-tools", Platform: "linux/amd64",
				Format: definition.OutputExecutable, Components: []string{member.component}}}}
		write("defs/"+member.name+".yaml", doc)
		digest, err := doc.Digest()
		if err != nil {
			t.Fatal(err)
		}
		composite.Members = append(composite.Members, definition.CompositeMember{Name: member.name, Definition: member.name + ".yaml", Digest: digest})
	}
	write("defs/composite.yaml", composite)
	write("defs/environment.yaml", definition.Environment{APIVersion: definition.EnvironmentAPIVersion, Kind: definition.EnvironmentKind,
		Metadata: definition.Metadata{Name: "fixture"}, UtilityPlatform: "linux/amd64",
		UtilityContainer: "golang@sha256:fe5d57d3b718e7a4986bae156c2d73f44973bfd313073aed08a4de6692bb6161"})
	plan, err := lifecycle.CompileFiles(t.Context(), "defs/composite.yaml", "defs/environment.yaml", "composite-test")
	if err != nil {
		t.Fatal(err)
	}
	write("plan.json", plan)

	for _, tc := range []struct {
		name, definition string
		code             e.Code
	}{
		{"composite", "defs/composite.yaml", e.CodeVerifyFailed},
		{"member against composite Plan", "defs/alpha.yaml", e.CodeUnverifiedRef},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			verify := func(_ context.Context, _ string, _ lifecycle.VerificationPolicy,
				_ lifecycle.ConsumerOptions, expectation *lifecycle.ExactInputExpectation, _ releaseExportOptions,
			) (releaseVerificationResult, error) {
				called = expectation != nil
				return releaseVerificationResult{}, e.New(e.CodeVerifyFailed, "fixture stops after exact-input preparation")
			}
			app := &cli.App{Commands: []*cli.Command{cmdReleaseWithVerify(verify)}}
			err := app.RunContext(t.Context(), []string{"nvkr", "release", "verify",
				"--reference", "example.test/release@sha256:" + strings.Repeat("a", 64),
				"--lane", "pipeline-attested", "--scope", "artifact-publication", "--identity", "^builder$", "--issuer", "issuer",
				"--definition", tc.definition, "--environment", "defs/environment.yaml", "--plan", "plan.json"})
			if got := cliErrorCode(err); got != tc.code || called != (tc.code == e.CodeVerifyFailed) {
				t.Fatalf("release verify = %s (called=%v): %v", got, called, err)
			}
		})
	}
}
