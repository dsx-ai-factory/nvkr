package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/lifecycle"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/release"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/status"
	"github.com/urfave/cli/v2"
)

const releaseVerificationResultSchemaVersion = "nkd.dev/release-verification-result/v1"

type releaseVerificationResult struct {
	SchemaVersion string                           `json:"schemaVersion"`
	Release       lifecycle.Release                `json:"release"`
	ExactInputs   *lifecycle.ExactInputDescription `json:"exactInputs,omitempty"`
}

type releaseVerifyOperation func(context.Context, string, lifecycle.VerificationPolicy,
	lifecycle.ConsumerOptions, *lifecycle.ExactInputExpectation) (releaseVerificationResult, error)

func executeReleaseVerification(ctx context.Context, reference string, policy lifecycle.VerificationPolicy,
	options lifecycle.ConsumerOptions, expectation *lifecycle.ExactInputExpectation,
) (releaseVerificationResult, error) {
	consumer, err := lifecycle.NewConsumer(options)
	if err != nil {
		return releaseVerificationResult{}, err
	}
	var verified *lifecycle.VerifiedRelease
	var receipt *lifecycle.ExactInputReceipt
	if expectation != nil {
		verified, receipt, err = consumer.VerifyExactInputs(ctx, reference, policy, expectation)
	} else {
		verified, err = consumer.Verify(ctx, reference, policy)
	}
	if err != nil {
		return releaseVerificationResult{}, err
	}
	defer func() { _ = verified.Close() }()
	description, err := verified.Description()
	if err != nil {
		return releaseVerificationResult{}, err
	}
	result := releaseVerificationResult{Release: description}
	if receipt != nil {
		exact, exactErr := receipt.Description()
		if exactErr != nil {
			return releaseVerificationResult{}, exactErr
		}
		result.ExactInputs = &exact
	}
	return result, nil
}

// cmdRelease exposes the release object-authoring and authentication utilities that
// have production adapters. Promotion, selection, rollback, and publication remain
// explicit fail-closed capability stubs until their protocols are implemented.
func cmdRelease() *cli.Command {
	return cmdReleaseWithVerify(executeReleaseVerification)
}

