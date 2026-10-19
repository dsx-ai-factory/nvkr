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
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/releasemanifest"
	"github.com/urfave/cli/v2"
)

type qualificationRunPublisher func(context.Context, string, lifecycle.QualificationRuntimeOptions) (*qualification.Publication, error)

type qualificationManifestPublisher func(context.Context, string, lifecycle.QualificationRuntimeOptions) (*releasemanifest.RunResult, error)

func cmdRunQualify() *cli.Command {
	return cmdRunQualifyWithPublisher(qualification.PublishTriggeredRun)
}

func cmdRunQualifyWithPublisher(publish qualificationRunPublisher) *cli.Command {
	return cmdRunQualifyWithPublishers(publish, releasemanifest.PublishTriggeredRun)
}

func cmdRunQualifyWithPublishers(publish qualificationRunPublisher, issue qualificationManifestPublisher) *cli.Command {
	return &cli.Command{Name: "qualify", Usage: "qualify the authenticated triggering Run under independently selected policy", Flags: []cli.Flag{
		&cli.StringFlag{Name: "policy", Usage: "independently selected qualification policy YAML"},
		&cli.StringFlag{Name: "workspace", Value: ".nvkr/qualification", Usage: "private qualification workspace"},
		&cli.BoolFlag{Name: "issue-manifest", Usage: "authenticate complete production qualification and attest its release manifest (no release tag)"},
	}, Action: func(c *cli.Context) error {
		if c.Args().Present() || strings.TrimSpace(c.String("policy")) == "" || strings.TrimSpace(c.String("workspace")) == "" {
			return e.New(e.CodeUsage, "qualify requires --policy, a non-empty workspace, and no positional arguments")
		}
		var result any
		var publishErr error
		if c.Bool("issue-manifest") {
			publication, err := issue(c.Context, c.String("policy"), qualificationRuntimeOptions(c))
			publishErr = err
			if publication != nil {
				result = publication
			}
		} else {
			publication, err := publish(c.Context, c.String("policy"), qualificationRuntimeOptions(c))
			publishErr = err
			if publication != nil {
				result = publication
			}
		}
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
