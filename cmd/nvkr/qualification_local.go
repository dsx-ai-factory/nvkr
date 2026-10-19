// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/urfave/cli/v2"
)

type localQualifier func(context.Context, lifecycle.LocalQualificationOptions) (*lifecycle.LocalQualificationResult, error)

func cmdRunQualifyLocal() *cli.Command { return cmdRunQualifyLocalWith(lifecycle.QualifyLocalRun) }

func cmdRunQualifyLocalWith(qualify localQualifier) *cli.Command {
	return &cli.Command{Name: "qualify-local", Usage: "check, smoke, sign and publish a retained local Run with independent registry read-back; remains unqualified-local-execution",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "workspace", Usage: "completed local attempt with captured compiler inputs"},
			&cli.StringFlag{Name: "policy", Usage: "independent local qualification policy JSON"},
			&cli.StringFlag{Name: "builder-key", Usage: "explicit passwordless local builder private-key file"},
			&cli.StringFlag{Name: "smoke-key", Usage: "distinct local smoke private-key file"},
			&cli.StringFlag{Name: "qualifier-key", Usage: "distinct local qualifier private-key file"},
			&cli.StringFlag{Name: "cosign", Usage: "cosign executable pinned by the policy"},
			&cli.StringFlag{Name: "runtime", Value: "docker", Usage: "native isolated tool-smoke runtime"},
			&cli.StringFlag{Name: "repository", Usage: "canonical untagged registry namespace for this local rehearsal"},
			&cli.StringFlag{Name: "scratch", Usage: "existing disk-backed directory with capacity for export and read-back"},
			&cli.StringFlag{Name: "evidence-out", Usage: "new evidence-bundle file in an existing directory"},
			&cli.BoolFlag{Name: "plain-http", Usage: "explicit HTTP transport for a development registry"},
			&cli.BoolFlag{Name: "anonymous", Usage: "explicit anonymous registry access"},
		}, Action: func(c *cli.Context) error {
			if c.Args().Present() {
				return e.New(e.CodeUsage, "qualify-local accepts no positional arguments")
			}
			for _, flag := range []string{"workspace", "policy", "builder-key", "smoke-key", "qualifier-key", "cosign", "repository", "scratch", "evidence-out"} {
				if strings.TrimSpace(c.String(flag)) == "" {
					return e.New(e.CodeUsage, "qualify-local requires --%s", flag)
				}
			}
			result, qualifyErr := qualify(c.Context, lifecycle.LocalQualificationOptions{Workspace: c.String("workspace"), PolicyPath: c.String("policy"), BuilderKeyPath: c.String("builder-key"), SmokeKeyPath: c.String("smoke-key"), QualifierKeyPath: c.String("qualifier-key"), CosignExecutable: c.String("cosign"), RuntimeExecutable: c.String("runtime"), Repository: c.String("repository"), Scratch: c.String("scratch"), EvidenceOut: c.String("evidence-out"), Registry: delivery.RegistryOptions{PlainHTTP: c.Bool("plain-http"), Anonymous: c.Bool("anonymous"), Username: os.Getenv("NVKR_REGISTRY_USER"), Password: os.Getenv("NVKR_REGISTRY_PASSWORD")}})
			if result != nil {
				raw, err := canonical.Marshal(result)
				if err != nil {
					return e.Wrap(e.CodeInternal, err, "encoding local qualification result")
				}
				if _, err := c.App.Writer.Write(append(raw, '\n')); err != nil {
					return e.Wrap(e.CodeInternal, err, "writing local qualification result")
				}
			}
			return qualifyErr
		}}
}