func cmdReleaseWithVerify(verify releaseVerifyOperation) *cli.Command {
	legacy := []*cli.Command{
		{
			Name:    "promote",
			Aliases: []string{"clear"},
			Usage:   "disabled legacy promotion path (qualified receipt adapter required)",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "inventory", Hidden: true},
				&cli.StringFlag{Name: "set", Hidden: true},
				&cli.StringFlag{Name: "conformance", Hidden: true},
				&cli.StringFlag{Name: "out", Hidden: true},
			},
			Action: func(*cli.Context) error {
				return e.New(e.CodeUnsupported,
					"legacy release promote is disabled: caller-provided reproduction/conformance verdicts cannot authorize a release; qualified release promotion is not implemented")
			},
		},
		{
			Name: "select", Usage: "disabled diagnostic selector (authenticated ReleaseSet + fresh status checkpoint adapter required)", ArgsUsage: "<selector>",
			Flags: []cli.Flag{&cli.StringFlag{Name: "resolve", Usage: "digest a bare tag resolves to (stand-in for a registry lookup)"}},
			Action: func(*cli.Context) error {
				return e.New(e.CodeUnsupported, "release select is disabled: resolving a name or digest is not authorization; selection requires an authenticated ReleaseSet and a fresh accepted status checkpoint")
			},
		},
		{
			Name: "rollback", Usage: "disabled standalone rollback path (fresh authorization and status capabilities required)",
			Flags: []cli.Flag{&cli.StringFlag{Name: "to", Hidden: true}, &cli.StringFlag{Name: "known", Hidden: true}},
			Action: func(*cli.Context) error {
				return e.New(e.CodeUnsupported, "standalone rollback is disabled: historical JSON/map entries are not authority; a production rollback adapter with fresh authenticated evidence and status is not implemented")
			},
		},
		releaseStatusCmd(), releaseSetCmd(), releasePromotionPolicyCmd(),
		releaseDeclareCmd("supersede", status.ActionSuperseded), releaseDeclareCmd("deprecate", status.ActionDeprecated),
		releaseDeclareCmd("withdraw", status.ActionWithdrawn), releaseCheckpointCmd(), releaseFromVectorsCmd(), releaseAdvisoryCmd(),
	}
	for _, command := range legacy {
		command.Hidden = true
	}
	return &cli.Command{
		Name:            "release",
		Usage:           "inspect or verify an immutable release closure",
		HideHelpCommand: true,
		Subcommands: append([]*cli.Command{
			releaseBrowseCmd(),
			{
				Name: "inspect", Usage: "inspect an exact local OCI release layout without granting authority",
				Flags: []cli.Flag{&cli.StringFlag{Name: "reference", Required: true}, &cli.StringFlag{Name: "layout", Required: true}},
				Action: func(c *cli.Context) error {
					artifacts, err := lifecycle.InspectReleaseLayout(c.Context, c.String("reference"), c.String("layout"))
					if err != nil {
						return err
					}
					raw, err := canonical.Marshal(artifacts)
					if err != nil {
						return err
					}
					_, err = os.Stdout.Write(append(raw, '\n'))
					return err
				},
			},
			{
				Name: "verify", Usage: "verify one pinned reference under an explicit Verification Policy scope",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "reference", Required: true}, &cli.StringFlag{Name: "lane", Required: true}, &cli.StringFlag{Name: "scope", Required: true},
					&cli.StringFlag{Name: "minimum-disk-evidence", Usage: "minimum accepted disk evidence: strict or build-provenance-v1 (default: build-provenance-v1 for pipeline-attested; strict for qualified)"},
					&cli.StringFlag{Name: "identity"}, &cli.StringFlag{Name: "issuer"}, &cli.StringFlag{Name: "cosign"}, &cli.StringFlag{Name: "cosign-digest"},
					&cli.StringFlag{Name: "definition", Usage: "exact authored Definition to require with --environment and --plan"},
					&cli.StringFlag{Name: "environment", Usage: "exact selected Environment to require with --definition and --plan"},
					&cli.StringFlag{Name: "plan", Usage: "exact generated compiled Plan to require with --definition and --environment; unreadable or undecodable Plan bytes report pipeline drift, while validation errors keep their code"},
					&cli.StringFlag{Name: "workspace", Value: ".nkd/verified-releases"}, &cli.BoolFlag{Name: "anonymous"}, &cli.BoolFlag{Name: "plain-http"},
				},
				Action: func(c *cli.Context) error {
					exactInputCount := 0
					for _, name := range []string{"definition", "environment", "plan"} {
						if !c.IsSet(name) {
							continue
						}
						exactInputCount++
						if strings.TrimSpace(c.String(name)) == "" {
							return e.New(e.CodeUsage, "--%s requires a non-empty path", name)
						}
					}
					if exactInputCount != 0 && exactInputCount != 3 {
						return e.New(e.CodeUsage, "release verification requires --definition, --environment, and --plan together")
					}
					policy := lifecycle.VerificationPolicy{Lane: c.String("lane"), Scope: c.String("scope"), IdentityRegexp: c.String("identity"), Issuer: c.String("issuer"),
						MinimumDiskEvidence: lifecycle.DiskEvidenceRequirement(c.String("minimum-disk-evidence"))}
					if err := policy.Validate(); err != nil {
						return err
					}
					if policy.Lane != lifecycle.LanePipeline || policy.Scope != lifecycle.ScopeArtifactPublication {
						if exactInputCount != 0 {
							return e.New(e.CodeEngineCapability,
								"exact-input verification requires pipeline-attested/artifact-publication policy")
						}
						_, err := (&lifecycle.Consumer{}).Verify(c.Context, c.String("reference"), policy)
						return err
					}
					var definition lifecycle.Definition
					var environment lifecycle.Environment
					var plan *factory.Plan
					var expectation *lifecycle.ExactInputExpectation
					if exactInputCount == 3 {
						var err error
						definition, err = lifecycle.LoadDefinition(c.Context, c.String("definition"))
						if err != nil {
							return err
						}
						environment, err = lifecycle.LoadEnvironment(c.Context, c.String("environment"))
						if err != nil {
							return err
						}
						plan, err = factory.LoadPlanContext(c.Context, c.String("plan"))
						if err != nil {
							return err
						}
						expectation, err = lifecycle.PrepareExactInputs(definition, environment, plan)
						if err != nil {
							return err
						}
					}
					result, err := verify(c.Context, c.String("reference"), policy, lifecycle.ConsumerOptions{
						CosignBinary: c.String("cosign"), CosignDigest: c.String("cosign-digest"),
						Registry:      delivery.RegistryOptions{Anonymous: c.Bool("anonymous"), PlainHTTP: c.Bool("plain-http")},
						WorkspaceRoot: c.String("workspace"),
					}, expectation)
					if err != nil {
						return err
					}
					var raw []byte
					if result.ExactInputs != nil {
						result.SchemaVersion = releaseVerificationResultSchemaVersion
						raw, err = canonical.Marshal(result)
					} else {
						raw, err = canonical.Marshal(result.Release)
					}
					if err != nil {
						return err
					}
					_, err = os.Stdout.Write(append(raw, '\n'))
					return err
				},
			},
		}, legacy...),
	}
}

