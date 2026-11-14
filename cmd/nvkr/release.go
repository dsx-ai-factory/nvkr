// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/urfave/cli/v2"
)

const releaseVerificationResultSchemaVersion = "nvkr.dev/release-verification-result/v1"

type releaseVerificationResult struct {
	SchemaVersion string                           `json:"schemaVersion"`
	Release       lifecycle.Release                `json:"release"`
	ExactInputs   *lifecycle.ExactInputDescription `json:"exactInputs,omitempty"`
}

type releaseVerifyOperation func(context.Context, string, lifecycle.VerificationPolicy,
	lifecycle.ConsumerOptions, *lifecycle.ExactInputExpectation, releaseExportOptions) (releaseVerificationResult, error)

type releaseExportOptions struct {
	Invocation string
	Directory  string
}

type releaseAuthenticateOperation func(context.Context, string, lifecycle.VerificationPolicy,
	lifecycle.ConsumerOptions) (lifecycle.AuthenticityDescription, error)

func executeReleaseAuthentication(ctx context.Context, reference string, policy lifecycle.VerificationPolicy,
	options lifecycle.ConsumerOptions,
) (lifecycle.AuthenticityDescription, error) {
	consumer, err := lifecycle.NewConsumer(options)
	if err != nil {
		return lifecycle.AuthenticityDescription{}, err
	}
	authenticated, err := consumer.Authenticate(ctx, reference, policy)
	if err != nil {
		return lifecycle.AuthenticityDescription{}, err
	}
	defer func() { _ = authenticated.Close() }()
	return authenticated.Description()
}

func executeReleaseVerification(ctx context.Context, reference string, policy lifecycle.VerificationPolicy,
	options lifecycle.ConsumerOptions, expectation *lifecycle.ExactInputExpectation,
	export releaseExportOptions,
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
	if export.Directory != "" {
		if err := verified.ExportForInvocation(ctx, receipt, export.Invocation, export.Directory); err != nil {
			return releaseVerificationResult{}, err
		}
	}
	return result, nil
}

// cmdRelease exposes the supported release inspection and authentication utilities.
func cmdRelease() *cli.Command {
	return cmdReleaseWithVerify(executeReleaseVerification)
}

func cmdReleaseWithVerify(verify releaseVerifyOperation) *cli.Command {
	return cmdReleaseWithOperations(verify, executeReleaseAuthentication)
}

