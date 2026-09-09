package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/nhensley/nkdctl/pkg/nkd/assemble"
	"github.com/nhensley/nkdctl/pkg/nkd/attest"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/pipeline"
	"github.com/nhensley/nkdctl/pkg/nkd/profile"
	"github.com/nhensley/nkdctl/pkg/nkd/recipe"
	"github.com/nhensley/nkdctl/pkg/nkd/trust"
	"github.com/urfave/cli/v2"
)

// qualifiedVerifyDependencies keeps the command thin while putting production and
// adversarial test adapters at the same seams. The authenticated document module is
// always responsible for signer authorization and decoding; Build receives only an
// already-decoded profile, never a profile pathname.
type qualifiedVerifyDependencies struct {
	Verifier    trust.DocumentVerifier
	Resolve     func(*cli.Context) (*recipe.Resolved, string, error)
	Build       func(*cli.Context, *recipe.Resolved, *profile.BuilderProfile, profile.HostPreflightReceipt) (*pipeline.Report, error)
	ObserveHost func() map[string]string
	// VerifyEvidence is package-private test injection; production always binds the
	// concrete SBOM/provenance recomputation below. It receives the authenticated
	// vector receipt and opaque terminal report rather than decoded values or caller
	// digests, so a test seam cannot accidentally become production authority.
	VerifyEvidence func(context.Context, trust.DocumentReceipt, *recipe.Resolved, *pipeline.Report) error
}

func defaultQualifiedVerifyDependencies() qualifiedVerifyDependencies {
	return qualifiedVerifyDependencies{
		Resolve:        resolveRecipe,
		Build:          runMeasureOnlyVerification,
		ObserveHost:    profile.ObserveHost,
		VerifyEvidence: verifyEvidenceDigests,
	}
}

// runMeasureOnlyVerification is the stock verifier boundary required by ADR-04.
// It consumes terminal result files and never invokes a build engine. From-source
// result qualification additionally needs a provider-issued execution receipt, for
// which the stock CLI has no adapter, so that lane fails closed in runQualifiedBuild.
func runMeasureOnlyVerification(c *cli.Context, res *recipe.Resolved, authenticatedProfile *profile.BuilderProfile, hostReceipt profile.HostPreflightReceipt) (*pipeline.Report, error) {
	if strings.TrimSpace(c.String("results-dir")) == "" {
		return nil, e.New(e.CodeEngineCapability,
			"the stock verifier is measure-only and never rebuilds; supply --results-dir with qualified producer results, or use a separately deployed producer/verifier integration")
	}
	return runQualifiedBuild(c, res, authenticatedProfile, hostReceipt)
}

func cmdVerify() *cli.Command {
	command := cmdVerifyWithDependencies(defaultQualifiedVerifyDependencies())
	action := command.Action
	command.Action = func(c *cli.Context) error {
		if err := rejectQualifiedPackaging(c); err != nil {
			return err
		}
		if strings.TrimSpace(c.String("decode")) == "" &&
			strings.TrimSpace(c.String("results-dir")) == "" {
			return e.New(e.CodeEngineCapability,
				"the stock verifier is measure-only: supply --results-dir with qualified producer results, or use --decode for authenticated disk-envelope verification")
		}
		return action(c)
	}
	return command
}