// releaseCheckpointCmd creates the signed complete-set object consumed by
// release status. It makes no verification claim about its member documents; the
// consumer independently authenticates every exact declaration before accepting
// the checkpoint capability.
func releaseCheckpointCmd() *cli.Command {
	return &cli.Command{
		Name:  "checkpoint",
		Usage: "emit a canonical complete status checkpoint for independent signing",
		Flags: append([]cli.Flag{
			&cli.StringFlag{Name: "target", Required: true, Usage: "exact publication digest"},
			&cli.StringFlag{Name: "authority", Required: true, Usage: "stable status authority identity"},
			&cli.Uint64Flag{Name: "sequence", Usage: "monotonic checkpoint sequence (genesis is 0)"},
			&cli.StringFlag{Name: "previous", Usage: "exact predecessor checkpoint digest (required after genesis)"},
			&cli.StringSliceFlag{Name: "declaration", Usage: "canonical status declaration in the complete set (repeatable)"},
			&cli.Int64Flag{Name: "issued-epoch", Required: true},
			&cli.Int64Flag{Name: "valid-until-epoch", Required: true},
			&cli.StringFlag{Name: "out", Value: "status-checkpoint.json"},
			&cli.BoolFlag{Name: "sign", Usage: "detached-sign the canonical checkpoint with the pinned cosign capability"},
		}, qualifiedDocumentSignerFlags()...),
		Action: func(c *cli.Context) error {
			if c.Bool("sign") {
				if err := admitQualifiedDocumentSigner(c); err != nil {
					return err
				}
			}
			digests := make([]string, 0, len(c.StringSlice("declaration")))
			for _, path := range c.StringSlice("declaration") {
				var declaration status.StatusDeclaration
				if err := readCanonicalJSONContext(c.Context, path, &declaration); err != nil {
					return err
				}
				if err := declaration.Validate(); err != nil {
					return err
				}
				if declaration.Target != c.String("target") {
					return e.New(e.CodeStatusCheckpoint,
						"declaration %s targets %s instead of checkpoint target %s", path, declaration.Target, c.String("target"))
				}
				digest, err := declaration.Digest()
				if err != nil {
					return err
				}
				digests = append(digests, digest)
			}
			sort.Strings(digests)
			for index := 1; index < len(digests); index++ {
				if digests[index-1] == digests[index] {
					return e.New(e.CodeStatusCheckpoint, "checkpoint repeats declaration %s", digests[index])
				}
			}
			checkpoint := &status.StatusCheckpoint{
				SchemaVersion: status.CheckpointSchemaVersion, Target: c.String("target"),
				Authority: c.String("authority"), Sequence: c.Uint64("sequence"),
				PreviousCheckpoint: c.String("previous"), DeclarationDigests: digests,
				IssuedEpoch: c.Int64("issued-epoch"), ValidUntilEpoch: c.Int64("valid-until-epoch"),
			}
			raw, err := checkpoint.Marshal()
			if err != nil {
				return err
			}
			out := c.String("out")
			digest, _ := checkpoint.Digest()
			if c.Bool("sign") {
				if err := writeQualifiedSignedDocument(c, raw, out, ""); err != nil {
					return err
				}
			} else if err := writeAtomic(c.Context, out, raw, 0o644); err != nil {
				return err
			}
			fmt.Printf("wrote status checkpoint %s → %s\n", digest, out)
			return nil
		},
	}
}

