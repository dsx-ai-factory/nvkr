// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"github.com/urfave/cli/v2"
)

type smokeWorkerRunner func(context.Context, string, lifecycle.QualificationRuntimeOptions) (*qualification.SmokeWorkerResult, error)

func cmdRunSmokeWorker() *cli.Command {
	return cmdRunSmokeWorkerWithRunner(qualification.RunTriggeredSmokeWorker)
}

func cmdRunSmokeWorkerWithRunner(run smokeWorkerRunner) *cli.Command {
	return &cli.Command{Name: "smoke-worker", Hidden: true, Usage: "run the generated native smoke worker", Flags: []cli.Flag{
		&cli.StringFlag{Name: "policy", Usage: "independently selected qualification policy YAML"},
	}, Action: func(c *cli.Context) error {
		if c.Args().Present() || strings.TrimSpace(c.String("policy")) == "" {
			return e.New(e.CodeUsage, "smoke-worker requires --policy and no positional arguments")
		}
		for _, name := range []string{"NVKR_REGISTRY", "NVKR_REGISTRY_USER", "NVKR_REGISTRY_PASSWORD"} {
			if os.Getenv(name) != "" {
				return e.New(e.CodeTrustPolicyMissing, "smoke-worker refuses publication environment; use scoped pull credentials")
			}
		}
		options := lifecycle.QualificationRuntimeOptions{WorkspaceRoot: qualification.SmokeWorkerWorkspace, GitHubToken: os.Getenv("NVKR_GITHUB_TOKEN"),
			CIStorageToken: os.Getenv("NVKR_CI_STORAGE_TOKEN"), CIStorageUsername: os.Getenv("NVKR_CI_STORAGE_USER"),
			Credential: sign.Credential{Registry: os.Getenv("NVKR_PULL_REGISTRY"), Username: os.Getenv("NVKR_PULL_REGISTRY_USER"), Password: os.Getenv("NVKR_PULL_REGISTRY_PASSWORD")},
			Registry:   delivery.RegistryOptions{Username: os.Getenv("NVKR_PULL_REGISTRY_USER"), Password: os.Getenv("NVKR_PULL_REGISTRY_PASSWORD")},
		}
		result, runErr := run(c.Context, c.String("policy"), options)
		if result != nil {
			raw, err := canonical.Marshal(result)
			if err != nil {
				return errors.Join(runErr, e.Wrap(e.CodeInternal, err, "encoding smoke worker diagnostics"))
			}
			if _, err := c.App.Writer.Write(append(raw, '\n')); err != nil {
				return errors.Join(runErr, e.Wrap(e.CodeInternal, err, "writing smoke worker diagnostics"))
			}
		}
		return runErr
	}}
}
