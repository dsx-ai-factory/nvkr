// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"

	client "github.com/dsx-ai-factory/nvkr/client/v1"
	"github.com/urfave/cli/v2"
)

// cmdEvidenceReport is appended by the evidence command owner. Admission and
// local immutable output authority remain in testreporting, not the command.
func cmdEvidenceReport() *cli.Command {
	return &cli.Command{Name: "report", Usage: "authenticate historical observations and export local discovery/TestGrid artifacts", Flags: []cli.Flag{
		&cli.StringFlag{Name: "inputs", Required: true, Usage: "canonical independent locator/expectation/source-admission list"},
		&cli.StringFlag{Name: "cosign", Required: true, Usage: "exact pinned Linux cosign executable"},
		&cli.StringFlag{Name: "cosign-digest", Required: true, Usage: "independent SHA-256 executable pin"},
		&cli.BoolFlag{Name: "anonymous", Usage: "anonymous OCI transport"},
		&cli.BoolFlag{Name: "plain-http", Usage: "explicit loopback fixture transport"},
		&cli.StringFlag{Name: "format", Value: "json", Usage: "local report format: json or markdown"},
		&cli.StringFlag{Name: "out", Required: true, Usage: "new immutable local report file"},
		&cli.StringFlag{Name: "testgrid-target", Usage: "canonical explicit TestGrid bucket/prefix/source selection; export-only"},
		&cli.StringFlag{Name: "testgrid-out", Usage: "new local TestGrid artifact directory; no service upload"},
	}, Action: func(c *cli.Context) error {
		options := evidenceOptions(c)
		result, err := client.New().ReportEvidence(c.Context, c.String("inputs"), c.String("format"), c.String("out"), c.String("testgrid-target"), c.String("testgrid-out"), options)
		if result != nil {
			return errors.Join(err, evidenceJSON(c, result))
		}
		return err
	}}
}