func cmdReleaseWithOperations(verify releaseVerifyOperation, authenticate releaseAuthenticateOperation) *cli.Command {
	return &cli.Command{
		Name:            "release",
		Usage:           "inspect, compare, pull outputs from, or verify an immutable release closure",
		HideHelpCommand: true,
		Subcommands: []*cli.Command{
			releaseBrowseCmd(),
			releaseDiffCmd(),
			releaseExportCmd(),
			releasePullCmd(),
			{
				Name: "inspect", Usage: "inspect an exact local OCI release layout without granting authority",
				Flags: append([]cli.Flag{&cli.StringFlag{Name: "reference", Required: true}, &cli.StringFlag{Name: "layout", Required: true}}, commandPresentationFlags()...),
				Action: func(c *cli.Context) (returnedErr error) {
					view, err := selectCommandPresentation(c)
					if err != nil {
						return err
					}
					shown := false
					defer func() {
						if returnedErr != nil && view.tui && !shown {
							returnedErr = showCommandResult(c, view, releaseFailureDisplay("Release inspection"), returnedErr)
						}
					}()
					artifacts, err := lifecycle.InspectReleaseLayout(c.Context, c.String("reference"), c.String("layout"))
					if err != nil {
						return err
					}
					if view.tui {
						shown = true
						return showCommandResult(c, view, releaseArtifactsDisplay("Release inspection", "Local closure membership and payload integrity checked; signatures and policy were not verified.", artifacts), nil)
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
				Flags: append([]cli.Flag{
					&cli.StringFlag{Name: "reference", Required: true}, &cli.StringFlag{Name: "lane", Required: true, Usage: "pipeline-attested", Action: func(_ *cli.Context, value string) error {
						if value != lifecycle.LanePipeline {
							return e.New(e.CodeUsage, "unsupported lane %q; use pipeline-attested", value)
						}
						return nil
					}},
					&cli.StringFlag{Name: "scope", Required: true, Usage: "authenticity (signatures and evidence; payload integrity not checked), artifact-publication (includes payload integrity), or release (capability-gated)"},
					&cli.DurationFlag{Name: "timeout", Value: time.Hour, Usage: "overall release verification timeout"},
					&cli.BoolFlag{Name: "no-cache", Usage: "disable the authenticity metadata cache"},
					&cli.StringFlag{Name: "minimum-disk-evidence", Usage: "minimum accepted disk evidence: strict or build-provenance-v1 (default: build-provenance-v1)"},
					&cli.StringFlag{Name: "identity"}, &cli.StringFlag{Name: "issuer"}, &cli.StringFlag{Name: "cosign"}, &cli.StringFlag{Name: "cosign-digest"},
					&cli.StringFlag{Name: "definition", Usage: "exact authored Definition to require with --environment and --plan"},
					&cli.StringFlag{Name: "environment", Usage: "exact selected Environment to require with --definition and --plan"},
					&cli.StringFlag{Name: "plan", Usage: "exact generated compiled Plan to require with --definition and --environment; unreadable or undecodable Plan bytes report pipeline drift, while validation errors keep their code"},
					&cli.StringFlag{Name: "export-layout", Usage: "export held verified bytes to a new layout; requires exact inputs and --expected-invocation"},
					&cli.StringFlag{Name: "expected-invocation", Usage: "exact originating invocation required for --export-layout"},
					&cli.StringFlag{Name: "workspace", Value: ".nvkr/verified-releases"}, &cli.BoolFlag{Name: "anonymous"}, &cli.BoolFlag{Name: "plain-http"},
				}, commandPresentationFlags()...),
				Action: func(c *cli.Context) (returnedErr error) {
					view, err := selectCommandPresentation(c)
					if err != nil {
						return err
					}
					shown := false
					defer func() {
						if returnedErr != nil && view.tui && !shown {
							returnedErr = showCommandResult(c, view, releaseFailureDisplay("Release verification"), returnedErr)
						}
					}()
					exactInputCount := 0
					for _, name := range []string{"definition", "environment", "plan"} {
						if !c.IsSet(name) {
							continue
						}
						if c.String("scope") == lifecycle.ScopeAuthenticity {
							return e.New(e.CodeEngineCapability,
								"authenticity verification cannot verify exact inputs; --definition, --environment, and --plan require artifact-publication scope")
						}
						exactInputCount++
						if strings.TrimSpace(c.String(name)) == "" {
							return e.New(e.CodeUsage, "--%s requires a non-empty path", name)
						}
					}
					if exactInputCount != 0 && exactInputCount != 3 {
						return e.New(e.CodeUsage, "release verification requires --definition, --environment, and --plan together")
					}
					export := releaseExportOptions{Invocation: c.String("expected-invocation"), Directory: c.String("export-layout")}
					if c.IsSet("export-layout") || c.IsSet("expected-invocation") {
						if c.String("scope") == lifecycle.ScopeAuthenticity {
							return e.New(e.CodeEngineCapability, "authenticity verification cannot export payloads; publication export requires artifact-publication scope")
						}
						if strings.TrimSpace(export.Invocation) == "" || strings.TrimSpace(export.Directory) == "" || exactInputCount != 3 {
							return e.New(e.CodeUsage, "publication export requires exact inputs, --export-layout and --expected-invocation")
						}
					}
					policy := lifecycle.VerificationPolicy{Lane: c.String("lane"), Scope: c.String("scope"), IdentityRegexp: c.String("identity"), Issuer: c.String("issuer"),
						MinimumDiskEvidence: lifecycle.DiskEvidenceRequirement(c.String("minimum-disk-evidence"))}
					if err := policy.Validate(); err != nil {
						return err
					}
					timeout := c.Duration("timeout")
					if timeout <= 0 {
						return e.New(e.CodeUsage, "release verification timeout must be positive")
					}
					ctx, cancel := context.WithTimeout(c.Context, timeout)
					defer cancel()
					options := lifecycle.ConsumerOptions{
						Timeout: timeout, CosignBinary: c.String("cosign"), CosignDigest: c.String("cosign-digest"),
						Registry:             delivery.RegistryOptions{Anonymous: c.Bool("anonymous"), PlainHTTP: c.Bool("plain-http")},
						WorkspaceRoot:        c.String("workspace"),
						DisableMetadataCache: c.Bool("no-cache"), MetadataProgress: releaseMetadataReporter(c, view),
					}
					if policy.Scope == lifecycle.ScopeAuthenticity {
						if view.tui {
							shown = true
							deadline, _ := ctx.Deadline()
							return showReleaseMetadataLoad(c, view, "Release authenticity verification", "", func(workCtx context.Context, report func(delivery.MetadataProgress)) (commandDisplay, error) {
								workCtx, cancel := context.WithDeadline(workCtx, deadline)
								defer cancel()
								options.MetadataProgress = report
								description, err := authenticate(workCtx, c.String("reference"), policy, options)
								if err != nil {
									return commandDisplay{}, err
								}
								return authenticatedReleaseDisplay(description), nil
							})
						}
						description, err := authenticate(ctx, c.String("reference"), policy, options)
						if err != nil {
							return err
						}
						raw, err := canonical.Marshal(description)
						if err != nil {
							return err
						}
						_, err = os.Stdout.Write(append(raw, '\n'))
						if err != nil {
							return e.Wrap(e.CodeInternal, err, "writing release authenticity result")
						}
						return nil
					}
					if policy.Lane != lifecycle.LanePipeline || policy.Scope != lifecycle.ScopeArtifactPublication {
						if exactInputCount != 0 {
							return e.New(e.CodeEngineCapability,
								"exact-input verification requires pipeline-attested/artifact-publication policy")
						}
						_, err := (&lifecycle.Consumer{}).Verify(ctx, c.String("reference"), policy)
						return err
					}
					var expectation *lifecycle.ExactInputExpectation
					if exactInputCount == 3 {
						input, err := lifecycle.LoadDefinitionInput(ctx, c.String("definition"))
						if err != nil {
							return err
						}
						environment, err := lifecycle.LoadEnvironment(ctx, c.String("environment"))
						if err != nil {
							return err
						}
						plan, err := factory.LoadPlanContext(ctx, c.String("plan"))
						if err != nil {
							return err
						}
						expectation, err = lifecycle.PrepareInputExactInputs(input, environment, plan)
						if err != nil {
							return err
						}
					}
					result, err := verify(ctx, c.String("reference"), policy, options, expectation, export)
					if err != nil {
						return err
					}
					if view.tui {
						shown = true
						return showCommandResult(c, view, verifiedReleaseDisplay(result), nil)
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
		},
	}
}
