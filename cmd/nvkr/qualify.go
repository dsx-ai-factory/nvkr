// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
	"github.com/urfave/cli/v2"
)

type qualificationRunPublisher func(context.Context, string, lifecycle.QualificationRuntimeOptions) (*qualification.Publication, error)

func cmdRunQualify() *cli.Command {
	return cmdRunQualifyWithPublisher(qualification.PublishTriggeredRun)
}

func cmdRunQualifyWithPublisher(publish qualificationRunPublisher) *cli.Command {
	return &cli.Command{Name: "qualify", Usage: "qualify the authenticated triggering Run under independently selected policy", Flags: []cli.Flag{
		&cli.StringFlag{Name: "policy", Usage: "independently selected qualification policy YAML"},
		&cli.StringFlag{Name: "workspace", Value: ".nvkr/qualification", Usage: "private qualification workspace"},
	}, Action: func(c *cli.Context) error {
		if c.Args().Present() || strings.TrimSpace(c.String("policy")) == "" || strings.TrimSpace(c.String("workspace")) == "" {
			return e.New(e.CodeUsage, "qualify requires --policy, a non-empty workspace, and no positional arguments")
		}
		result, publishErr := publish(c.Context, c.String("policy"), qualificationRuntimeOptions(c))
		if result != nil {
			raw, err := canonical.Marshal(result)
			if err != nil {
				return errors.Join(publishErr, e.Wrap(e.CodeInternal, err, "encoding qualification diagnostics"))
			}
			if _, err := c.App.Writer.Write(append(raw, '\n')); err != nil {
				return errors.Join(publishErr, e.Wrap(e.CodeInternal, err, "writing qualification diagnostics"))
			}
		}
		return publishErr
	}}
}
