// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/cistore"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/urfave/cli/v2"
)

func cmdFactoryCIUpload() *cli.Command {
	return &cli.Command{
		Name: "ci-upload", Hidden: true, Usage: "transfer validated metadata to Actions or an exact payload to private storage",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "name"},
			&cli.StringFlag{Name: "plan"},
			&cli.StringFlag{Name: "path"},
			&cli.StringFlag{Name: "out"},
			&cli.StringFlag{Name: "if-no-files-found", Value: "error"},
			&cli.BoolFlag{Name: "include-hidden-files"},
		},
		Action: func(c *cli.Context) error {
			if c.Args().Present() || c.String("name") == "" || c.String("path") == "" || c.String("out") == "" {
				return e.New(e.CodeUsage, "ci-upload requires --name, --path and --out without positional arguments")
			}
			request, options, err := factoryCIContext()
			if err != nil {
				return err
			}
			request.Name = c.String("name")
			if err := cistore.ValidateGitHubRequest(c.Context, request); err != nil {
				return err
			}
			output, err := cistore.OpenGitHubOutput(c.Context, os.Getenv("GITHUB_OUTPUT"))
			if err != nil {
				return err
			}
			defer func() { _ = output.Close() }()
			input := cistore.GitHubUpload{
				Request: request, Options: options, Root: ".", Paths: c.String("path"),
				OutputDirectory: c.String("out"), IfNoFilesFound: c.String("if-no-files-found"),
				IncludeHiddenFiles: c.Bool("include-hidden-files"),
			}
			var path string
			if c.String("plan") != "" {
				plan, loadErr := factory.LoadPlanContext(c.Context, c.String("plan"))
				if loadErr != nil {
					return loadErr
				}
				path, err = factory.UploadGitHubCarrier(c.Context, plan, input)
			} else {
				if err := cistore.ValidateGitHubTransfer(c.Context, request, options); err != nil {
					return err
				}
				path, err = cistore.UploadGitHub(c.Context, input)
			}
			if err != nil {
				return err
			}
			writeErr := cistore.WriteLocatorOutput(output, path)
			closeErr := output.Close()
			if writeErr != nil {
				return writeErr
			}
			if closeErr != nil {
				return e.Wrap(e.CodeInternal, closeErr, "close private CI transfer step output")
			}
			return nil
		},
	}
}

func cmdFactoryCIFetchLocators() *cli.Command {
	return &cli.Command{
		Name: "ci-fetch-locators", Hidden: true, Usage: "fetch exact provider locator records with bounded serial requests",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "index-dir"}, &cli.StringFlag{Name: "name"}, &cli.StringFlag{Name: "pattern"},
			&cli.BoolFlag{Name: "merge-multiple"}, &cli.StringFlag{Name: "run-id"}, &cli.StringFlag{Name: "attempt"},
			&cli.BoolFlag{Name: "allow-earlier-attempts"},
		},
		Action: func(c *cli.Context) error {
			if c.Args().Present() || c.String("index-dir") == "" {
				return e.New(e.CodeUsage, "ci-fetch-locators requires --index-dir without positional arguments")
			}
			if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("GITHUB_SERVER_URL") != "https://github.com" {
				return e.New(e.CodeEngineCapability, "CI locator acquisition requires the GitHub runner context")
			}
			if (c.String("run-id") == "") != (c.String("attempt") == "") {
				return e.New(e.CodeUsage, "cross-Run CI locator acquisition requires both --run-id and --attempt")
			}
			if c.Bool("allow-earlier-attempts") && c.String("run-id") != "" {
				return e.New(e.CodeUsage, "earlier-attempt CI reuse requires the current Run context")
			}
			request := cistore.Request{Repository: os.Getenv("GITHUB_REPOSITORY"), RunID: os.Getenv("GITHUB_RUN_ID"), Attempt: os.Getenv("GITHUB_RUN_ATTEMPT")}
			if c.String("run-id") != "" {
				request.RunID, request.Attempt = c.String("run-id"), c.String("attempt")
			}
			return cistore.FetchGitHubLocators(c.Context, request, c.String("index-dir"), cistore.GitHubLocatorSelection{
				Name: c.String("name"), Pattern: c.String("pattern"), MergeMultiple: c.Bool("merge-multiple"),
				AllowEarlierAttempts: c.Bool("allow-earlier-attempts"),
			}, os.Getenv("NVKR_CI_REPOSITORY_TOKEN"))
		},
	}
}

