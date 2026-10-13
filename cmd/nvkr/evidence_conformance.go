// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"

	client "github.com/dsx-ai-factory/nvkr/client/v1"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
	"github.com/urfave/cli/v2"
)

func cmdEvidenceConformance() *cli.Command {
	return &cli.Command{Name: "conformance", Usage: "explicitly run pinned upstream Kubernetes conformance on an existing test cluster", Flags: []cli.Flag{&cli.StringFlag{Name: "configuration", Required: true, Usage: "canonical private invocation configuration; no provisioning"}}, Action: func(c *cli.Context) error {
		result, runErr := client.New().RunConformance(c.Context, c.String("configuration"))
		if result == nil {
			return runErr
		}
		return errors.Join(runErr, evidenceJSON(c, struct {
			PrivateWorkspace    string                  `json:"privateWorkspace"`
			Execution           testobservation.Outcome `json:"execution"`
			Cleanup             testobservation.Outcome `json:"cleanup"`
			PublicBundleEmitted bool                    `json:"publicBundleEmitted"`
		}{PrivateWorkspace: result.PrivateWorkspace, Execution: result.Observation.Execution, Cleanup: result.Observation.Cleanup}))
	}}
}