// releaseFromVectorsCmd authors a structural ReleaseSet document from caller-supplied
// JSON. It authenticates no Inventory, member, vector, or ReleaseSet and therefore
// cannot report or mint release authority.
func releaseFromVectorsCmd() *cli.Command {
	return &cli.Command{
		Name:      "from-vectors",
		Usage:     "author an UNQUALIFIED structural ReleaseSet from caller-supplied Inventory/member JSON",
		ArgsUsage: "<member.json>...",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "inventory", Required: true, Usage: "inventory JSON (binds the required set; ADR-05)"},
			&cli.StringFlag{Name: "version", Required: true, Usage: "release version label"},
			&cli.StringFlag{Name: "out", Value: "release-set.json", Usage: "ReleaseSet output path"},
		},
		Action: func(c *cli.Context) error {
			if c.NArg() == 0 {
				return e.New(e.CodeUsage, "from-vectors takes one or more member JSON paths")
			}
			var in release.Inventory
			if err := readCanonicalJSONContext(c.Context, c.String("inventory"), &in); err != nil {
				return err
			}
			invDigest, err := in.Digest()
			if err != nil {
				return err
			}
			rs := &release.ReleaseSet{SchemaVersion: release.ReleaseSetSchemaVersion, Version: c.String("version"), InventoryRef: invDigest}
			for _, p := range c.Args().Slice() {
				var mem release.Member
				if err := readCanonicalJSONContext(c.Context, p, &mem); err != nil {
					return err
				}
				rs.Members = append(rs.Members, mem)
			}
			d, err := rs.Digest()
			if err != nil {
				return err
			}
			b, err := rs.Marshal()
			if err != nil {
				return err
			}
			if err := writeAtomic(c.Context, c.String("out"), b, 0o644); err != nil {
				return err
			}
			fmt.Printf("wrote UNQUALIFIED structural ReleaseSet %s (%d members, inventory %s) → %s; no input or output was authenticated\n",
				d, len(rs.Members), invDigest, c.String("out"))
			if err := rs.CompletenessGate(&in); err != nil {
				fmt.Printf("UNQUALIFIED STRUCTURAL DIAGNOSTIC: incomplete caller-authored set: %v\n", err)
			} else {
				fmt.Println("UNQUALIFIED STRUCTURAL DIAGNOSTIC: every caller-authored Inventory member is represented; this is not a promotion/completeness receipt")
			}
			return nil
		},
	}
}

// releaseAdvisoryCmd maps an advisory to the build digests it affects over a
// CALLER-SUPPLIED resolvable set (ADR-13 §4) — NKD keeps no fleet index, so any digest
// it cannot independently recompute fails closed.
func releaseAdvisoryCmd() *cli.Command {
	return &cli.Command{
		Name:  "advisory",
		Usage: "disabled legacy advisory path (a recomputing build-index adapter is required)",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "id", Hidden: true},
			&cli.StringSliceFlag{Name: "affected", Hidden: true},
			&cli.StringSliceFlag{Name: "resolvable", Hidden: true},
			&cli.StringFlag{Name: "replacement", Hidden: true},
			&cli.StringFlag{Name: "out", Hidden: true},
		},
		Action: func(*cli.Context) error {
			return e.New(e.CodeUnsupported,
				"legacy advisory is disabled: --resolvable caller booleans do not prove build identity; configure an adapter that recomputes every affected build digest")
		},
	}
}

// releaseDeclareCmd builds a lifecycle-declaration verb (ADR-06 §2): it emits a
// canonical StatusDeclaration referencing the target by immutable identity and can
// optionally sign it with a detached cosign signature from the pinned status-signer.
func releaseDeclareCmd(name, action string) *cli.Command {
	return &cli.Command{
		Name:  name,
		Usage: "emit a canonical " + action + " status declaration, optionally signed (ADR-06)",
		Flags: append([]cli.Flag{
			&cli.StringFlag{Name: "target", Required: true, Usage: "publication identity (sha256:…) being " + action},
			&cli.StringFlag{Name: "replaced-by", Usage: "the immutable identity replacing it (required for supersede)"},
			&cli.StringFlag{Name: "reason"},
			&cli.Int64Flag{Name: "effective-epoch", Usage: "operator-supplied effective time (never a wall-clock read)"},
			&cli.StringFlag{Name: "out", Value: "status-declaration.json", Usage: "declaration output path"},
			&cli.BoolFlag{Name: "sign", Usage: "detached-sign the declaration via the pinned cosign capability"},
		}, qualifiedDocumentSignerFlags()...),
		Action: func(c *cli.Context) error {
			if c.Bool("sign") {
				if err := admitQualifiedDocumentSigner(c); err != nil {
					return err
				}
			}
			d := &status.StatusDeclaration{
				SchemaVersion: status.SchemaVersion, Target: c.String("target"), Action: action,
				ReplacedBy: c.String("replaced-by"), Reason: c.String("reason"), EffectiveEpoch: c.Int64("effective-epoch"),
			}
			if err := d.Validate(); err != nil {
				return err
			}
			b, err := d.Marshal()
			if err != nil {
				return err
			}
			out := c.String("out")
			dg, _ := d.Digest()
			if c.Bool("sign") {
				if err := writeQualifiedSignedDocument(c, b, out, ""); err != nil {
					return err
				}
				fmt.Printf("signed: %s.sig + %s.pem (attach as an OCI referrer to the publication)\n", out, out)
			} else if err := writeAtomic(c.Context, out, b, 0o644); err != nil {
				return err
			}
			fmt.Printf("wrote %s declaration → %s (%s)\n", action, out, dg)
			return nil
		},
	}
}