func cmdFactoryCIDownload() *cli.Command {
	return &cli.Command{
		Name: "ci-download", Hidden: true, Usage: "restore exact metadata and private carriers from provider records",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "index-dir"}, &cli.StringFlag{Name: "destination"},
			&cli.StringFlag{Name: "run-id"}, &cli.StringFlag{Name: "attempt"},
			&cli.StringFlag{Name: "plan"}, &cli.StringFlag{Name: "stage"},
			&cli.BoolFlag{Name: "allow-earlier-attempts"},
		},
		Action: func(c *cli.Context) error {
			if c.Args().Present() || c.String("index-dir") == "" || c.String("destination") == "" {
				return e.New(e.CodeUsage, "ci-download requires --index-dir and --destination without positional arguments")
			}
			if (c.String("plan") == "") != (c.String("stage") == "") {
				return e.New(e.CodeUsage, "streamed CI stage restoration requires both --plan and --stage")
			}
			if c.String("stage") != "" && (c.String("destination") != ".nvkr/carriers" || c.String("run-id") != "" || c.String("attempt") != "") {
				return e.New(e.CodeUsage, "streamed CI stage restoration requires current-Run carriers at the generated destination")
			}
			request, options, err := factoryCIContext()
			if err != nil {
				return err
			}
			if (c.String("run-id") == "") != (c.String("attempt") == "") {
				return e.New(e.CodeUsage, "cross-Run CI downloads require both --run-id and --attempt")
			}
			if c.Bool("allow-earlier-attempts") && c.String("run-id") != "" {
				return e.New(e.CodeUsage, "earlier-attempt CI reuse requires the current Run context")
			}
			options.AllowEarlierAttempts = c.Bool("allow-earlier-attempts")
			if c.String("run-id") != "" {
				request.RunID, request.Attempt = c.String("run-id"), c.String("attempt")
			}
			if c.String("stage") != "" {
				plan, err := factory.LoadPlanContext(c.Context, c.String("plan"))
				if err != nil {
					return err
				}
				return factory.RestoreGitHubPrivateInputs(c.Context, plan, c.String("stage"), request, c.String("index-dir"), options,
					factory.GitHubStepOptions{Stdout: os.Stdout, Stderr: os.Stderr})
			}
			return cistore.DownloadGitHub(c.Context, request, c.String("index-dir"), c.String("destination"), options)
		},
	}
}

func factoryCIContext() (cistore.Request, cistore.Options, error) {
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("GITHUB_SERVER_URL") != "https://github.com" {
		return cistore.Request{}, cistore.Options{}, e.New(e.CodeEngineCapability, "private CI transfer requires the GitHub runner context")
	}
	if strings.TrimSpace(os.Getenv("NVKR_CI_STORAGE_USER")) == "" {
		return cistore.Request{}, cistore.Options{}, e.New(e.CodeTrustPolicyMissing, "CI transfer requires its configured storage username")
	}
	return cistore.Request{Repository: os.Getenv("GITHUB_REPOSITORY"), RunID: os.Getenv("GITHUB_RUN_ID"), Attempt: os.Getenv("GITHUB_RUN_ATTEMPT")},
		cistore.Options{Token: os.Getenv("NVKR_CI_STORAGE_TOKEN"), RepositoryToken: os.Getenv("NVKR_CI_REPOSITORY_TOKEN"), Username: os.Getenv("NVKR_CI_STORAGE_USER")}, nil
}
