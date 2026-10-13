// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"

	client "github.com/dsx-ai-factory/nvkr/client/v1"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
	"github.com/urfave/cli/v2"
)

func cmdRunQualifyTests() *cli.Command {
	return &cli.Command{Name: "qualify-tests", Usage: "publish qualification v2 using complete artifact checks and authenticated required suites", Flags: append(qualificationFileFlags(), &cli.StringFlag{Name: "qualification-workflow", Required: true}, &cli.StringFlag{Name: "suite-config", Required: true, Usage: "independent canonical suite policy and digest-pinned transport coordinates"}), Action: func(c *cli.Context) error {
		config, err := qualification.LoadSuiteConfigurationV2(c.Context, c.String("suite-config"))
		if err != nil {
			return err
		}
		files := qualificationFileOptions(c)
		result, publishErr := client.New().QualifyWithSuites(c.Context, qualification.FilesOptionsV2{ArtifactFiles: files, QualificationWorkflow: c.String("qualification-workflow"), Signer: qualification.Options{CosignBinary: files.Consumer.CosignBinary, CosignDigest: files.Consumer.CosignDigest, Credential: files.Consumer.Credential}, Policy: config.Policy, Suites: config.Suites})
		if result != nil {
			return errors.Join(publishErr, evidenceJSON(c, result))
		}
		return publishErr
	}}
}
