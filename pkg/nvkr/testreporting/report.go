// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package testreporting projects fresh authenticated portable observations into
// passive discovery and reporting descriptions. Exported JSON cannot recreate
// receipts, authorize qualification, establish current cluster health or prove
// TestGrid publication. The package performs no suite execution or service writes.
package testreporting

import (
	"context"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/safetext"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
	"github.com/google/go-containerregistry/pkg/name"
)

const SchemaVersion = "nvkr.dev/test-report-index/v1"
const MaxInputs = 128

// Bound retained descriptor pressure independently from covered-byte sizes.
const MaxHeldFiles = 512
const MaxCoveredBytes int64 = testobservation.MaxTotalBytes
const FirstParty = "first-party"
const Community = "community"

// Admission is independently reviewed configuration; no destination, tier or
// signer is inferred from a locator, certificate claim, historical report or tag.
type Admission struct {
	Identity         string `json:"identity"`
	Issuer           string `json:"issuer"`
	Tier             string `json:"tier"`
	Namespace        string `json:"namespace"`
	SourcePath       string `json:"sourcePath"`
	DefinitionDigest string `json:"definitionDigest"`
	Revoked          bool   `json:"revoked"`
}
type Input struct {
	Locator   testobservation.Locator  `json:"locator"`
	Expected  testobservation.Expected `json:"expected"`
	Admission Admission                `json:"admission"`
}

// Entry is a generated description of one freshly verified observation. Signer
// projection tokens are deterministic grouping labels; they establish no authority.
type Entry struct {
	Namespace             string                      `json:"namespace"`
	SourcePath            string                      `json:"sourcePath"`
	Tier                  string                      `json:"tier"`
	Identity              string                      `json:"identity"`
	Issuer                string                      `json:"issuer"`
	SignerProjectionToken string                      `json:"signerProjectionToken,omitempty"`
	Reference             string                      `json:"reference"`
	TransportDigest       string                      `json:"transportDigest"`
	ObservationDigest     string                      `json:"observationDigest"`
	Passing               bool                        `json:"passing"`
	Observation           testobservation.Observation `json:"observation"`
	Coverage              Coverage                    `json:"coverage"`
	Reproduction          Reproduction                `json:"reproduction"`
}
type Coverage struct {
	Required  int      `json:"required"`
	Total     int      `json:"total"`
	Selection []string `json:"selection"`
}
type Reproduction struct {
	HarnessRevision string   `json:"harnessRevision"`
	ImageDigest     string   `json:"imageDigest"`
	Selection       []string `json:"selection"`
	PolicyDigest    string   `json:"policyDigest"`
}
type Index struct {
	SchemaVersion string  `json:"schemaVersion"`
	Scope         string  `json:"scope"`
	Entries       []Entry `json:"entries"`
}
type receipt interface {
	Observation(context.Context) (testobservation.Observation, error)
	Description(context.Context) (testobservation.Verification, error)
	Signer(context.Context) (string, string, error)
	Read(context.Context, string) ([]byte, error)
	Close() error
}
type verifyFunc func(context.Context, Input, testobservation.RegistryOptions) (receipt, error)
type retained struct {
	input   Input
	receipt receipt
}

// Report has no public constructor or deserializer. Fresh receipt lifetime and
// request cancellation gate every projection; a detached Index is never input.
type Report struct {
	mu       sync.Mutex
	ctx      context.Context
	receipts []retained
	closed   bool
}

func failure(format string, args ...any) error { return e.New(e.CodeVerifyFailed, format, args...) }
func checkContext(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "test reporting requires context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "test reporting canceled")
	}
	return nil
}
func text(value string) bool { return safetext.Valid(value, 4096, false) }