func cmdVerifyWithDependencies(deps qualifiedVerifyDependencies) *cli.Command {
	return &cli.Command{
		Name:  "verify",
		Usage: "authenticate a signed validation vector and verify qualified producer results without rebuilding",
		Flags: append(append(append(queryFlags(), qualifiedVerifyBuildFlags()...),
			&cli.StringFlag{Name: "expected-digests", Aliases: []string{"e"}, Required: true,
				Usage: "canonical JSON validation vector; the exact file bytes are authenticated and decoded once"},
			&cli.StringFlag{Name: "profile", Usage: "signed canonical JSON builder profile; required when the vector is profile-bound"},
			&cli.StringFlag{Name: "signature", Usage: "detached cosign signature for the validation vector (optional only when --bundle contains it)"},
			&cli.StringFlag{Name: "certificate", Usage: "Fulcio certificate for the validation vector (required)"},
			&cli.StringFlag{Name: "bundle", Usage: "Sigstore bundle for the validation vector"},
			&cli.StringFlag{Name: "trusted-root", Usage: "explicit Sigstore trusted-root JSON (required with --offline)"},
			&cli.StringFlag{Name: "trusted-root-digest", Usage: "exact lowercase sha256 digest of --trusted-root (required with --offline)"},
			&cli.StringFlag{Name: "identity", Usage: "trusted validation-vector certificate identity; overrides the operator vector trust store"},
			&cli.StringFlag{Name: "issuer", Usage: "pinned validation-vector OIDC issuer (required)"},
			&cli.BoolFlag{Name: "offline", Usage: "qualified air-gap verification using local bundle + explicit trusted root only"},
			&cli.StringFlag{Name: "trust-store", Usage: "operator role-separated trust store JSON (default .nkd/trust.json)"},
			&cli.StringFlag{Name: "profile-signature", Usage: "detached cosign signature for the canonical JSON builder profile"},
			&cli.StringFlag{Name: "profile-certificate", Usage: "Fulcio certificate for the builder profile"},
			&cli.StringFlag{Name: "profile-bundle", Usage: "Sigstore bundle for the builder profile"},
			&cli.StringFlag{Name: "profile-trusted-root", Usage: "explicit Sigstore trusted-root JSON for the builder profile (required offline)"},
			&cli.StringFlag{Name: "profile-trusted-root-digest", Usage: "exact lowercase sha256 digest of --profile-trusted-root (required offline)"},
			&cli.StringFlag{Name: "profile-identity", Usage: "trusted builder-profile certificate identity; overrides profileIdentities in the trust store"},
			&cli.StringFlag{Name: "profile-issuer", Usage: "pinned builder-profile OIDC issuer"},
			&cli.StringFlag{Name: "results-dir", Usage: "verify an exact profile-free prebuilt CI result set; from-source requires a provider stage-two attestation adapter"},
			&cli.StringFlag{Name: "evidence-out", Usage: "write an UNQUALIFIED deterministic diagnostic evidence payload; not a release receipt"},
			&cli.StringFlag{Name: "decode", Usage: "verify a derived disk envelope against the authenticated vector without rebuilding"},
			&cli.StringFlag{Name: "decode-format", Value: "qcow2", Usage: "with --decode: envelope format (qcow2|vmdk|vhd)"},
			&cli.StringFlag{Name: "decode-component", Usage: "with --decode: validation-vector component containing the RAW logical digest"},
			&cli.StringFlag{Name: "qemu-img", Value: assemble.QemuImgDefault, Usage: "with --decode: pinned qemu-img adapter path"},
			&cli.StringFlag{Name: "qemu-img-digest", Usage: "with --decode: exact sha256 digest of the qemu-img executable"},
		), qualifiedDocumentVerifierFlags()...),
		Action: func(c *cli.Context) error { return runQualifiedVerify(c, deps) },
	}
}

func qualifiedVerifyBuildFlags() []cli.Flag {
	var flags []cli.Flag
	for _, flag := range buildFlags() {
		if names := flag.Names(); len(names) > 0 {
			switch names[0] {
			case "skip-preflight", "profile-offline":
				continue // a bypass can never back a qualified Reproduced claim
			case "profile", "profile-signature", "profile-certificate", "profile-bundle",
				"profile-trusted-root", "profile-trusted-root-digest", "profile-identity",
				"profile-issuer", "trust-store":
				continue // qualified verify supplies its stricter canonical/authenticated contract above
			}
		}
		flags = append(flags, flag)
	}
	return flags
}

