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
			args := []string{"nvkr", "validators", operation, "--catalog", "../../inputs/testing/validators/catalog.yaml"}
			if operation == "describe" {
				args = append(args, "nvke/nvkectl/contract")
			}
			if err := app.RunContext(t.Context(), args); err != nil {
				t.Fatal(err)
			}
			if operation == "describe" {
				var d validation.Description
				if err := json.Unmarshal(out.Bytes(), &d); err != nil || !d.Available || d.Validator.Suite != "contract" {
					t.Fatalf("%s %v", out.Bytes(), err)
				}
			} else {
				var d []validation.Description
				if err := json.Unmarshal(out.Bytes(), &d); err != nil || len(d) != 2 {
					t.Fatalf("%s %v", out.Bytes(), err)
				}
			}
		})
	}
	var out bytes.Buffer
	app := &cli.App{Writer: &out, Commands: []*cli.Command{cmdValidators()}}
	err := app.RunContext(t.Context(), []string{"nvkr", "validators", "describe", "--catalog", "../../inputs/testing/validators/catalog.yaml", "unknown"})
	assertLifecycleCommandCode(t, err, e.CodeUnsupported)
	if out.Len() != 0 {
		t.Fatal("refusal emitted metadata")
	}
}

func TestValidatorCommandRefusals(t *testing.T) {
	catalog := "../../inputs/testing/validators/catalog.yaml"
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"list-extra", []string{"list", "--catalog", catalog, "extra"}},
		{"describe-missing-id", []string{"describe", "--catalog", catalog}},
		{"describe-extra-id", []string{"describe", "--catalog", catalog, "a", "b"}},
		{"run-extra", []string{"run", "--profile", "missing.yaml", "extra"}},
		{"run-missing-profile", []string{"run", "--profile", filepath.Join(t.TempDir(), "missing.yaml")}},
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
}

type failedValidatorWriter struct{}

func (failedValidatorWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestValidatorDiscoveryWriteFailure(t *testing.T) {
	app := &cli.App{Writer: failedValidatorWriter{}, Commands: []*cli.Command{cmdValidators()}}
	err := app.RunContext(t.Context(), []string{"nvkr", "validators", "list", "--catalog", "../../inputs/testing/validators/catalog.yaml"})
	assertLifecycleCommandCode(t, err, e.CodeInternal)
}
