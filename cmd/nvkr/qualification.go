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
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"github.com/urfave/cli/v2"
)

func qualificationRuntimeOptions(c *cli.Context) lifecycle.QualificationRuntimeOptions {
	return lifecycle.QualificationRuntimeOptions{
		GitHubToken: os.Getenv("NVKR_GITHUB_TOKEN"), WorkspaceRoot: c.String("workspace"),
		CIStorageToken:    os.Getenv("NVKR_CI_STORAGE_TOKEN"),
		CIStorageUsername: os.Getenv("NVKR_CI_STORAGE_USER"),
		Credential:        sign.Credential{Registry: os.Getenv("NVKR_REGISTRY"), Username: os.Getenv("NVKR_REGISTRY_USER"), Password: os.Getenv("NVKR_REGISTRY_PASSWORD")},
		Registry:          delivery.RegistryOptions{Username: os.Getenv("NVKR_REGISTRY_USER"), Password: os.Getenv("NVKR_REGISTRY_PASSWORD")},
	}
}

type qualificationRunChecker func(context.Context, string, string, lifecycle.QualificationRuntimeOptions) (*lifecycle.ArtifactCheckReceipt, *lifecycle.ArtifactCheckDescription, error)

func cmdRunCheckArtifacts() *cli.Command {
	return cmdRunCheckArtifactsWithChecker(lifecycle.CheckQualificationRun)
}

func cmdRunCheckArtifactsWithChecker(check qualificationRunChecker) *cli.Command {
	return &cli.Command{Name: "check-artifacts", Usage: "authenticate the complete published artifact set against its producing Run; grants no signing authority",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "run", Usage: "exact producing GitHub Run URL, including /attempts/N"},
			&cli.StringFlag{Name: "policy", Usage: "independently selected qualification policy YAML"},
			&cli.StringFlag{Name: "workspace", Value: ".nvkr/qualification", Usage: "private publication materialization workspace"},
		}, Action: func(c *cli.Context) error {
			// Keep missing-value admission coded; cli.Required emits an uncoded error.
			if c.Args().Present() || strings.TrimSpace(c.String("run")) == "" || strings.TrimSpace(c.String("policy")) == "" || strings.TrimSpace(c.String("workspace")) == "" {
				return e.New(e.CodeUsage, "check-artifacts requires --run and --policy, a non-empty workspace, and no positional arguments")
			}
			receipt, report, checkErr := check(c.Context, c.String("run"), c.String("policy"), qualificationRuntimeOptions(c))
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

// The generated adapter emits a descriptive commit locator; later stages still
// reauthenticate the source and verify the original signed Plan and artifacts.
func cmdQualificationSource() *cli.Command {
	return &cli.Command{Name: "qualification-source", Hidden: true, Flags: []cli.Flag{&cli.StringFlag{Name: "policy"}}, Action: func(c *cli.Context) error {
		if c.Args().Present() || c.String("policy") == "" {
			return e.New(e.CodeUsage, "qualification-source requires --policy")
		}
		policy, err := lifecycle.LoadQualificationPolicy(c.Context, c.String("policy"))
		if err != nil {
			return err
		}
		provider := qualificationorigin.NewGitHub(os.Getenv("NVKR_GITHUB_TOKEN"))
		source, err := provider.AuthenticateTriggeredSource(c.Context, policy.Source)
		if err != nil {
			return err
		}
		defer func() { _ = source.Close() }()
		description, err := source.Description()
		if err != nil {
			return err
		}
		raw, err := canonical.Marshal(description)
		if err != nil {
			return err
		}
		if _, err := c.App.Writer.Write(append(raw, '\n')); err != nil {
			return e.Wrap(e.CodeInternal, err, "writing source description")
		}
		return nil
	}}
}
