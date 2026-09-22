package main

import (
	"fmt"
	"path/filepath"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/status"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/trust"
	"github.com/urfave/cli/v2"
)

const (
	statusInputsSchemaVersion = "nkd.dev/status-verification-inputs/v1"
)

// signedDocumentLocation is transport configuration, not trust evidence. The
// document authenticator snapshots and verifies every referenced file before a
// path can become a trust.DocumentReceipt.
type signedDocumentLocation struct {
	Document    string `json:"document"`
	Signature   string `json:"signature,omitempty"`
	Certificate string `json:"certificate"`
	Bundle      string `json:"bundle,omitempty"`
	TrustedRoot string `json:"trustedRoot,omitempty"`
}

type statusVerificationInputs struct {
	SchemaVersion string                   `json:"schemaVersion"`
	Checkpoint    signedDocumentLocation   `json:"checkpoint"`
	Declarations  []signedDocumentLocation `json:"declarations"`
}

func releaseStatusCmd() *cli.Command {
	return releaseStatusCmdWithVerifier(nil)
}

func releaseStatusCmdWithVerifier(verifier trust.DocumentVerifier) *cli.Command {
	return &cli.Command{
		Name:  "status",
		Usage: "diagnostically authenticate a status checkpoint at a caller epoch and update its high-water mark (not release clearance)",
		Flags: append([]cli.Flag{
			&cli.StringFlag{Name: "target", Required: true, Usage: "exact publication digest governed by the checkpoint"},
			&cli.StringFlag{Name: "authority", Required: true, Usage: "stable status authority identity"},
			&cli.StringFlag{Name: "documents", Required: true, Usage: "canonical JSON locating the checkpoint and its exact declaration set"},
			&cli.StringFlag{Name: "high-water", Required: true, Usage: "canonical authenticated local high-water policy state"},
			&cli.StringFlag{Name: "high-water-out", Usage: "must equal --high-water when supplied; retained for compatibility; accepted state is updated in place"},
			&cli.Int64Flag{Name: "as-of", Required: true, Usage: "caller-supplied diagnostic policy epoch; not trusted-time authority"},
			&cli.StringSliceFlag{Name: "status-identity", Usage: "currently authorized status certificate identity (repeatable; overrides trust store)"},
			&cli.StringFlag{Name: "status-issuer", Required: true, Usage: "exact pinned status OIDC issuer"},
			&cli.StringFlag{Name: "trust-store", Usage: "role-separated operator trust store (default .nkd/trust.json)"},
			&cli.StringFlag{Name: "trusted-root-digest", Usage: "exact lowercase sha256 policy pin for every offline status trusted root"},
			&cli.BoolFlag{Name: "offline", Usage: "require a local Sigstore bundle and explicit trusted root for every status document"},
		}, qualifiedDocumentVerifierFlags()...),
		Action: func(c *cli.Context) error {
			return consumeQualifiedStatus(c, verifier)
		},
	}
}