func runQualifiedVerify(c *cli.Context, deps qualifiedVerifyDependencies) error {
	if deps.Resolve == nil || deps.Build == nil || deps.ObserveHost == nil || deps.VerifyEvidence == nil {
		return e.New(e.CodeInternal, "qualified verify dependencies are incomplete")
	}
	if err := rejectQualifiedPackaging(c); err != nil {
		return err
	}
	verifier, err := resolveQualifiedDocumentVerifier(c, deps.Verifier)
	if err != nil {
		return err
	}
	vectorMaterial := trust.SignatureMaterial{
		SignaturePath: c.String("signature"), CertificatePath: c.String("certificate"),
		BundlePath: c.String("bundle"), TrustedRootPath: c.String("trusted-root"),
	}
	if err := validateQualifiedMaterial("validation vector", vectorMaterial, c.Bool("offline")); err != nil {
		return err
	}
	storeFile, err := loadTrustStoreFile(c.Context, trustStorePath(c))
	if err != nil {
		return err
	}
	vectorPolicy, err := qualifiedRolePolicy(c, storeFile, trust.RoleValidationVector)
	if err != nil {
		return err
	}
	vectorPolicy, err = qualifiedPolicyWithTrustedRoot(vectorPolicy,
		c.String("trusted-root"), c.String("trusted-root-digest"), c.Bool("offline"))
	if err != nil {
		return err
	}
	authenticator, err := trust.NewDocumentAuthenticator(
		verifier,
		trust.JSONSchema[attest.Manifest](attest.SchemaVersion),
		trust.JSONSchema[profile.BuilderProfile](profile.SchemaVersion),
	)
	if err != nil {
		return err
	}
	vectorReceipt, err := authenticator.Authenticate(c.Context, trust.AuthenticateRequest{
		SourcePath: c.String("expected-digests"), Signature: vectorMaterial,
		Offline: c.Bool("offline"), Role: trust.RoleValidationVector, Policy: vectorPolicy,
	})
	if err != nil {
		return err
	}
	wantValue, ok := vectorReceipt.Decoded().(attest.Manifest)
	if !ok {
		return e.New(e.CodeInternal, "authenticated validation-vector decoder returned %T", vectorReceipt.Decoded())
	}
	want := &wantValue
	if err := want.ValidatePublished(); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err,
			"authenticated validation vector is incomplete")
	}
	fmt.Printf("AUTHENTICATED validation vector %s (signer %s, role %s, schema %s)\n",
		vectorReceipt.SnapshotSHA256(), vectorReceipt.SignerIdentity(), vectorReceipt.Role(), vectorReceipt.SchemaVersion())

	// Decode verification consumes only the authenticated vector object. It never
	// reopens expected-digests, so replacing the source pathname cannot change the
	// expected logical digest after signature verification.
	if envelope := c.String("decode"); envelope != "" {
		return verifyDecode(c, want, envelope)
	}

	var (
		authenticatedProfile *profile.BuilderProfile
		profileDigest        string
		profileDivs          []profile.Divergence
		hostReceipt          profile.HostPreflightReceipt
	)
	expectedProfileDigest, err := vectorProfileDigest(want)
	if err != nil {
		return err
	}
	if expectedProfileDigest != "" {
		profileMaterial := trust.SignatureMaterial{
			SignaturePath: c.String("profile-signature"), CertificatePath: c.String("profile-certificate"),
			BundlePath: c.String("profile-bundle"), TrustedRootPath: c.String("profile-trusted-root"),
		}
		if err := validateQualifiedMaterial("builder profile", profileMaterial, c.Bool("offline")); err != nil {
			return err
		}
		if strings.TrimSpace(c.String("profile")) == "" {
			return e.New(e.CodeUntrustedProfile,
				"profile-bound validation vector requires --profile with the signed canonical JSON profile")
		}
		profilePolicy, err := qualifiedRolePolicy(c, storeFile, trust.RoleBuilderProfile)
		if err != nil {
			return err
		}
		profilePolicy, err = qualifiedPolicyWithTrustedRoot(profilePolicy,
			c.String("profile-trusted-root"), c.String("profile-trusted-root-digest"), c.Bool("offline"))
		if err != nil {
			return err
		}
		profileReceipt, err := authenticator.Authenticate(c.Context, trust.AuthenticateRequest{
			SourcePath: c.String("profile"), Signature: profileMaterial,
			Offline: c.Bool("offline"), Role: trust.RoleBuilderProfile, Policy: profilePolicy,
		})
		if err != nil {
			return err
		}
		profileValue, ok := profileReceipt.Decoded().(profile.BuilderProfile)
		if !ok {
			return e.New(e.CodeInternal, "authenticated profile decoder returned %T", profileReceipt.Decoded())
		}
		if err := profileValue.Validate(); err != nil {
			return err
		}
		profileDigest, err = profileValue.Digest()
		if err != nil {
			return err
		}
		if profileDigest != expectedProfileDigest {
			return e.New(e.CodeUntrustedProfile,
				"authenticated builder profile digest %s does not match vector-bound digest %s",
				profileDigest, expectedProfileDigest)
		}
		hostReceipt, profileDivs, err = profileValue.VerifyHost(c.Context, deps.ObserveHost())
		if err != nil {
			return err
		}
		authenticatedProfile = &profileValue
		fmt.Printf("AUTHENTICATED builder profile %s (signer %s, role %s)\n",
			profileReceipt.SnapshotSHA256(), profileReceipt.SignerIdentity(), profileReceipt.Role())
	}

	// Profile preflight above deliberately occurs before catalog/input resolution.
	res, _, err := deps.Resolve(c)
	if err != nil {
		return err
	}
	fromSource := recipeHasSource(res)
	if fromSource && authenticatedProfile == nil {
		return e.New(e.CodeUntrustedProfile,
			"from-source qualified verification requires a vector-bound, separately authenticated builder profile")
	}
	if !fromSource && authenticatedProfile != nil {
		return e.New(e.CodeVerifyFailed,
			"profile-bound validation vector resolved to an all-prebuilt recipe")
	}
	if authenticatedProfile != nil {
		if err := authenticatedProfile.RequireBuilders(recipe.RequiredBuilders(res)); err != nil {
			return err
		}
	}

	report, err := deps.Build(c, res, authenticatedProfile, hostReceipt)
	if report != nil {
		defer func() { _ = report.Close() }()
	}
	if err != nil {
		printReport(report)
		return err
	}
	if err := report.RequireQualified(c.Context); err != nil {
		return err
	}
	got, err := attest.FromQualifiedReport(c.Context, report)
	if err != nil {
		return err
	}
	if fromSource && got.ProfileDigest != profileDigest {
		return e.New(e.CodePreflightFailed,
			"qualified vector profile %s does not match authenticated profile %s", got.ProfileDigest, profileDigest)
	}

	strictErr := want.VerifyQualifiedAgainst(c.Context, got)
	if strictErr == nil {
		if err := deps.VerifyEvidence(c.Context, vectorReceipt, res, report); err != nil {
			_ = writeQualifiedEvidence(c, want.BuildEvidence(got, attest.VerdictRejected, profileDivs))
			return err
		}
		if err := writeQualifiedEvidence(c, want.BuildEvidence(got, attest.VerdictReproduced, profileDivs)); err != nil {
			return err
		}
		fmt.Printf("QUALIFIED VERIFIED (Reproduced)  %s\n%d selected component digest(s) reproduced from authenticated vector bytes under profile %s; omitted expected members received no verdict.\n",
			want.RecipeDigest, len(got.Components), orNone(want.ProfileDigest))
		return nil
	}
	_ = writeQualifiedEvidence(c, want.BuildEvidence(got, attest.VerdictRejected, profileDivs))
	return strictErr
}

