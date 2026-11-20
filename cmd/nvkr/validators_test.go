// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/validation"
	"github.com/urfave/cli/v2"
)

func TestValidatorDiscoveryCommands(t *testing.T) {
	for _, operation := range []string{"list", "describe"} {
		t.Run(operation, func(t *testing.T) {
			var out bytes.Buffer
			app := &cli.App{Writer: &out, Commands: []*cli.Command{cmdValidators()}}
			args := []string{"nvkr", "validators", operation, "--validator-catalog", "../../inputs/testing/validator-catalog"}
			if operation == "describe" {
				args = append(args, "nvke/nvkectl/contract")
			}
			if err := app.RunContext(t.Context(), args); err != nil {
				t.Fatal(err)
			}
			if operation == "describe" {
				var r validation.Registration
				if err := json.Unmarshal(out.Bytes(), &r); err != nil || r.ID != "nvke/nvkectl/contract" {
					t.Fatalf("%s %v", out.Bytes(), err)
				}
			} else {
				var r []validation.Registration
				if err := json.Unmarshal(out.Bytes(), &r); err != nil || len(r) != 2 {
					t.Fatalf("%s %v", out.Bytes(), err)
				}
			}
		})
	}
	var out bytes.Buffer
	app := &cli.App{Writer: &out, Commands: []*cli.Command{cmdValidators()}}
	err := app.RunContext(t.Context(), []string{"nvkr", "validators", "describe", "--validator-catalog", "../../inputs/testing/validator-catalog", "unknown"})
	assertLifecycleCommandCode(t, err, e.CodeUnsupported)
	if out.Len() != 0 {
		t.Fatal("refusal emitted metadata")
	}
}

// The shipped catalog is the default, resolved against the working directory.
func TestValidatorDiscoveryDefaultsToShippedCatalog(t *testing.T) {
	t.Chdir("../..")
	var out bytes.Buffer
	app := &cli.App{Writer: &out, Commands: []*cli.Command{cmdValidators()}}
	if err := app.RunContext(t.Context(), []string{"nvkr", "validators", "list"}); err != nil {
		t.Fatal(err)
	}
	var r []validation.Registration
	if err := json.Unmarshal(out.Bytes(), &r); err != nil || len(r) != 2 {
		t.Fatalf("%s %v", out.Bytes(), err)
	}
	t.Chdir(t.TempDir())
	out.Reset()
	err := app.RunContext(t.Context(), []string{"nvkr", "validators", "list"})
	assertLifecycleCommandCode(t, err, e.CodeUsage)
	if out.Len() != 0 {
		t.Fatal("refusal emitted metadata")
	}
}

func TestValidatorCommandRefusals(t *testing.T) {
	catalog := "../../inputs/testing/validator-catalog"
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"list-extra", []string{"list", "--validator-catalog", catalog, "extra"}},
		{"describe-missing-id", []string{"describe", "--validator-catalog", catalog}},
		{"describe-extra-id", []string{"describe", "--validator-catalog", catalog, "a", "b"}},
		{"run-extra", []string{"run", "--profile", "missing.yaml", "extra"}},
		{"run-missing-profile", []string{"run", "--profile", filepath.Join(t.TempDir(), "missing.yaml")}},
		{"list-empty-catalog", []string{"list", "--validator-catalog", ""}},
		{"list-missing-catalog", []string{"list", "--validator-catalog", filepath.Join(t.TempDir(), "missing")}},
		{"describe-empty-catalog", []string{"describe", "--validator-catalog", "", "nvke/nvkectl/contract"}},
		{"describe-no-id-no-catalog", []string{"describe"}},
		{"run-empty-profile", []string{"run", "--profile", ""}},
		{"run-no-profile", []string{"run"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			app := &cli.App{Writer: &out, ErrWriter: &out, Commands: []*cli.Command{cmdValidators()}}
			err := app.RunContext(t.Context(), append([]string{"nvkr", "validators"}, tc.args...))
			assertLifecycleCommandCode(t, err, e.CodeUsage)
			if out.Len() != 0 {
				t.Fatal("refusal emitted output", out.String())
			}
		})
	}
	// The index-file flag was replaced; the root app maps unknown flags to usage errors.
	var out bytes.Buffer
	app := newApp()
	app.Writer, app.ErrWriter = &out, &out
	err := app.RunContext(t.Context(), []string{"nvkr", "validators", "list", "--catalog", "../../inputs/testing/validators/catalog.yaml"})
	assertLifecycleCommandCode(t, err, e.CodeUsage)
}

type failedValidatorWriter struct{}

func (failedValidatorWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestValidatorDiscoveryWriteFailure(t *testing.T) {
	app := &cli.App{Writer: failedValidatorWriter{}, Commands: []*cli.Command{cmdValidators()}}
	err := app.RunContext(t.Context(), []string{"nvkr", "validators", "list", "--validator-catalog", "../../inputs/testing/validator-catalog"})
	assertLifecycleCommandCode(t, err, e.CodeInternal)
}

func TestValidatorsRegisteredHiddenInRootApp(t *testing.T) {
	var out bytes.Buffer
	app := newApp()
	app.Writer = &out
	if err := app.RunContext(t.Context(), []string{"nvkr", "validators", "list", "--validator-catalog", "../../inputs/testing/validator-catalog"}); err != nil {
		t.Fatal(err)
	}
	var d []validation.Registration
	if err := json.Unmarshal(out.Bytes(), &d); err != nil || len(d) != 2 {
		t.Fatalf("%s %v", out.Bytes(), err)
	}
	for _, c := range newApp().Commands {
		if c.Name == "validators" {
			if !c.Hidden {
				t.Fatal("validators must stay hidden while in development")
			}
			return
		}
	}
	t.Fatal("validators is not registered in the root app")
}
