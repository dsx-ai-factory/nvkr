// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"

	client "github.com/dsx-ai-factory/nvkr/client/v1"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
	"github.com/urfave/cli/v2"
)

func evidenceRegistryFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "cosign", Required: true, Usage: "exact pinned Linux cosign executable"},
		&cli.StringFlag{Name: "cosign-digest", Required: true, Usage: "independent SHA-256 cosign executable pin"},
		&cli.StringFlag{Name: "identity", Required: true, Usage: "independently admitted exact certificate identity"},
		&cli.StringFlag{Name: "issuer", Required: true, Usage: "independently admitted OIDC issuer"},
		&cli.BoolFlag{Name: "anonymous", Usage: "anonymous OCI transport"},
		&cli.BoolFlag{Name: "plain-http", Usage: "explicit loopback fixture transport"},
	}
}
func evidenceOptions(c *cli.Context) testobservation.RegistryOptions {
	return testobservation.RegistryOptions{
		Transport:    delivery.RegistryOptions{Anonymous: c.Bool("anonymous"), PlainHTTP: c.Bool("plain-http")},
		CosignBinary: c.String("cosign"), CosignDigest: c.String("cosign-digest"), Identity: c.String("identity"), Issuer: c.String("issuer"),
	}
}

func cmdEvidence() *cli.Command {
	return &cli.Command{Name: "evidence", Usage: "emit, publish and independently verify portable test observations", Subcommands: []*cli.Command{
		{Name: "emit", Usage: "capture reviewed public result files without signing", Flags: []cli.Flag{
			&cli.StringFlag{Name: "root", Required: true}, &cli.StringFlag{Name: "description", Required: true, Usage: "canonical observation description with empty inventory and zero summary"},
			&cli.StringFlag{Name: "files", Required: true, Usage: "canonical explicit file selection with roles and exact raw pins"}, &cli.StringFlag{Name: "out", Required: true},
		}, Action: func(c *cli.Context) error {
			identity, err := client.New().EmitEvidence(c.Context, c.String("root"), c.String("description"), c.String("files"), c.String("out"))
			if err != nil {
				return err
			}
			return evidenceJSON(c, struct {
				ObservationDigest string `json:"observationDigest"`
			}{identity})
		}},
		{Name: "publish", Usage: "sign and publish exact recorded observations including failures", Flags: append(evidenceRegistryFlags(), &cli.StringFlag{Name: "root", Required: true}, &cli.StringFlag{Name: "repository", Required: true}), Action: func(c *cli.Context) error {
			result, publishErr := client.New().PublishEvidence(c.Context, c.String("root"), c.String("repository"), evidenceOptions(c))
			if result != nil {
				return errors.Join(publishErr, evidenceJSON(c, result))
			}
			return publishErr
		}},
		{Name: "verify-local", Usage: "check unsigned self-consistency against independent expectations", Flags: []cli.Flag{&cli.StringFlag{Name: "root", Required: true}, &cli.StringFlag{Name: "expected", Required: true}, &cli.BoolFlag{Name: "json"}}, Action: func(c *cli.Context) error {
			expected, err := testobservation.LoadExpected(c.Context, c.String("expected"))
			if err != nil {
				return err
			}
			result, err := testobservation.VerifyLocal(c.Context, c.String("root"), expected)
			if err != nil {
				return err
			}
			if c.Bool("json") {
				return evidenceJSON(c, result)
			}
			bundle, err := testobservation.Open(c.Context, c.String("root"))
			if err != nil {
				return err
			}
			defer func() { _ = bundle.Close() }()
			if err := expected.Check(bundle.Observation()); err != nil {
				return err
			}
			_, err = fmt.Fprint(c.App.Writer, testobservation.Render(bundle.Observation(), *result))
			if err != nil {
				return e.Wrap(e.CodeInternal, err, "writing evidence summary")
			}
			return nil
		}},
		{Name: "verify", Usage: "authenticate pinned OCI observations under independent expectations", Flags: append(evidenceRegistryFlags(), &cli.StringFlag{Name: "reference", Usage: "digest-pinned observation OCI reference"}, &cli.StringFlag{Name: "locator", Usage: "canonical locator file, not consumer trust"}, &cli.StringFlag{Name: "expected", Required: true}, &cli.BoolFlag{Name: "json"}), Action: func(c *cli.Context) error {
			expected, err := testobservation.LoadExpected(c.Context, c.String("expected"))
			if err != nil {
				return err
			}
			reference := c.String("reference")
			if (reference == "") == (c.String("locator") == "") {
				return e.New(e.CodeUsage, "provide exactly one reference or locator")
			}
			if c.String("locator") != "" {
				locator, err := testobservation.LoadLocator(c.Context, c.String("locator"))
				if err != nil {
					return err
				}
				if locator.ObservationDigest != expected.ObservationDigest {
					return e.New(e.CodeVerifyFailed, "locator differs from independent expectation")
				}
				reference = locator.Reference
			}
			verified, err := client.New().VerifyEvidence(c.Context, reference, expected, evidenceOptions(c))
			if err != nil {
				return err
			}
			defer func() { _ = verified.Close() }()
			result, err := verified.Description(c.Context)
			if err != nil {
				return err
			}
			observation, err := verified.Observation(c.Context)
			if err != nil {
				return err
			}
			if c.Bool("json") {
				return evidenceJSON(c, struct {
					Verification testobservation.Verification `json:"verification"`
					Observation  testobservation.Observation  `json:"observation"`
				}{result, observation})
			}
			_, err = fmt.Fprint(c.App.Writer, testobservation.Render(observation, result))
			if err != nil {
				return e.Wrap(e.CodeInternal, err, "writing evidence summary")
			}
			return nil
		}},
	}}
}

func evidenceJSON(c *cli.Context, value any) error {
	raw, err := canonical.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := c.App.Writer.Write(raw); err != nil {
		return e.Wrap(e.CodeInternal, err, "writing evidence result")
	}
	return nil
}