func validateQualifiedMaterial(label string, material trust.SignatureMaterial, offline bool) error {
	if strings.TrimSpace(material.CertificatePath) == "" {
		return e.New(e.CodeSignatureInvalid, "qualified %s authentication requires a Fulcio certificate", label)
	}
	if strings.TrimSpace(material.SignaturePath) == "" && strings.TrimSpace(material.BundlePath) == "" {
		return e.New(e.CodeSignatureInvalid, "qualified %s authentication requires a signature or Sigstore bundle", label)
	}
	if material.TrustedRootPath != "" && material.BundlePath == "" {
		return e.New(e.CodeSignatureInvalid, "%s trusted root requires a Sigstore bundle", label)
	}
	if offline {
		if strings.TrimSpace(material.BundlePath) == "" {
			return e.New(e.CodeSignatureInvalid, "qualified offline %s authentication requires a Sigstore bundle", label)
		}
		if strings.TrimSpace(material.TrustedRootPath) == "" {
			return e.New(e.CodeUnpinnedInput,
				"qualified offline %s authentication requires an exact trusted-root path", label)
		}
	}
	return nil
}

func qualifiedRolePolicy(c *cli.Context, storeFile trustStoreFile, role trust.SignerRole) (trust.RolePolicy, error) {
	var identity, issuer string
	var operatorIdentities []string
	switch role {
	case trust.RoleValidationVector:
		identity, issuer = c.String("identity"), c.String("issuer")
		operatorIdentities = storeFile.Identities
	case trust.RoleBuilderProfile:
		identity, issuer = c.String("profile-identity"), c.String("profile-issuer")
		operatorIdentities = storeFile.ProfileIdentities
	case trust.RoleStatus:
		issuer = c.String("status-issuer")
		if storeFile.StatusSigner != "" {
			operatorIdentities = []string{storeFile.StatusSigner}
		}
	case trust.RoleConformance:
		issuer = c.String("conformance-issuer")
		operatorIdentities = storeFile.ConformanceIdentities
	default:
		return trust.RolePolicy{}, e.New(e.CodeTrustPolicyMissing, "unsupported signer role %q", role)
	}
	var cliIdentities []string
	if strings.TrimSpace(identity) != "" {
		cliIdentities = []string{identity}
	}
	return trust.NewRolePolicy(role, trust.Store{
		CLIIdentities: cliIdentities, OperatorIdentities: operatorIdentities,
	}, issuer)
}

