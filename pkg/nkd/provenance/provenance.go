// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package provenance emits deterministic SLSA build provenance per built artifact
// (ADR-09). Each statement is an in-toto v1 Statement carrying an
// https://slsa.dev/provenance/v1 predicate; build-time fields are OMITTED so the
// provenance is reproducible and its digest is a stable expected-evidence digest in
// the ValidationVector (ADR-05). `runDetails.builder.id` is the builder-profile
// identity (the reproducible "who built it"), never a host.
//
// SLSA Provenance v1 makes startedOn/finishedOn optional; the required fields are
// buildDefinition.buildType, buildDefinition.externalParameters, and
// runDetails.builder.id — all present here — so omitting build times is spec-valid.
//
// Production provenance records invocation times separately from reproducible
// evidence. A signed host-observations attestation remains deferred in ADR-09 §2.
package provenance

import (
	"regexp"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

var (
	bareSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)
	gitCommit  = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// BuildType URIs identify the NKD build lane a statement describes.
const (
	BuildTypeOCIHermetic = "https://nkd.dev/buildtypes/oci-hermetic/v2"
	BuildTypeNix         = "https://nkd.dev/buildtypes/nix/v2"
	BuildTypePrebuilt    = "https://nkd.dev/buildtypes/prebuilt-acquire/v2"

	statementType = "https://in-toto.io/Statement/v1"
	predicateType = "https://slsa.dev/provenance/v1"
)

// Subject is the artifact a statement is about (in-toto subject).
type Subject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"` // {"sha256": "<hex>"}
}

// Dependency is one pinned resolved input (source or toolchain).
type Dependency struct {
	URI    string            `json:"uri"`
	Digest map[string]string `json:"digest,omitempty"`
}

// BuildDefinition is the SLSA buildDefinition (times excluded by construction).
//
// InternalParameters is populated ONLY by the production emitter in
// production.go; reproduction statements omit it entirely (so their bytes and
// digests are unchanged) and Index.Validate rejects any reproduction statement
// that carries one.
type BuildDefinition struct {
	BuildType            string            `json:"buildType"`
	ExternalParameters   map[string]string `json:"externalParameters"`
	InternalParameters   map[string]string `json:"internalParameters,omitempty"`
	ResolvedDependencies []Dependency      `json:"resolvedDependencies,omitempty"`
}

// Builder identifies the reproducible builder — the profile identity, not a host.
type Builder struct {
	ID string `json:"id"`
}

