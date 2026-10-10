// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/release"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/trust"
	"github.com/urfave/cli/v2"
)

func releasePromotionPolicyCmd() *cli.Command {
	return releasePromotionPolicyCmdWithVerifier(nil)
}

func releasePromotionPolicyCmdWithVerifier(verifier trust.DocumentVerifier) *cli.Command {
	return &cli.Command{
		Name:  "policy",
		Usage: "authenticate and validate a canonical promotion policy",
		Flags: append([]cli.Flag{
			&cli.StringFlag{Name: "policy", Required: true, Usage: "canonical promotion-policy JSON"},
			&cli.StringFlag{Name: "signature", Usage: "detached promotion-policy signature"},
			&cli.StringFlag{Name: "certificate", Required: true, Usage: "promotion-policy signing certificate"},
			&cli.StringFlag{Name: "bundle", Usage: "promotion-policy Sigstore bundle"},
			&cli.StringFlag{Name: "trusted-root", Usage: "explicit offline promotion-policy trusted root"},
			&cli.StringFlag{Name: "trusted-root-digest", Usage: "exact lowercase sha256 digest of the promotion-policy trusted root"},
			&cli.StringSliceFlag{Name: "promotion-identity", Usage: "authorized promotion-policy identity (repeatable; overrides trust store)"},
			&cli.StringFlag{Name: "promotion-issuer", Required: true, Usage: "exact promotion-policy OIDC issuer"},
			&cli.StringFlag{Name: "trust-store", Usage: "role-separated operator trust store (default .nvkr/trust.json)"},
			&cli.BoolFlag{Name: "offline", Usage: "require a local bundle and explicit trusted root"},
		}, qualifiedDocumentVerifierFlags()...),
		Action: func(c *cli.Context) error {
			return authenticatePromotionPolicy(c, verifier)
		},
	}
}

func authenticatePromotionPolicy(c *cli.Context, verifier trust.DocumentVerifier) error {
	documentVerifier, err := resolveQualifiedDocumentVerifier(c, verifier)
	if err != nil {
		return err
	}
	material := trust.SignatureMaterial{
		SignaturePath: c.String("signature"), CertificatePath: c.String("certificate"),
		BundlePath: c.String("bundle"), TrustedRootPath: c.String("trusted-root"),
	}
	if err := validateQualifiedMaterial("promotion policy", material, c.Bool("offline")); err != nil {
		return err
	}
	identities, err := promotionPolicyIdentities(c)
	if err != nil {
		return err
	}
	policy, err := trust.NewRolePolicy(trust.RolePromotionPolicy,
		trust.Store{CLIIdentities: identities}, c.String("promotion-issuer"))
	if err != nil {
		return err
	}
	policy, err = qualifiedPolicyWithTrustedRoot(policy,
		c.String("trusted-root"), c.String("trusted-root-digest"), c.Bool("offline"))
	if err != nil {
		return err
	}
	authenticator, err := trust.NewDocumentAuthenticator(documentVerifier,
		trust.JSONSchema[release.PromotionPolicy](release.PromotionPolicySchemaVersion))
	if err != nil {
		return err
	}
	document, err := authenticator.Authenticate(c.Context, trust.AuthenticateRequest{
		SourcePath: c.String("policy"), Signature: material, Offline: c.Bool("offline"),
		Role: trust.RolePromotionPolicy, Policy: policy,
	})
	if err != nil {
		return err
	}
	digest, err := release.VerifyPromotionPolicy(c.Context, document)
	if err != nil {
		return err
	}
	fmt.Printf("AUTHENTICATED promotion policy %s (signer %s, role %s)\n",
		digest, document.SignerIdentity(), document.Role())
	return nil
}

func promotionPolicyIdentities(c *cli.Context) ([]string, error) {
	identities := append([]string(nil), c.StringSlice("promotion-identity")...)
	if len(identities) == 0 {
		store, err := loadTrustStoreFile(c.Context, trustStorePath(c))
		if err != nil {
			return nil, err
		}
		identities = append(identities, store.PromotionIdentities...)
	}
	return normalizedRoleIdentities(identities, "promotion policy")
}
