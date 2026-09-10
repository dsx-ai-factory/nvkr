package main

import (
	"encoding/hex"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sign"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/trust"
	"github.com/urfave/cli/v2"
)

func exactSHA256Pin(value string) bool {
	const prefix = "sha256:"
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil
}

// qualifiedPolicyWithTrustedRoot binds an operator-supplied trust-anchor pin into
// the immutable role policy. SignatureMaterial carries only observed paths and can
// never select or replace this authority.
func qualifiedPolicyWithTrustedRoot(policy trust.RolePolicy, rootPath, rootDigest string, offline bool) (trust.RolePolicy, error) {
	rootPath = strings.TrimSpace(rootPath)
	rootDigest = strings.TrimSpace(rootDigest)
	if rootPath == "" {
		if offline {
			return trust.RolePolicy{}, e.New(e.CodeUnpinnedInput,
				"qualified offline verification requires an exact trusted-root path and sha256 policy pin")
		}
		if rootDigest != "" {
			return trust.RolePolicy{}, e.New(e.CodeUnpinnedInput,
				"trusted-root digest policy was configured without a trusted-root path")
		}
		return policy, nil
	}
	if !exactSHA256Pin(rootDigest) {
		return trust.RolePolicy{}, e.New(e.CodeUnpinnedInput,
			"operator-selected trusted root requires an exact lowercase sha256 policy pin")
	}
	return policy.WithTrustedRootDigest(rootDigest)
}

func qualifiedDocumentVerifierFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:  "cosign",
			Value: sign.DefaultBinary,
			Usage: "static Linux cosign verifier executable to resolve and execute as a sealed held capability",
		},
		&cli.StringFlag{
			Name:  "cosign-digest",
			Usage: "required exact lowercase sha256 digest of the cosign verifier executable",
		},
	}
}

func qualifiedDocumentSignerFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:  "cosign",
			Value: sign.DefaultBinary,
			Usage: "static Linux cosign signer executable to resolve and execute as a sealed held capability",
		},
		&cli.StringFlag{
			Name:  "cosign-digest",
			Usage: "required exact lowercase sha256 digest of the cosign signer executable",
		},
		&cli.StringFlag{
			Name:  "signer-identity",
			Usage: "required expected certificate identity for qualified keyless signing",
		},
		&cli.StringFlag{
			Name:  "signer-issuer",
			Usage: "required expected OIDC issuer for qualified keyless signing",
		},
	}
}

// resolveQualifiedDocumentVerifier keeps test/embedding injection explicit while
// making every stock qualified command pin the exact verifier bytes. Platform
// capability is admitted here before any document, trust-store, or status-policy
// file is opened; Linux resolves and holds the executable later, immediately
// before its exact bytes are used.
func resolveQualifiedDocumentVerifier(c *cli.Context, injected trust.DocumentVerifier) (trust.DocumentVerifier, error) {
	if injected != nil {
		return injected, nil
	}
	if c == nil {
		return nil, e.New(e.CodeUsage, "qualified document verification requires command context")
	}
	binary := strings.TrimSpace(c.String("cosign"))
	if binary == "" {
		binary = sign.DefaultBinary
	}
	return sign.NewPinnedCosignVerifier(binary, strings.TrimSpace(c.String("cosign-digest")))
}

func resolveQualifiedDocumentSigner(c *cli.Context) (*sign.CosignSigner, error) {
	if c == nil {
		return nil, e.New(e.CodeUsage, "qualified document signing requires command context")
	}
	binary := strings.TrimSpace(c.String("cosign"))
	if binary == "" {
		binary = sign.DefaultBinary
	}
	signer, err := sign.NewPinnedCosignSigner(binary, strings.TrimSpace(c.String("cosign-digest")))
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(c.String("signer-identity")) == "" || strings.TrimSpace(c.String("signer-issuer")) == "" {
		return nil, e.New(e.CodeTrustPolicyMissing,
			"qualified keyless signing requires --signer-identity and --signer-issuer")
	}
	return signer, nil
}

// admitQualifiedDocumentSigner fails missing/malformed producer authority before
// catalog, profile, declaration, build, or external-evidence I/O. SignDocument
// re-captures and rechecks the same executable pin immediately before use.
func admitQualifiedDocumentSigner(c *cli.Context) error {
	signer, err := resolveQualifiedDocumentSigner(c)
	if err != nil {
		return err
	}
	return signer.Preflight(c.Context)
}

// writeQualifiedSignedDocument signs the exact caller-supplied canonical bytes and
// only then publishes those same bytes plus the privately captured cosign outputs.
// No authority path asks cosign to reopen a mutable public destination.
func writeQualifiedSignedDocument(c *cli.Context, canonicalBytes []byte, out, bundlePath string) error {
	signer, err := resolveQualifiedDocumentSigner(c)
	if err != nil {
		return err
	}
	ctx := c.Context
	document := append([]byte(nil), canonicalBytes...)
	if err := signer.Preflight(ctx); err != nil {
		return err
	}
	publication := sign.PublicationRequest{
		Document: document, DocumentPath: out, BundlePath: bundlePath,
	}
	if err := sign.PreparePublication(ctx, publication); err != nil {
		return err
	}
	signed, err := signer.SignDocument(ctx, document,
		sign.QualifiedSignOptions{
			Bundle:           bundlePath != "",
			ExpectedIdentity: c.String("signer-identity"),
			ExpectedIssuer:   c.String("signer-issuer"),
		})
	if err != nil {
		return err
	}
	return signed.Publish(ctx, publication)
}
