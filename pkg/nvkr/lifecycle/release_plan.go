// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"regexp"
	"runtime"
	"slices"
	"sort"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// ReleasePlanSchemaVersion is the schema of ReleasePlanDescription.
const ReleasePlanSchemaVersion = "nvkr.dev/release-plan/v1"

// PlanSource is one source the release was built from: a repository at the exact
// commit and tag its catalog pin resolved, with the components built from it.
// The same repository at one commit appears once for each distinct tag, because
// a monorepo's modules are tagged separately.
type PlanSource struct {
	Repo       string   `json:"repo"`
	Commit     string   `json:"commit"`
	TreeHash   string   `json:"treeHash,omitempty"`
	Tag        string   `json:"tag,omitempty"`
	Components []string `json:"components,omitempty"`
}

// ReleasePlanDescription describes the build Plan a published release was
// produced from. Its sources are read from Plan bytes whose digest equals the
// plan digest in the release's signed metadata, so they are what the producing
// Run compiled.
//
// It is a description. It does not authenticate any output signature, evidence
// bundle or payload, and it issues no receipt, so it grants no verification,
// qualification or release authority.
type ReleasePlanDescription struct {
	SchemaVersion string                          `json:"schemaVersion"`
	Release       string                          `json:"release"`
	PlanDigest    string                          `json:"planDigest"`
	Origin        qualificationorigin.Description `json:"origin"`
	Sources       []PlanSource                    `json:"sources"`
}

// PublishedOutput is one output the release's signed metadata lists, which is
// what the producing Run published and not what its Plan declared. Its
// reference and digest are pinned by the verified root, so a pull by digest
// authenticates the bytes, but the output's own signature and evidence are not
// verified here. Components names what the output was built from, so a caller
// can join it with the Plan's sources.
type PublishedOutput struct {
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Reference  string   `json:"reference"`
	Digest     string   `json:"digest"`
	Platform   string   `json:"platform,omitempty"`
	Components []string `json:"components,omitempty"`
}

// ReleaseBuild pairs what a release was built from with what it
// published. The two come from different places. Plan is read from the
// producing attempt's handoff and bound to the Plan digest in the signed root.
// Outputs are read from the metadata the same root binds. Neither is a receipt.
type ReleaseBuild struct {
	Plan    ReleasePlanDescription `json:"plan"`
	Outputs []PublishedOutput      `json:"outputs"`
}

// releaseRootClaims are the facts the release root's signed metadata states.
type releaseRootClaims struct {
	PlanDigest   string
	InvocationID string
	Outputs      []PublishedOutput
}

// authenticateRootFunc verifies the release root under a trust policy and
// returns what its metadata claims.
type authenticateRootFunc func(context.Context, string, VerificationPolicy) (releaseRootClaims, error)

// openHandoffFunc authenticates the exact source attempt and returns its origin
// with the handoff bytes. The callback closes the source.
type openHandoffFunc func(context.Context, qualificationorigin.RunLocator) (qualificationorigin.Description, []byte, func() error, error)