func safePath(value string) bool {
	if value == "" || len(value) > 1024 || strings.Count(value, "/") > 8 {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
				return false
			}
		}
	}
	return true
}
func validateInput(input Input, options testobservation.RegistryOptions) error {
	a := input.Admission
	if a.Tier == FirstParty && strings.HasPrefix(a.SourcePath, "community/") {
		return failure("first-party source cannot use the community path namespace")
	}
	if a.Revoked {
		return e.New(e.CodeUntrustedSigner, "configured observation source is revoked")
	}
	if !text(a.Identity) || !text(a.Issuer) || strings.TrimSpace(a.Identity) != a.Identity || strings.TrimSpace(a.Issuer) != a.Issuer || (a.Tier != FirstParty && a.Tier != Community) || !safePath(a.SourcePath) || !canonical.IsDigest(a.DefinitionDigest) || a.DefinitionDigest != input.Expected.Inputs.DefinitionDigest {
		return failure("independent signer, tier, source path and exact Definition admission required")
	}
	if options.Identity != "" && options.Identity != a.Identity || options.Issuer != "" && options.Issuer != a.Issuer {
		return e.New(e.CodeUntrustedSigner, "registry signer expectations contradict source admission")
	}
	namespace, err := name.NewRepository(a.Namespace, name.StrictValidation)
	if err != nil || namespace.Name() != a.Namespace || !strings.Contains(a.Namespace, "/") {
		return failure("explicit canonical registry namespace required")
	}
	if err := input.Locator.Validate(); err != nil {
		return err
	}
	if err := input.Expected.Validate(); err != nil {
		return err
	}
	if input.Locator.ObservationDigest != input.Expected.ObservationDigest {
		return failure("locator differs from independently selected observation")
	}
	ref, err := name.NewDigest(input.Locator.Reference, name.StrictValidation)
	if err != nil || ref.Context().Name() != a.Namespace+"/"+a.SourcePath {
		return failure("observation locator is outside exact reviewed source repository")
	}
	selected := options
	selected.Identity = a.Identity
	selected.Issuer = a.Issuer
	if err := selected.ValidateReference(input.Locator.Reference); err != nil {
		return err
	}
	return nil
}

// ProjectionSourcePath prepares a community contribution path from independently
// selected configuration. The hash is a deterministic grouping label, not an authenticated
// identity. Ingest recomputes it only after signer authentication before counting.
func ProjectionSourcePath(definitionDigest, identity, issuer string) (string, error) {
	if !canonical.IsDigest(definitionDigest) || !text(identity) || !text(issuer) || strings.TrimSpace(identity) != identity || strings.TrimSpace(issuer) != issuer {
		return "", failure("projection requires exact Definition and signer configuration")
	}
	token, err := canonical.Digest(canonical.KindCommunitySignerLabel, struct {
		Identity string `json:"identity"`
		Issuer   string `json:"issuer"`
	}{identity, issuer})
	if err != nil {
		return "", err
	}
	token = strings.TrimPrefix(token, "sha256:")
	return "community/" + strings.TrimPrefix(definitionDigest, "sha256:") + "/signer-" + token, nil
}

