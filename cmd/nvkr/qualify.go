// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
	"github.com/urfave/cli/v2"
)

type qualificationFilePublisher func(context.Context, qualification.FilesOptions) (*qualification.Publication, error)

func cmdRunQualify() *cli.Command { return cmdRunQualifyWithPublisher(qualification.PublishFiles) }

func cmdRunQualifyWithPublisher(publish qualificationFilePublisher) *cli.Command {
	return &cli.Command{Name: "qualify", Usage: "publish independently signed qualification evidence only after all required checks pass", Flags: append(qualificationFileFlags(),
		&cli.StringFlag{Name: "qualification-workflow", Required: true, Usage: "approved independent .github/workflows path for the active workflow_run invocation"},
		&cli.StringSliceFlag{Name: "external-suite", Usage: "named external suite explicitly not evaluated because it is outside MVP scope"},
		&cli.StringSliceFlag{Name: "required-suite", Usage: "required external suite; rejects qualification until authenticated integration is available"},
	), Action: func(c *cli.Context) error {
		files := qualificationFileOptions(c)
		result, publishErr := publish(c.Context, qualification.FilesOptions{ArtifactFiles: files, QualificationWorkflow: c.String("qualification-workflow"),
			Signer: qualification.Options{CosignBinary: files.Consumer.CosignBinary, CosignDigest: files.Consumer.CosignDigest, Credential: files.Consumer.Credential},
			Policy: qualification.Policy{OutsideMVPSuites: c.StringSlice("external-suite"), RequiredSuites: c.StringSlice("required-suite")}})
		if result != nil {
			raw, err := canonical.Marshal(result)
			if err != nil {
				return errors.Join(publishErr, e.Wrap(e.CodeInternal, err, "encoding qualification diagnostics"))
			}
			if _, err := c.App.Writer.Write(raw); err != nil {
				return errors.Join(publishErr, e.Wrap(e.CodeInternal, err, "writing qualification diagnostics"))
			}
		}
		return publishErr
	}}
}