func consumeQualifiedStatus(c *cli.Context, verifier trust.DocumentVerifier) error {
	documentVerifier, err := resolveQualifiedDocumentVerifier(c, verifier)
	if err != nil {
		return err
	}
	var inputs statusVerificationInputs
	if err := readCanonicalJSONContext(c.Context, c.String("documents"), &inputs); err != nil {
		return err
	}
	if inputs.SchemaVersion != statusInputsSchemaVersion {
		return e.New(e.CodeInvalidCatalog, "status input schemaVersion %q is unsupported (want %q)", inputs.SchemaVersion, statusInputsSchemaVersion)
	}
	if err := validateSignedDocumentLocation("status checkpoint", inputs.Checkpoint, c.Bool("offline")); err != nil {
		return err
	}
	for i, declaration := range inputs.Declarations {
		if err := validateSignedDocumentLocation(fmt.Sprintf("status declaration %d", i), declaration, c.Bool("offline")); err != nil {
			return err
		}
	}

	identities, err := qualifiedStatusIdentities(c)
	if err != nil {
		return err
	}
	rolePolicy, err := trust.NewRolePolicy(trust.RoleStatus, trust.Store{CLIIdentities: identities}, c.String("status-issuer"))
	if err != nil {
		return err
	}
	statusRootPath := inputs.Checkpoint.TrustedRoot
	for _, declaration := range inputs.Declarations {
		if statusRootPath == "" {
			statusRootPath = declaration.TrustedRoot
		}
	}
	rolePolicy, err = qualifiedPolicyWithTrustedRoot(rolePolicy,
		statusRootPath, c.String("trusted-root-digest"), c.Bool("offline"))
	if err != nil {
		return err
	}
	checkpointPolicy, err := status.NewCheckpointPolicy(c.String("target"), c.String("authority"), c.String("status-issuer"), identities)
	if err != nil {
		return err
	}
	asOf, err := status.At(c.Int64("as-of"))
	if err != nil {
		return err
	}
	path := c.String("high-water-out")
	if path != "" && filepath.Clean(path) != filepath.Clean(c.String("high-water")) {
		return e.New(e.CodeUsage, "--high-water-out must equal --high-water so monotonic state is updated with compare-and-swap")
	}
	declarations := make([]trust.AuthenticateRequest, 0, len(inputs.Declarations))
	for _, location := range inputs.Declarations {
		declarations = append(declarations, statusDocumentRequest(rolePolicy, location, c.Bool("offline")))
	}
	accepted, err := status.AcceptDiagnosticCheckpoint(c.Context, status.DiagnosticCheckpointRequest{
		HighWaterPath: c.String("high-water"), Policy: checkpointPolicy, AsOf: asOf,
		Checkpoint:   statusDocumentRequest(rolePolicy, inputs.Checkpoint, c.Bool("offline")),
		Declarations: declarations,
	}, documentVerifier)
	if err != nil {
		return err
	}
	decision := accepted.Decision
	digest, sequence := accepted.CheckpointDigest, accepted.Sequence
	fmt.Printf("UNQUALIFIED DIAGNOSTIC STATUS: checkpoint %s sequence %d was authenticated at caller-supplied epoch %d; no trusted-time binding or clearance authority was issued\n",
		digest, sequence, c.Int64("as-of"))
	if !decision.Effective {
		fmt.Println("no declaration is in effect at that diagnostic epoch")
		return nil
	}
	fmt.Printf("diagnostic decision: %s: %s\n", decision.Action, decision.Warn)
	return nil
}

func statusDocumentRequest(policy trust.RolePolicy, location signedDocumentLocation, offline bool) trust.AuthenticateRequest {
	return trust.AuthenticateRequest{
		SourcePath: location.Document,
		Signature: trust.SignatureMaterial{
			SignaturePath: location.Signature, CertificatePath: location.Certificate,
			BundlePath: location.Bundle, TrustedRootPath: location.TrustedRoot,
		},
		Offline: offline, Role: trust.RoleStatus, Policy: policy,
	}
}

func validateSignedDocumentLocation(label string, location signedDocumentLocation, offline bool) error {
	if strings.TrimSpace(location.Document) == "" {
		return e.New(e.CodeUsage, "%s document path is required", label)
	}
	return validateQualifiedMaterial(label, trust.SignatureMaterial{
		SignaturePath: location.Signature, CertificatePath: location.Certificate,
		BundlePath: location.Bundle, TrustedRootPath: location.TrustedRoot,
	}, offline)
}

func qualifiedStatusIdentities(c *cli.Context) ([]string, error) {
	identities := append([]string(nil), c.StringSlice("status-identity")...)
	if len(identities) == 0 {
		store, err := loadTrustStoreFile(c.Context, trustStorePath(c))
		if err != nil {
			return nil, err
		}
		identities = append(identities, store.StatusIdentities...)
		if store.StatusSigner != "" {
			identities = append(identities, store.StatusSigner)
		}
	}
	return normalizedRoleIdentities(identities, "status checkpoint")
}
