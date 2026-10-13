// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"strings"

	client "github.com/dsx-ai-factory/nvkr/client/v1"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
	"github.com/urfave/cli/v2"
)

type qualificationSuiteRunPublisher func(context.Context, string, string, lifecycle.QualificationRuntimeOptions) (*qualification.PublicationV3, error)

func cmdRunQualifyTests() *cli.Command {
	return cmdRunQualifyTestsWithPublisher(client.New().QualifyTriggeredWithSuites)
}
func cmdRunQualifyTestsWithPublisher(publish qualificationSuiteRunPublisher) *cli.Command {
	return &cli.Command{Name: "qualify-tests", Usage: "qualify the authenticated triggering Run with mandatory native smoke and required suites (v3)", Flags: []cli.Flag{
		&cli.StringFlag{Name: "policy", Usage: "independently selected native qualification policy YAML"},
		&cli.StringFlag{Name: "suite-config", Usage: "independently selected canonical required-suite policy and pinned transport"},
		&cli.StringFlag{Name: "workspace", Value: ".nvkr/qualification", Usage: "private qualification workspace"},
	}, Action: func(c *cli.Context) error {
		if c.Args().Present() || strings.TrimSpace(c.String("policy")) == "" || strings.TrimSpace(c.String("suite-config")) == "" || strings.TrimSpace(c.String("workspace")) == "" {
			return e.New(e.CodeUsage, "qualify-tests requires --policy, --suite-config, a non-empty workspace, and no positional arguments")
		}
		result, publishErr := publish(c.Context, c.String("policy"), c.String("suite-config"), qualificationRuntimeOptions(c))
		if result != nil {
			return errors.Join(publishErr, evidenceJSON(c, result))
		}
		return publishErr
	}}
}
