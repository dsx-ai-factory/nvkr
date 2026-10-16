// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/cistore"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/urfave/cli/v2"
)

func cmdFactoryCIUpload() *cli.Command {
	return &cli.Command{
		Name: "ci-upload", Hidden: true, Usage: "transfer an exact CI carrier to admitted private storage",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "name"},
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
			if err := cistore.ValidateGitHubTransfer(c.Context, request, options); err != nil {
				return err
			}
			output, err := cistore.OpenGitHubOutput(c.Context, os.Getenv("GITHUB_OUTPUT"))
			if err != nil {
				return err
			}
			defer func() { _ = output.Close() }()
			path, err := cistore.UploadGitHub(c.Context, cistore.GitHubUpload{
				Request: request, Options: options, Root: ".", Paths: c.String("path"),
				OutputDirectory: c.String("out"), IfNoFilesFound: c.String("if-no-files-found"),
				IncludeHiddenFiles: c.Bool("include-hidden-files"),
			})
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

func cmdFactoryCIDownload() *cli.Command {
	return &cli.Command{
		Name: "ci-download", Hidden: true, Usage: "restore exact private CI carriers from provider locator records",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "index-dir"}, &cli.StringFlag{Name: "destination"},
			&cli.StringFlag{Name: "run-id"}, &cli.StringFlag{Name: "attempt"},
		},
		Action: func(c *cli.Context) error {
			if c.Args().Present() || c.String("index-dir") == "" || c.String("destination") == "" {
				return e.New(e.CodeUsage, "ci-download requires --index-dir and --destination without positional arguments")
			}
			request, options, err := factoryCIContext()
			if err != nil {
				return err
			}
			if (c.String("run-id") == "") != (c.String("attempt") == "") {
				return e.New(e.CodeUsage, "cross-Run CI downloads require both --run-id and --attempt")
			}
			if c.String("run-id") != "" {
				request.RunID, request.Attempt = c.String("run-id"), c.String("attempt")
			}
			return cistore.DownloadGitHub(c.Context, request, c.String("index-dir"), c.String("destination"), options)
		},
	}
}

func factoryCIContext() (cistore.Request, cistore.Options, error) {
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("GITHUB_SERVER_URL") != "https://github.com" {
		return cistore.Request{}, cistore.Options{}, e.New(e.CodeEngineCapability, "private CI transfer requires the GitHub runner context")
	}
	return cistore.Request{Repository: os.Getenv("GITHUB_REPOSITORY"), RunID: os.Getenv("GITHUB_RUN_ID"), Attempt: os.Getenv("GITHUB_RUN_ATTEMPT")},
		cistore.Options{Token: os.Getenv("NVKR_CI_STORAGE_TOKEN"), Username: os.Getenv("NVKR_CI_STORAGE_USER")}, nil
}