// ReadReleaseBuild reads what a published release was built from and what it
// published, without verifying each output.
//
// It verifies the release root's signature under the policy's producer identity,
// reads the plan digest and producing attempt from the metadata that root binds,
// authenticates that attempt, downloads its handoff, and requires the handoff's
// Plan to have exactly that digest. The check costs one signature verification
// and one handoff download, where the artifact check verifies every output. The
// outputs are those the verified root's metadata lists.
//
// The handoff is a CI artifact and expires. A release whose handoff is gone
// cannot be read this way.
func ReadReleaseBuild(ctx context.Context, reference, policyPath string, options QualificationRuntimeOptions) (ReleaseBuild, error) {
	if err := admitQualificationRun(ctx); err != nil {
		return ReleaseBuild{}, err
	}
	if err := admitQualificationReference(reference); err != nil {
		return ReleaseBuild{}, err
	}
	if err := admitQualificationReferenceTransport(reference, options); err != nil {
		return ReleaseBuild{}, err
	}
	policy, err := LoadQualificationPolicy(ctx, policyPath)
	if err != nil {
		return ReleaseBuild{}, err
	}
	binary := policy.Cosign.Path
	if binary == "" {
		binary = sign.DefaultBinary
	}
	digest, err := policy.Cosign.ExecutableDigest("linux/" + runtime.GOARCH)
	if err != nil {
		return ReleaseBuild{}, err
	}
	pinned, err := sign.NewPinnedCosignSigner(binary, digest)
	if err != nil {
		return ReleaseBuild{}, err
	}
	if err := pinned.Preflight(ctx); err != nil {
		return ReleaseBuild{}, err
	}
	consumer, err := newQualificationConsumer(reference, policy, options)
	if err != nil {
		return ReleaseBuild{}, err
	}
	provider := qualificationorigin.NewGitHubWithCIStorageCredential(options.GitHubToken,
		qualificationorigin.CIStorageCredential{Username: options.CIStorageUsername, Token: options.CIStorageToken})
	return readReleaseBuildWithPolicy(ctx, reference, policy, consumer.authenticateRoot,
		func(ctx context.Context, locator qualificationorigin.RunLocator) (qualificationorigin.Description, []byte, func() error, error) {
			source, err := provider.AuthenticateRun(ctx, locator, policy.Source)
			if err != nil {
				return qualificationorigin.Description{}, nil, nil, err
			}
			origin, err := source.Description()
			if err != nil {
				_ = source.Close()
				return qualificationorigin.Description{}, nil, nil, err
			}
			raw, err := provider.DownloadQualificationHandoff(ctx, source)
			if err != nil {
				_ = source.Close()
				return qualificationorigin.Description{}, nil, nil, err
			}
			return origin, raw, source.Close, nil
		})
}

// readReleaseBuildWithPolicy keeps ownership and exact-binding checks in one
// production path. Its private callbacks cannot issue public authority.
func readReleaseBuildWithPolicy(ctx context.Context, reference string, policy QualificationPolicy,
	authenticateRoot authenticateRootFunc, openHandoff openHandoffFunc,
) (build ReleaseBuild, err error) {
	if ctx == nil {
		return ReleaseBuild{}, e.New(e.CodeUsage, "release plan reading requires context")
	}
	if err := authenticityContext(ctx); err != nil {
		return ReleaseBuild{}, err
	}
	if err := admitQualificationReference(reference); err != nil {
		return ReleaseBuild{}, err
	}
	if err := policy.Validate(); err != nil {
		return ReleaseBuild{}, err
	}
	if authenticateRoot == nil || openHandoff == nil {
		return ReleaseBuild{}, e.New(e.CodeInternal, "release plan dependencies are incomplete")
	}
	builder := "https://github.com/" + policy.Source.Repository + "/" + policy.Source.Workflow + "@refs/heads/" + policy.Source.DefaultBranch
	trust := VerificationPolicy{Lane: LanePipeline, Scope: ScopeAuthenticity, IdentityRegexp: "^" + regexp.QuoteMeta(builder) + "$", Issuer: "https://token.actions.githubusercontent.com"}
	claims, err := authenticateRoot(ctx, reference, trust)
	if err != nil {
		return ReleaseBuild{}, err
	}
	if claims.InvocationID == "" {
		return ReleaseBuild{}, e.New(e.CodeProvenanceMissing, "authenticated publication root omits its source invocation")
	}
	if !canonical.IsDigest(claims.PlanDigest) {
		return ReleaseBuild{}, e.New(e.CodeProvenanceMissing, "authenticated publication root omits its plan digest")
	}
	locator, err := qualificationorigin.ParseRunURL(claims.InvocationID)
	if err != nil {
		return ReleaseBuild{}, e.Wrap(e.CodeProvenanceMissing, err, "authenticated publication has no exact source attempt")
	}
	if locator.Repository != policy.Source.Repository {
		return ReleaseBuild{}, e.New(e.CodeUntrustedSigner, "authenticated publication source differs from the selected policy")
	}
	origin, raw, closeSource, err := openHandoff(ctx, locator)
	if err != nil {
		return ReleaseBuild{}, err
	}
	defer func() {
		if closeErr := closeSource(); closeErr != nil && err == nil {
			build, err = ReleaseBuild{}, e.Wrap(e.CodeGateUnmet, closeErr, "closing the source attempt")
		}
	}()
	plan, err := decodeReleasePlan(raw, reference, origin, claims.PlanDigest)
	if err != nil {
		return ReleaseBuild{}, err
	}
	sources, err := planSources(plan)
	if err != nil {
		return ReleaseBuild{}, err
	}
	if err := authenticityContext(ctx); err != nil {
		return ReleaseBuild{}, err
	}
	return ReleaseBuild{
		Plan:    ReleasePlanDescription{SchemaVersion: ReleasePlanSchemaVersion, Release: reference, PlanDigest: claims.PlanDigest, Origin: origin, Sources: sources},
		Outputs: claims.Outputs,
	}, nil
}

