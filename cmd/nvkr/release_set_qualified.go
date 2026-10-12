// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/release"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/trust"
	"github.com/urfave/cli/v2"
)

// releaseSetCmd authenticates the exact normalized ReleaseSet bytes under the
// dedicated release-set role. Document authentication grants no release clearance.
func releaseSetCmd() *cli.Command {
	return releaseSetCmdWithVerifier(nil)
}

func releaseSetCmdWithVerifier(verifier trust.DocumentVerifier) *cli.Command {
	return &cli.Command{
		Name:  "verify-set",
		Usage: "authenticate canonical ReleaseSet bytes under the release-set signer role",
		Flags: append([]cli.Flag{
			&cli.StringFlag{Name: "set", Required: true, Usage: "normalized canonical ReleaseSet JSON"},
			&cli.StringFlag{Name: "signature", Usage: "detached ReleaseSet signature"},
			&cli.StringFlag{Name: "certificate", Required: true, Usage: "ReleaseSet signing certificate"},
			&cli.StringFlag{Name: "bundle", Usage: "ReleaseSet Sigstore bundle"},
			&cli.StringFlag{Name: "trusted-root", Usage: "explicit offline ReleaseSet trusted root"},
			&cli.StringFlag{Name: "trusted-root-digest", Usage: "exact lowercase sha256 digest of the ReleaseSet trusted root"},
			&cli.StringSliceFlag{Name: "release-set-identity", Usage: "authorized release-set identity (repeatable; overrides trust store)"},
			&cli.StringFlag{Name: "release-set-issuer", Required: true, Usage: "exact release-set OIDC issuer"},
			&cli.StringFlag{Name: "trust-store", Usage: "role-separated operator trust store (default .nvkr/trust.json)"},
			&cli.BoolFlag{Name: "offline", Usage: "require a local bundle and explicit trusted root"},
		}, qualifiedDocumentVerifierFlags()...),
		Action: func(c *cli.Context) error {
			return authenticateReleaseSet(c, verifier)
		},
	}
}

func authenticateReleaseSet(c *cli.Context, verifier trust.DocumentVerifier) error {
	documentVerifier, err := resolveQualifiedDocumentVerifier(c, verifier)
	if err != nil {
		return err
	}
	material := trust.SignatureMaterial{
		SignaturePath: c.String("signature"), CertificatePath: c.String("certificate"),
		BundlePath: c.String("bundle"), TrustedRootPath: c.String("trusted-root"),
	}
	if err := validateQualifiedMaterial("release set", material, c.Bool("offline")); err != nil {
		return err
	}
	identities, err := releaseSetIdentities(c)
	if err != nil {
		return err
	}
	policy, err := trust.NewRolePolicy(trust.RoleReleaseSet,
		trust.Store{CLIIdentities: identities}, c.String("release-set-issuer"))
	if err != nil {
		return err
	}
	policy, err = qualifiedPolicyWithTrustedRoot(policy,
		c.String("trusted-root"), c.String("trusted-root-digest"), c.Bool("offline"))
	if err != nil {
		return err
	}
	authenticator, err := trust.NewDocumentAuthenticator(documentVerifier,
		trust.JSONSchema[release.ReleaseSet](release.ReleaseSetSchemaVersion))
	if err != nil {
		return err
	}
	document, err := authenticator.Authenticate(c.Context, trust.AuthenticateRequest{
		SourcePath: c.String("set"), Signature: material, Offline: c.Bool("offline"),
		Role: trust.RoleReleaseSet, Policy: policy,
	})
	if err != nil {
		return err
	}
	if err := release.VerifyAuthenticatedReleaseSet(c.Context, document); err != nil {
		return err
	}
	set, ok := document.Decoded().(release.ReleaseSet)
	if !ok {
		return e.New(e.CodeSignatureInvalid, "authenticated release set has an unexpected decoded type")
	}
	digest, err := set.Digest()
	if err != nil {
		return err
	}
	fmt.Printf("AUTHENTICATED release set %s (signer %s, role %s)\n",
		digest, document.SignerIdentity(), document.Role())
	return nil
}

func releaseSetIdentities(c *cli.Context) ([]string, error) {
	identities := append([]string(nil), c.StringSlice("release-set-identity")...)
	if len(identities) == 0 {
		store, err := loadTrustStoreFile(c.Context, trustStorePath(c))
		if err != nil {
			return nil, err
		}
		identities = append(identities, store.ReleaseSetIdentities...)
	}
	return normalizedRoleIdentities(identities, "release set")
}