// Ingest authenticates every supplied input through the stock OCI verifier.
// Duplicate inputs are verified before deduplication within the admitted source.
func stockVerify(ctx context.Context, input Input, options testobservation.RegistryOptions) (receipt, error) {
	return testobservation.VerifyOCI(ctx, input.Locator.Reference, input.Expected, options)
}
func Ingest(ctx context.Context, inputs []Input, options testobservation.RegistryOptions) (*Report, error) {
	return ingestWith(ctx, inputs, options, func(ctx context.Context, input Input, options testobservation.RegistryOptions) (receipt, error) {
		return testobservation.VerifyOCI(ctx, input.Locator.Reference, input.Expected, options)
	})
}
func ingestWith(ctx context.Context, inputs []Input, options testobservation.RegistryOptions, verify verifyFunc) (*Report, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if len(inputs) == 0 || len(inputs) > MaxInputs || verify == nil {
		return nil, e.New(e.CodeUsage, "reporting requires 1 through %d independently admitted inputs", MaxInputs)
	}
	sources := map[string]Admission{}
	for _, input := range inputs {
		if err := validateInput(input, options); err != nil {
			return nil, err
		}
		a := input.Admission
		key := a.Namespace + "\x00" + a.SourcePath
		if prior, ok := sources[key]; ok && (prior.Tier != a.Tier || prior.DefinitionDigest != a.DefinitionDigest || prior.Identity != a.Identity || prior.Issuer != a.Issuer) {
			return nil, failure("conflicting tier, Definition or signer for reviewed source")
		}
		sources[key] = a
	}
	result := &Report{ctx: ctx}
	complete := false
	defer func() {
		if !complete {
			_ = result.Close()
		}
	}()
	var retainedBytes int64
	var retainedFiles int
	for _, input := range inputs {
		if err := checkContext(ctx); err != nil {
			return nil, err
		}
		selected := options
		selected.Identity = input.Admission.Identity
		selected.Issuer = input.Admission.Issuer
		verified, err := verify(ctx, input, selected)
		if err != nil {
			return nil, err
		}
		if verified == nil {
			return nil, failure("verifier returned no authenticated observation")
		}
		owned := retained{input: cloneInput(input), receipt: verified}
		result.receipts = append(result.receipts, owned)
		entry, err := projectEntry(ctx, owned)
		if err != nil {
			return nil, err
		}
		raw, err := entry.Observation.Canonical()
		if err != nil {
			return nil, err
		}
		observedBytes := int64(len(raw))
		for _, file := range entry.Observation.Files {
			observedBytes += file.Size
		}
		if observedBytes > MaxCoveredBytes-retainedBytes {
			if err := verified.Close(); err != nil {
				return nil, e.Wrap(e.CodeUnavailable, err, "closing excess authenticated report inventory")
			}
			return nil, failure("authenticated report inventories exceed cumulative covered-byte budget")
		}
		observedFiles := 1 + len(entry.Observation.Files)
		if observedFiles > MaxHeldFiles-retainedFiles {
			if err := verified.Close(); err != nil {
				return nil, e.Wrap(e.CodeUnavailable, err, "closing excess report descriptors")
			}
			return nil, failure("authenticated report inventories exceed cumulative held-file budget")
		}
		retainedFiles += observedFiles
		retainedBytes += observedBytes
	}
	if _, err := result.indexLocked(ctx); err != nil {
		return nil, err
	}
	complete = true
	return result, nil
}
func cloneInput(input Input) Input {
	input.Expected.Subjects = slices.Clone(input.Expected.Subjects)
	input.Expected.Suite.Selection = slices.Clone(input.Expected.Suite.Selection)
	input.Expected.Suite.RequiredCases = slices.Clone(input.Expected.Suite.RequiredCases)
	return input
}
func projectEntry(ctx context.Context, held retained) (Entry, error) {
	identity, issuer, err := held.receipt.Signer(ctx)
	if err != nil {
		return Entry{}, err
	}
	a := held.input.Admission
	if identity != a.Identity || issuer != a.Issuer {
		return Entry{}, e.New(e.CodeUntrustedSigner, "authenticated signer differs from exact configured source")
	}
	description, err := held.receipt.Description(ctx)
	if err != nil {
		return Entry{}, err
	}
	if !description.SignerAuthenticated || !description.SelfConsistent || description.ObservationDigest != held.input.Expected.ObservationDigest {
		return Entry{}, failure("receipt does not establish exact authenticated observation")
	}
	observed, err := held.receipt.Observation(ctx)
	if err != nil {
		return Entry{}, err
	}
	if err := held.input.Expected.Check(observed); err != nil {
		return Entry{}, err
	}
	token := ""
	if a.Tier == Community {
		path, err := ProjectionSourcePath(observed.Inputs.DefinitionDigest, identity, issuer)
		if err != nil {
			return Entry{}, err
		}
		if a.SourcePath != path {
			return Entry{}, failure("community source path differs from verified signer and exact Definition projection")
		}
		token = strings.TrimPrefix(path[strings.LastIndex(path, "/")+1:], "signer-")
	}
	parsed, err := name.NewDigest(held.input.Locator.Reference, name.StrictValidation)
	if err != nil {
		return Entry{}, failure("verified reference is not pinned")
	}
	suite := observed.Suite
	return Entry{Namespace: a.Namespace, SourcePath: a.SourcePath, Tier: a.Tier, Identity: identity, Issuer: issuer, SignerProjectionToken: token, Reference: held.input.Locator.Reference, TransportDigest: parsed.DigestStr(), ObservationDigest: description.ObservationDigest, Passing: description.Passing, Observation: observed, Coverage: Coverage{Required: len(suite.RequiredCases), Total: observed.Summary.Total, Selection: slices.Clone(suite.Selection)}, Reproduction: Reproduction{HarnessRevision: suite.HarnessRevision, ImageDigest: suite.ImageDigest, Selection: slices.Clone(suite.Selection), PolicyDigest: suite.PolicyDigest}}, nil
}
func (r *Report) check(ctx context.Context) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if r == nil || r.closed || len(r.receipts) == 0 {
		return failure("report receipt is closed or unavailable")
	}
	return checkContext(r.ctx)
}
func entryKey(entry Entry) string {
	return entry.Namespace + "\x00" + entry.SourcePath + "\x00" + entry.Tier + "\x00" + entry.Identity + "\x00" + entry.Issuer + "\x00" + entry.ObservationDigest
}
func (r *Report) indexLocked(ctx context.Context) (Index, error) {
	if err := r.check(ctx); err != nil {
		return Index{}, err
	}
	index := Index{SchemaVersion: SchemaVersion, Scope: "authenticated historical test observations; no current health or qualification claim", Entries: []Entry{}}
	entries := map[string]Entry{}
	// Defense in depth: an honest signer grouping digest is collision resistant,
	// but a collision still cannot join independently authenticated identities.
	sourceSigners := map[string]string{}
	for _, owned := range r.receipts {
		entry, err := projectEntry(ctx, owned)
		if err != nil {
			return Index{}, err
		}
		if entry.Tier == Community {
			source := entry.Namespace + "\x00" + entry.SourcePath
			signer := entry.Identity + "\x00" + entry.Issuer
			if prior, exists := sourceSigners[source]; exists && prior != signer {
				return Index{}, failure("community deterministic grouping label collides across authenticated signers")
			}
			sourceSigners[source] = signer
		}
		key := entryKey(entry)
		prior, exists := entries[key]
		if !exists || entry.Reference < prior.Reference {
			entries[key] = entry
		}
	}
	for _, entry := range entries {
		index.Entries = append(index.Entries, entry)
	}
	sort.Slice(index.Entries, func(i, j int) bool { return entryKey(index.Entries[i]) < entryKey(index.Entries[j]) })
	if err := r.check(ctx); err != nil {
		return Index{}, err
	}
	return index, nil
}
func (r *Report) Index(ctx context.Context) (Index, error) {
	if r == nil {
		return Index{}, failure("report unavailable")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.indexLocked(ctx)
}
func (r *Report) JSON(ctx context.Context) ([]byte, error) {
	if r == nil {
		return nil, failure("report unavailable")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	index, err := r.indexLocked(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := canonical.Marshal(index)
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxIndexBytes {
		return nil, failure("generated discovery index exceeds byte budget")
	}
	if err := r.check(ctx); err != nil {
		return nil, err
	}
	return raw, nil
}

func (r *Report) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	var first error
	for _, held := range r.receipts {
		if err := held.receipt.Close(); err != nil && first == nil {
			first = e.Wrap(e.CodeUnavailable, err, "closing verified report inputs")
		}
	}
	return first
}