// decodeReleasePlan decodes the handoff and returns its Plan only when the
// handoff belongs to the authenticated attempt and selected release and the
// Plan has the digest the release's signed metadata states. It replays no
// compiler input, which is the artifact check's job.
func decodeReleasePlan(raw []byte, reference string, origin qualificationorigin.Description, planDigest string) (*factory.Plan, error) {
	if len(raw) == 0 || len(raw) > qualificationorigin.MaxQualificationHandoffBytes {
		return nil, e.New(e.CodeProvenanceMissing, "qualification handoff is empty or exceeds its byte limit")
	}
	var handoff QualificationHandoff
	if err := canonical.UnmarshalExact(raw, &handoff); err != nil {
		return nil, e.Wrap(e.CodeProvenanceMissing, err, "decoding the source handoff")
	}
	if handoff.SchemaVersion != QualificationHandoffSchema || handoff.Source != origin {
		return nil, e.New(e.CodeVerifyFailed, "the source handoff differs from the exact authenticated attempt")
	}
	if handoff.Candidate.SchemaVersion != QualificationCandidateSchema || handoff.Candidate.Reference != reference {
		return nil, e.New(e.CodeVerifyFailed, "the source handoff does not bind the selected release")
	}
	if len(handoff.Plan) == 0 || int64(len(handoff.Plan)) > factory.MaxPlanBytes {
		return nil, e.New(e.CodeProvenanceMissing, "the source handoff Plan is empty or exceeds its byte limit")
	}
	var plan factory.Plan
	if err := canonical.UnmarshalExact(handoff.Plan, &plan); err != nil {
		return nil, e.Wrap(e.CodeProvenanceMissing, err, "decoding the source handoff Plan")
	}
	got, err := plan.IdentityDigest()
	if err != nil {
		return nil, err
	}
	if got != planDigest {
		return nil, e.New(e.CodeVerifyFailed, "the source handoff Plan differs from the plan digest in the signed release metadata")
	}
	return &plan, nil
}

// planSources lists the sources the Plan built from, read from each build's
// resolved components. A component built from source records the repository,
// tag, commit and tree hash its pin resolved, and the Plan carries them.
func planSources(plan *factory.Plan) ([]PlanSource, error) {
	type key struct{ repo, tag, commit, tree string }
	found := map[key]*PlanSource{}
	treeFor := map[[2]string]string{}
	for _, build := range plan.Builds {
		for _, named := range build.ResolvedComponents {
			c := named.Component
			if !c.FromSource || c.Repo == "" || c.Commit == "" {
				continue
			}
			if previous, ok := treeFor[[2]string{c.Repo, c.Commit}]; ok && previous != c.TreeHash {
				return nil, e.New(e.CodeVerifyFailed, "the release Plan pins %s at %s with two tree hashes", c.Repo, c.Commit)
			}
			treeFor[[2]string{c.Repo, c.Commit}] = c.TreeHash
			k := key{c.Repo, c.Tag, c.Commit, c.TreeHash}
			entry, ok := found[k]
			if !ok {
				entry = &PlanSource{Repo: c.Repo, Tag: c.Tag, Commit: c.Commit, TreeHash: c.TreeHash}
				found[k] = entry
			}
			if !slices.Contains(entry.Components, named.Name) {
				entry.Components = append(entry.Components, named.Name)
			}
		}
	}
	if len(found) == 0 {
		return nil, e.New(e.CodeProvenanceMissing, "the release Plan built no component from a pinned source")
	}
	sources := make([]PlanSource, 0, len(found))
	for _, entry := range found {
		sort.Strings(entry.Components)
		sources = append(sources, *entry)
	}
	sort.Slice(sources, func(i, j int) bool {
		a, b := sources[i], sources[j]
		if a.Repo != b.Repo {
			return a.Repo < b.Repo
		}
		if a.Commit != b.Commit {
			return a.Commit < b.Commit
		}
		return a.Tag < b.Tag
	})
	return sources, nil
}

