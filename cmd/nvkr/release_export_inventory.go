// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/releasemanifest"
	"github.com/urfave/cli/v2"
)

func releaseExportInventoryCmd() *cli.Command {
	return &cli.Command{
		Name: "export-inventory", Usage: "authenticate an explicit release manifest and export AICR chart/image identities (Linux)",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "reference", Required: true, Usage: "exact release repository@sha256:digest"},
			&cli.StringFlag{Name: "manifest", Required: true, Usage: "canonical release-manifest predicate file"},
			&cli.StringFlag{Name: "identity", Required: true, Usage: "publisher certificate identity regexp"},
			&cli.StringFlag{Name: "issuer", Required: true, Usage: "publisher OIDC issuer"},
			&cli.StringFlag{Name: "qualifier-identity", Required: true, Usage: "independently trusted exact GitHub qualifier certificate identity"},
			&cli.StringFlag{Name: "qualification-policy-digest", Required: true, Usage: "independently trusted qualification policy digest"},
			&cli.StringFlag{Name: "cosign", Required: true}, &cli.StringFlag{Name: "cosign-digest", Required: true},
			&cli.StringFlag{Name: "platform", Usage: "select linux/amd64 or linux/arm64 images; default retains every distinct entry"},
			&cli.StringFlag{Name: "minimum-disk-evidence", Usage: "policy floor for the complete source publication"},
			&cli.DurationFlag{Name: "timeout", Value: time.Hour},
			&cli.BoolFlag{Name: "anonymous"}, &cli.BoolFlag{Name: "plain-http"}, &cli.BoolFlag{Name: "no-cache"},
		},
		Action: func(c *cli.Context) error {
			if c.NArg() != 0 || c.Duration("timeout") <= 0 {
				return e.New(e.CodeUsage, "inventory export takes flags only and a positive timeout")
			}
			options := releasemanifest.InventoryOptions{
				Consumer: lifecycle.ConsumerOptions{Timeout: c.Duration("timeout"), CosignBinary: c.String("cosign"), CosignDigest: c.String("cosign-digest"),
					Registry: delivery.RegistryOptions{Anonymous: c.Bool("anonymous"), PlainHTTP: c.Bool("plain-http")}, DisableMetadataCache: c.Bool("no-cache")},
				Publisher: lifecycle.VerificationPolicy{Lane: lifecycle.LanePipeline, Scope: lifecycle.ScopeAuthenticity, IdentityRegexp: c.String("identity"), Issuer: c.String("issuer"), MinimumDiskEvidence: lifecycle.DiskEvidenceRequirement(c.String("minimum-disk-evidence"))},
				Qualifier: releasemanifest.Policy{QualificationIdentity: c.String("qualifier-identity"), QualificationPolicyDigest: c.String("qualification-policy-digest")}, Platform: c.String("platform"),
			}
			exporter, err := releasemanifest.NewInventoryExporter(c.String("reference"), options)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(c.Context, c.Duration("timeout"))
			defer cancel()
			var manifest json.RawMessage
			if err := readCanonicalJSONContext(ctx, c.String("manifest"), &manifest); err != nil {
				return err
			}
			raw, err := exporter.Export(ctx, manifest)
			if err != nil {
				return err
			}
			written, err := c.App.Writer.Write(raw)
			if err == nil && written != len(raw) {
				err = io.ErrShortWrite
			}
			if err != nil {
				return e.Wrap(e.CodeUnavailable, err, "writing AICR inventory")
			}
			return nil
		},
	}
}
