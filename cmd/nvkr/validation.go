// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	client "github.com/dsx-ai-factory/nvkr/client/v1"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
	"github.com/urfave/cli/v2"
)

type validationRunner func(context.Context, string, string, lifecycle.QualificationRuntimeOptions) ([]testresults.Output, error)

func cmdValidate() *cli.Command { return cmdValidateWithRunner(client.New().Validate) }
func cmdValidateWithRunner(run validationRunner) *cli.Command {
	return &cli.Command{Name: "validate", ArgsUsage: "[check-name]", Usage: "run configured diagnostic checks against a published source or complete local Run; retain original reports without qualification signing", Flags: []cli.Flag{
		&cli.StringFlag{Name: "config", Usage: "independently selected validation profile JSON"},
		&cli.StringFlag{Name: "workspace", Value: ".nvkr/validation", Usage: "private validation workspace"},
	}, Action: func(c *cli.Context) error {
		if c.NArg() > 1 || strings.TrimSpace(c.String("config")) == "" || strings.TrimSpace(c.String("workspace")) == "" || c.NArg() == 1 && strings.TrimSpace(c.Args().First()) == "" {
			return e.New(e.CodeUsage, "validate requires --config, a non-empty workspace, and at most one check name")
		}
		options := qualificationRuntimeOptions(c)
		if options.Credential.Registry == "" && options.Registry.Username == "" && options.Registry.Password == "" {
			options.Registry.Anonymous = true
		}
		outputs, runErr := run(c.Context, c.Args().First(), c.String("config"), options)
		if len(outputs) == 0 {
			return runErr
		}
		documents := make([]json.RawMessage, 0, len(outputs))
		for _, output := range outputs {
			raw, err := testresults.Encode(output)
			if err != nil {
				return errors.Join(runErr, e.Wrap(e.CodeInternal, err, "encoding validation output description"))
			}
			documents = append(documents, raw)
		}
		raw, err := canonical.Marshal(documents)
		if err != nil {
			return errors.Join(runErr, e.Wrap(e.CodeInternal, err, "encoding validation diagnostics"))
		}
		raw = append(raw, '\n')
		n, err := c.App.Writer.Write(raw)
		if err == nil && n != len(raw) {
			err = io.ErrShortWrite
		}
		if err != nil {
			return errors.Join(runErr, e.Wrap(e.CodeInternal, err, "writing validation output descriptions"))
		}
		return runErr
	}}
}
