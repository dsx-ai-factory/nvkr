// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"runtime"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
)

const QualificationCandidateSchema = "nvkr.dev/qualification-candidate/v1"

// QualificationCandidate is an untrusted transport record for the original
// Run's saved predicates. It is not independently authored lifecycle authority.
// Predicate contains the original canonical JSON bytes (base64 in JSON).
type QualificationCandidate struct {
	SchemaVersion string                   `json:"schemaVersion"`
	Reference     string                   `json:"reference"`
	Predicates    []QualificationPredicate `json:"predicates"`
}

type QualificationFilesOptions struct {
	DefinitionPath    string
	EnvironmentPath   string
	PlanPath          string
	CandidatePath     string
	Source            qualificationorigin.SourceRequest
	GitHubToken       string
	CIStorageToken    string
	CIStorageUsername string
	Consumer          ConsumerOptions
}

// CheckQualificationFiles owns file, provider and consumer orchestration for
// adapters. It recompiles the exact supplied inputs, authenticates the separate
// source Run, then consumes published bytes without running a producer.
func CheckQualificationFiles(ctx context.Context, options QualificationFilesOptions) (*ArtifactCheckReceipt, *ArtifactCheckDescription, error) {
	if ctx == nil {
		return nil, nil, e.New(e.CodeUsage, "artifact qualification requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, e.Wrap(e.CodeTimeout, err, "artifact qualification canceled")
	}
	if runtime.GOOS != "linux" {
		return nil, nil, e.New(e.CodeEngineCapability, "artifact qualification requires Linux pinned-executable verification")
	}
	if options.DefinitionPath == "" || options.EnvironmentPath == "" || options.PlanPath == "" || options.CandidatePath == "" {
		return nil, nil, e.New(e.CodeUsage, "artifact qualification requires Definition, Environment, original Plan and candidate records")
	}
	// Validate the tool pin before any document or provider work. Trust identity
	// is selected below from the admitted Environment and authenticated source.
	if options.Consumer.CosignBinary == "" || !canonical.IsDigest(options.Consumer.CosignDigest) {
		return nil, nil, e.New(e.CodeUnpinnedInput, "artifact qualification requires an explicit cosign executable and exact byte pin")
	}
	if err := options.Source.Validate(); err != nil {
		return nil, nil, err
	}
	// Read only the bounded transport record before admitting its destination.
	// Registry credentials are independently configured, never selected by the
	// candidate or Environment; refuse a different host before compiler/provider I/O.
	candidate, err := readQualificationCandidate(ctx, options.CandidatePath)
	if err != nil {
		return nil, nil, err
	}
	consumer, err := newQualificationReferenceConsumer(candidate.Reference, options.Consumer)
	if err != nil {
		return nil, nil, err
	}
	definition, err := LoadDefinition(ctx, options.DefinitionPath)
	if err != nil {
		return nil, nil, err
	}
	environment, err := LoadEnvironment(ctx, options.EnvironmentPath)
	if err != nil {
		return nil, nil, err
	}
	if environment.Signing == nil || environment.Signing.Mode != factory.SigningModeCosign || environment.Signing.Issuer != "https://token.actions.githubusercontent.com" {
		return nil, nil, e.New(e.CodeEngineCapability, "artifact qualification requires live GitHub cosign verification")
	}
	plan, err := factory.LoadPlanContext(ctx, options.PlanPath)
	if err != nil {
		return nil, nil, err
	}
	if err := factory.AdmitQualificationHandoff(plan); err != nil {
		return nil, nil, err
	}
	expected, err := Compile(ctx, options.DefinitionPath, definition, options.EnvironmentPath, environment, plan.GeneratorVersion)
	if err != nil {
		return nil, nil, err
	}
	want, err := expected.Digest()
	if err != nil {
		return nil, nil, err
	}
	got, err := plan.Digest()
	if err != nil {
		return nil, nil, err
	}
	if got != want {
		return nil, nil, e.New(e.CodePipelineDrift, "qualification Plan differs from the exact recompiled inputs")
	}
	origin, err := qualificationorigin.NewGitHubWithCIStorageCredential(options.GitHubToken, qualificationorigin.CIStorageCredential{Username: options.CIStorageUsername, Token: options.CIStorageToken}).AuthenticateSource(ctx, options.Source)
	if err != nil {
		return nil, nil, err
	}
	receipt, report, err := consumer.CheckQualification(ctx, QualificationRequest{Reference: candidate.Reference,
		Definition: definition, Environment: environment, Plan: plan, Origin: origin, Predicates: candidate.Predicates})
	if err != nil {
		_ = origin.Close()
	} else {
		// This adapter created the source. Transfer its revocation lifetime to the
		// returned receipt; plain Consumer callers retain their own source owner.
		receipt.lifetime.closeSource = origin.Close
	}
	return receipt, report, err
}

func readQualificationCandidate(ctx context.Context, path string) (QualificationCandidate, error) {
	const limit = 32 << 20
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "qualification-candidate", MaxBytes: limit})
	if err != nil {
		return QualificationCandidate{}, e.Wrap(e.CodeProvenanceMissing, err, "opening qualification candidate")
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, limit)
	if err != nil {
		return QualificationCandidate{}, e.Wrap(e.CodeProvenanceMissing, err, "reading qualification candidate")
	}
	var candidate QualificationCandidate
	if err := canonical.UnmarshalExact(raw, &candidate); err != nil {
		return QualificationCandidate{}, e.Wrap(e.CodeProvenanceMissing, err, "decoding qualification candidate")
	}
	if candidate.SchemaVersion != QualificationCandidateSchema || candidate.Reference == "" || len(candidate.Predicates) == 0 {
		return QualificationCandidate{}, e.New(e.CodeProvenanceMissing, "qualification candidate has an unsupported schema or missing original evidence")
	}
	return candidate, nil
}