// authenticateRoot verifies the release root's signature under policy and reads
// the claims of the metadata it binds. It does not verify any output signature,
// evidence bundle or payload, so its result is not an AuthenticatedRelease and
// cannot stand in for one.
func (consumer *Consumer) authenticateRoot(ctx context.Context, reference string, policy VerificationPolicy) (releaseRootClaims, error) {
	if ctx == nil {
		return releaseRootClaims{}, e.New(e.CodeUsage, "release root authentication requires a context")
	}
	if err := authenticityContext(ctx); err != nil {
		return releaseRootClaims{}, err
	}
	if err := policy.Validate(); err != nil {
		return releaseRootClaims{}, err
	}
	if policy.Lane != LanePipeline || policy.Scope != ScopeAuthenticity {
		return releaseRootClaims{}, e.New(e.CodeEngineCapability, "release root authentication requires pipeline-attested authenticity scope")
	}
	if consumer == nil || (consumer.verifier == nil && consumer.verifierFor == nil) {
		return releaseRootClaims{}, e.New(e.CodeEngineCapability, "release root authentication requires a configured signature adapter")
	}
	if err := consumer.inspection.Registry.Validate(); err != nil {
		return releaseRootClaims{}, err
	}
	verifier := consumer.verifier
	if verifier == nil {
		var err error
		if verifier, err = consumer.verifierFor(policy); err != nil {
			return releaseRootClaims{}, err
		}
	}
	timeout := consumer.inspection.Timeout
	if timeout < 0 {
		return releaseRootClaims{}, e.New(e.CodeUsage, "release root authentication timeout must be positive")
	}
	if timeout == 0 {
		timeout = defaultInspectionTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := authenticateReleaseSubject(ctx, verifier, reference, policy); err != nil {
		return releaseRootClaims{}, err
	}
	// A nil evidence callback reads the metadata the verified root binds by
	// digest without verifying each evidence bundle.
	metadata, err := inspectReleaseMetadata(ctx, reference, consumer.inspection, nil)
	if err != nil {
		return releaseRootClaims{}, err
	}
	if err := authenticityContext(ctx); err != nil {
		return releaseRootClaims{}, err
	}
	outputs, err := rootOutputs(metadata.Outputs)
	if err != nil {
		return releaseRootClaims{}, err
	}
	return releaseRootClaims{PlanDigest: metadata.Metadata.PlanDigest, InvocationID: metadata.Metadata.InvocationID, Outputs: outputs}, nil
}

// rootOutputs lists the outputs the verified root's metadata binds, sorted by
// name. An output with no name, no reference or no digest is refused, because a
// pull by digest needs all three.
func rootOutputs(listed []delivery.PublicationOutput) ([]PublishedOutput, error) {
	outputs := make([]PublishedOutput, 0, len(listed))
	seen := map[string]bool{}
	for _, o := range listed {
		if o.Name == "" || o.Reference == "" || !canonical.IsDigest(o.Digest) {
			return nil, e.New(e.CodeProvenanceMissing, "release output %q lacks a name, a reference or a digest", o.Name)
		}
		if seen[o.Name] {
			return nil, e.New(e.CodeAmbiguous, "the release lists output %q more than once", o.Name)
		}
		seen[o.Name] = true
		output := PublishedOutput{Name: o.Name, Kind: o.Kind, Reference: o.Reference, Digest: o.Digest, Components: slices.Clone(o.Components)}
		if o.OS != "" && o.Arch != "" {
			output.Platform = o.OS + "/" + o.Arch
		}
		outputs = append(outputs, output)
	}
	sort.Slice(outputs, func(i, j int) bool { return outputs[i].Name < outputs[j].Name })
	return outputs, nil
}