// RunDetails carries the builder id and an invocation id, but NO build times.
type RunDetails struct {
	Builder  Builder           `json:"builder"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Predicate is the slsa provenance/v1 predicate body.
type Predicate struct {
	BuildDefinition BuildDefinition `json:"buildDefinition"`
	RunDetails      RunDetails      `json:"runDetails"`
}

// Statement is one in-toto v1 statement with an SLSA provenance predicate.
type Statement struct {
	Type          string    `json:"_type"`
	Subject       []Subject `json:"subject"`
	PredicateType string    `json:"predicateType"`
	Predicate     Predicate `json:"predicate"`
}

// Artifact is the minimal per-built-artifact input the emitter needs.
type Artifact struct {
	Component   string
	SHA256      string // "sha256:<hex>" or "<hex>"
	InputDigest string // observed resolved-input identity for from-source builds
	BuilderType string // oci-hermetic | nix | "" (prebuilt)
	Deps        []Dependency
}

// ForArtifact builds the deterministic SLSA statement for one built artifact.
func ForArtifact(buildDigest, profileDigest string, a Artifact) Statement {
	deps := append([]Dependency(nil), a.Deps...)
	sort.Slice(deps, func(i, j int) bool { return deps[i].URI < deps[j].URI })
	builderID := orNone(profileDigest)
	if a.BuilderType == "" {
		builderID = "none"
	}
	return Statement{
		Type:          statementType,
		PredicateType: predicateType,
		Subject:       []Subject{{Name: a.Component, Digest: map[string]string{"sha256": hexOf(a.SHA256)}}},
		Predicate: Predicate{
			BuildDefinition: BuildDefinition{
				BuildType: buildTypeFor(a.BuilderType),
				ExternalParameters: map[string]string{
					"buildDigest": buildDigest,
					"component":   a.Component,
					"inputDigest": a.InputDigest,
				},
				ResolvedDependencies: deps,
			},
			RunDetails: RunDetails{
				Builder:  Builder{ID: builderID},
				Metadata: map[string]string{"invocationId": buildDigest + "/" + a.Component},
			},
		},
	}
}

// Index is the aggregate over every per-artifact statement, whose digest is a single
// expected-evidence digest in the ValidationVector (ADR-05 §1).
type Index struct {
	SchemaVersion string      `json:"schemaVersion"`
	BuildDigest   string      `json:"buildDigest"`
	Statements    []Statement `json:"statements"`
}

const IndexSchemaVersion = "nkd.dev/provenance-index/v2"

// BuildIndex emits one statement per artifact and returns the deterministic index
// (statements sorted by subject name).
func BuildIndex(buildDigest, profileDigest string, artifacts []Artifact) *Index {
	stmts := make([]Statement, 0, len(artifacts))
	for _, a := range artifacts {
		stmts = append(stmts, ForArtifact(buildDigest, profileDigest, a))
	}
	sort.Slice(stmts, func(i, j int) bool { return stmts[i].Subject[0].Name < stmts[j].Subject[0].Name })
	return &Index{SchemaVersion: IndexSchemaVersion, BuildDigest: buildDigest, Statements: stmts}
}

// Digest is the aggregate provenance evidence digest (ADR-01 domain-separated).
func (ix *Index) Digest() (string, error) {
	if err := ix.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindProvenance, ix)
}

// Validate enforces the semantic SLSA subset used as release evidence. It rejects
// duplicate subjects, unknown build types, malformed digests, mutable dependencies,
// and builder-type/profile erasure before an index can acquire an identity.
func (ix *Index) Validate() error {
	if ix == nil {
		return e.New(e.CodeProvenanceMissing, "provenance index is required")
	}
	if ix.SchemaVersion != IndexSchemaVersion {
		return e.New(e.CodeInvalidCatalog,
			"provenance schemaVersion %q is unsupported (want %q)", ix.SchemaVersion, IndexSchemaVersion)
	}
	if !canonical.IsDigest(ix.BuildDigest) {
		return e.New(e.CodeUnverifiedRef,
			"provenance buildDigest %q is not an exact lowercase sha256 identity", ix.BuildDigest)
	}
	if len(ix.Statements) == 0 {
		return e.New(e.CodeProvenanceMissing, "provenance index contains no artifact statements")
	}
	previousSubject := ""
	for statementIndex, statement := range ix.Statements {
		if statement.Type != statementType || statement.PredicateType != predicateType {
			return e.New(e.CodeInvalidCatalog,
				"provenance statement %d has an unsupported statement or predicate type", statementIndex)
		}
		if len(statement.Subject) != 1 {
			return e.New(e.CodeProvenanceMissing,
				"provenance statement %d must bind exactly one artifact subject", statementIndex)
		}
		subject := statement.Subject[0]
		if strings.TrimSpace(subject.Name) == "" || strings.TrimSpace(subject.Name) != subject.Name {
			return e.New(e.CodeInvalidCatalog,
				"provenance statement %d has a non-canonical subject name", statementIndex)
		}
		if previousSubject != "" && subject.Name <= previousSubject {
			return e.New(e.CodeInvalidCatalog,
				"provenance subjects must be strictly sorted and duplicate-free")
		}
		previousSubject = subject.Name
		if len(subject.Digest) != 1 || !bareSHA256.MatchString(subject.Digest["sha256"]) {
			return e.New(e.CodeUnverifiedRef,
				"provenance subject %q does not bind one exact sha256 digest", subject.Name)
		}

		definition := statement.Predicate.BuildDefinition
		fromSource := definition.BuildType == BuildTypeOCIHermetic || definition.BuildType == BuildTypeNix
		if !fromSource && definition.BuildType != BuildTypePrebuilt {
			return e.New(e.CodeInvalidCatalog,
				"provenance subject %q has unknown buildType %q", subject.Name, definition.BuildType)
		}
		if len(definition.InternalParameters) != 0 {
			return e.New(e.CodeUnverifiedRef,
				"reproduction provenance subject %q must not carry build-time internal parameters", subject.Name)
		}
		parameters := definition.ExternalParameters
		if len(parameters) != 3 || parameters["buildDigest"] != ix.BuildDigest ||
			parameters["component"] != subject.Name {
			return e.New(e.CodeUnverifiedRef,
				"provenance subject %q does not bind the exact build/component parameters", subject.Name)
		}
		inputDigest := parameters["inputDigest"]
		builderID := statement.Predicate.RunDetails.Builder.ID
		if fromSource {
			if !canonical.IsDigest(inputDigest) || !canonical.IsDigest(builderID) {
				return e.New(e.CodeProvenanceMissing,
					"from-source provenance subject %q requires exact input and builder-profile digests", subject.Name)
			}
		} else if inputDigest != "" || builderID != "none" {
			return e.New(e.CodeUnverifiedRef,
				"prebuilt provenance subject %q must be profile-independent and have no source-input digest", subject.Name)
		}
		metadata := statement.Predicate.RunDetails.Metadata
		if len(metadata) != 1 || metadata["invocationId"] != ix.BuildDigest+"/"+subject.Name {
			return e.New(e.CodeUnverifiedRef,
				"provenance subject %q has an invalid invocation binding", subject.Name)
		}
		previousDependency := ""
		for _, dependency := range definition.ResolvedDependencies {
			if strings.TrimSpace(dependency.URI) == "" || strings.TrimSpace(dependency.URI) != dependency.URI ||
				(previousDependency != "" && dependency.URI <= previousDependency) {
				return e.New(e.CodeUnpinnedInput,
					"provenance dependencies for %q must have canonical, strictly sorted, unique URIs", subject.Name)
			}
			previousDependency = dependency.URI
			if !validDependencyIdentity(dependency) {
				return e.New(e.CodeUnpinnedInput,
					"provenance dependency %q for %q has no supported exact content identity", dependency.URI, subject.Name)
			}
		}
	}
	return nil
}

func validDependencyIdentity(dependency Dependency) bool {
	if len(dependency.Digest) == 0 {
		return strings.Contains(dependency.URI, "@sha256:") &&
			canonical.IsDigest(dependency.URI[strings.LastIndex(dependency.URI, "@")+1:])
	}
	if len(dependency.Digest) != 1 {
		return false
	}
	if digest, ok := dependency.Digest["sha256"]; ok {
		return bareSHA256.MatchString(digest)
	}
	if digest, ok := dependency.Digest["gitCommit"]; ok {
		return gitCommit.MatchString(digest)
	}
	if digest, ok := dependency.Digest["gitTree"]; ok {
		return gitCommit.MatchString(digest)
	}
	return false
}

func buildTypeFor(builderType string) string {
	switch builderType {
	case "nix":
		return BuildTypeNix
	case "oci-hermetic":
		return BuildTypeOCIHermetic
	case "":
		return BuildTypePrebuilt
	default:
		// Preserve invalidity so Index.Validate rejects an unknown declared
		// builder instead of silently relabeling it as a prebuilt acquisition.
		return ""
	}
}

func hexOf(s string) string {
	if len(s) > 7 && s[:7] == "sha256:" {
		return s[7:]
	}
	return s
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