func vectorProfileDigest(vector *attest.Manifest) (string, error) {
	if vector.BuilderProfile == nil {
		return vector.ProfileDigest, nil
	}
	if strings.TrimSpace(vector.BuilderProfile.Digest) == "" {
		return "", e.New(e.CodeVerifyFailed,
			"authenticated vector has a builderProfile reference without a digest")
	}
	if vector.ProfileDigest != vector.BuilderProfile.Digest {
		return "", e.New(e.CodeVerifyFailed,
			"authenticated vector is internally inconsistent: profileDigest %s != builderProfile.digest %s",
			orNone(vector.ProfileDigest), vector.BuilderProfile.Digest)
	}
	return vector.BuilderProfile.Digest, nil
}

func runQualifiedBuild(c *cli.Context, res *recipe.Resolved, authenticatedProfile *profile.BuilderProfile, hostReceipt profile.HostPreflightReceipt) (*pipeline.Report, error) {
	if recipeHasSource(res) && authenticatedProfile == nil {
		return nil, e.New(e.CodeUntrustedProfile, "qualified from-source build has no authenticated builder profile")
	}
	graph, err := recipe.Compile(res)
	if err != nil {
		return nil, err
	}
	digest, err := graph.Digest()
	if err != nil {
		return nil, err
	}
	only, err := skuComponents(c, res)
	if err != nil {
		return nil, err
	}
	opts := pipeline.RunOptions{
		Ctx:        c.Context,
		SourceRoot: c.String("source-root"), Only: only, Jobs: c.Int("jobs"),
		KeepGoing: c.Bool("keep-going"), Package: c.Bool("package"), Runtime: c.String("runtime"),
		Stdout: os.Stdout, Stderr: os.Stderr,
	}
	if authenticatedProfile != nil {
		opts.Qualification = &pipeline.QualifiedExecution{
			Profile: *authenticatedProfile, HostReceipt: hostReceipt,
			Observer: pipeline.LocalExecutionObserver{},
		}
	}
	if resultsDir := strings.TrimSpace(c.String("results-dir")); resultsDir != "" {
		if recipeHasSource(res) {
			return nil, e.New(e.CodeEngineCapability,
				"from-source --results-dir requires a provider execution-attestation adapter that issued the stage-two receipt before producer artifact work; the stock CLI cannot substitute a terminal-host observation")
		}
		fmt.Fprintf(os.Stderr, "verifying terminal CI result set in %s (no source rebuild)\n", resultsDir)
		return pipeline.Local{}.VerifyResults(graph, digest, resultsDir, opts)
	}
	fmt.Fprintf(os.Stderr, "building with up to %d parallel job(s)\n", c.Int("jobs"))
	return pipeline.Local{}.Run(graph, digest, opts)
}

func writeQualifiedEvidence(c *cli.Context, bundle *attest.EvidenceBundle) error {
	path := c.String("evidence-out")
	if path == "" || bundle == nil {
		return nil
	}
	raw, err := bundle.Marshal()
	if err != nil {
		return err
	}
	if err := writeAtomic(c.Context, path, raw, 0o644); err != nil {
		return err
	}
	fmt.Printf("UNQUALIFIED DIAGNOSTIC: wrote evidence bundle %s (verdict=%s); downstream authority requires a separately authenticated reproduction receipt\n", path, bundle.Verdict)
	return nil
}
