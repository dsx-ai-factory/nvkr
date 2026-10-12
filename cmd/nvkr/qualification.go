// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"github.com/urfave/cli/v2"
)

func qualificationFileFlags() []cli.Flag {
	return append(lifecycleInputFlags(),
		&cli.StringFlag{Name: "plan", Required: true, Usage: "original generated Run Plan"},
		&cli.StringFlag{Name: "candidate", Required: true, Usage: "original publication reference and exact production predicates"},
		&cli.StringFlag{Name: "source-repository", Required: true, Usage: "approved producing owner/repository"},
		&cli.StringFlag{Name: "source-run", Required: true},
		&cli.StringFlag{Name: "source-attempt", Required: true},
		&cli.StringFlag{Name: "source-commit", Required: true},
		&cli.StringFlag{Name: "source-workflow", Required: true, Usage: "approved producing .github/workflows path"},
		&cli.StringFlag{Name: "default-branch", Required: true},
		&cli.StringFlag{Name: "cosign", Required: true, Usage: "exact pinned cosign executable"},
		&cli.StringFlag{Name: "cosign-digest", Required: true},
		&cli.StringFlag{Name: "workspace", Value: ".nvkr/qualification", Usage: "private publication materialization workspace"},
	)
}

func qualificationFileOptions(c *cli.Context) lifecycle.QualificationFilesOptions {
	return lifecycle.QualificationFilesOptions{
		DefinitionPath: c.String("definition"), EnvironmentPath: c.String("environment"),
		PlanPath: c.String("plan"), CandidatePath: c.String("candidate"), GitHubToken: os.Getenv("NVKR_GITHUB_TOKEN"),
		Source: qualificationorigin.SourceRequest{Repository: c.String("source-repository"), RunID: c.String("source-run"),
			Attempt: c.String("source-attempt"), Commit: c.String("source-commit"), Workflow: c.String("source-workflow"), DefaultBranch: c.String("default-branch")},
		Consumer: lifecycle.ConsumerOptions{CosignBinary: c.String("cosign"), CosignDigest: c.String("cosign-digest"), WorkspaceRoot: c.String("workspace"),
			Credential: sign.Credential{Registry: os.Getenv("NVKR_REGISTRY"), Username: os.Getenv("NVKR_REGISTRY_USER"), Password: os.Getenv("NVKR_REGISTRY_PASSWORD")},
			Registry:   delivery.RegistryOptions{Username: os.Getenv("NVKR_REGISTRY_USER"), Password: os.Getenv("NVKR_REGISTRY_PASSWORD")}},
	}
}

func cmdRunCheckArtifacts() *cli.Command {
	return &cli.Command{Name: "check-artifacts", Usage: "authenticate the complete published artifact set against its producing Run; grants no signing authority",
		Flags: qualificationFileFlags(), Action: func(c *cli.Context) error {
			receipt, report, checkErr := lifecycle.CheckQualificationFiles(c.Context, qualificationFileOptions(c))
			if receipt != nil {
				defer func() { _ = receipt.Close() }()
			}
			if report != nil {
				raw, err := canonical.Marshal(report)
				if err != nil {
					return e.Wrap(e.CodeInternal, err, "encoding artifact check diagnostics")
				}
				if _, err := c.App.Writer.Write(append(raw, '\n')); err != nil {
					return e.Wrap(e.CodeInternal, err, "writing artifact check diagnostics")
				}
			}
			return checkErr
		}}
}
