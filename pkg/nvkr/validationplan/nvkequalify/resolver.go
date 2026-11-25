// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkequalify

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvke/release/pkg/qualification"
	"github.com/dsx-ai-factory/nvke/release/pkg/resolve"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/validationplan"
)

// Strategy names a registry uses for components the authenticated release
// supplies.
const (
	// StrategyVersion resolves a version to the digest of one output of the
	// release, or to the release's own digest when no output is named.
	StrategyVersion = "nvkrReleaseVersion"

	// StrategyLocation resolves a location to the reference of one output.
	StrategyLocation = "nvkrReleaseLocation"

	// StrategyDeclared resolves a version to what the subject declares for it,
	// either a constraint or a value. It reads nothing, so it cannot say whether
	// the declaration matches what the release contains.
	StrategyDeclared = "nvkrDeclared"
)

// withOutput is the only key either strategy takes. It names a release output.
const withOutput = "output"

// strategies returns the two strategies, built over one authenticated release.
func strategies(release validationplan.Release) qualification.Strategies {
	return qualification.Strategies{
		Version: map[string]qualification.VersionFactory{
			StrategyDeclared: func(with map[string]string, _ string) (resolve.Resolver, error) {
				if err := checkDeclaredTable(with); err != nil {
					return nil, err
				}
				return declaredResolver{table: with}, nil
			},
			StrategyVersion: func(with map[string]string, _ string) (resolve.Resolver, error) {
				output, err := outputOf(with)
				if err != nil {
					return nil, err
				}
				if output != "" {
					if _, err := release.Output(output); err != nil {
						return nil, err
					}
				}
				return versionResolver{release: release, output: output}, nil
			},
		},
		Location: map[string]qualification.LocationFactory{
			StrategyLocation: func(with map[string]string, _ resolve.Options) (resolve.Resolver, error) {
				output, err := outputOf(with)
				if err != nil {
					return nil, err
				}
				if output == "" {
					return nil, fmt.Errorf("strategy %s needs %q in with", StrategyLocation, withOutput)
				}
				if _, err := release.Output(output); err != nil {
					return nil, err
				}
				return locationResolver{release: release, output: output}, nil
			},
		},
	}
}

// outputOf rejects any key but withOutput, so a misspelling cannot read as an
// omission.
func outputOf(with map[string]string) (string, error) {
	var unknown []string
	for k := range with {
		if k != withOutput {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return "", fmt.Errorf("the release strategies take only %q in with, not %s", withOutput, strings.Join(unknown, ", "))
	}
	return with[withOutput], nil
}

// servedURL reports whether an override is the https URL a copy of an output is
// served at.
func servedURL(override string) bool {
	parsed, err := url.Parse(override)
	return err == nil && parsed.Scheme == "https" && parsed.Host != ""
}

// versionResolver answers a version from the authenticated release. The release
// is the source of truth, so a person's override is refused and never
// substituted for what was authenticated.
type versionResolver struct {
	release validationplan.Release
	output  string
}

func (r versionResolver) Resolve(inputs map[string]string) (map[string]string, error) {
	if inputs[resolve.InputOverrideKey] != "" {
		return nil, fmt.Errorf("the release is authenticated, so a version it supplies cannot be overridden")
	}
	if r.output == "" {
		_, digest, _ := strings.Cut(r.release.Reference, "@")
		if digest == "" {
			return nil, fmt.Errorf("the release reference %q carries no digest", r.release.Reference)
		}
		return map[string]string{resolve.ResultVersionKey: digest}, nil
	}
	output, err := r.release.Output(r.output)
	if err != nil {
		return nil, err
	}
	return map[string]string{resolve.ResultVersionKey: output.Digest}, nil
}

// locationResolver answers where an output of the release is. An override
// names the https URL a copy of that output is served at, such as the node
// image a run publishes where hardware can boot it, and nothing else. NVKE
// hands an override to the version half when an entry has both, so only an
// entry with no version half can be served this way.
type locationResolver struct {
	release validationplan.Release
	output  string
}

func (r locationResolver) Resolve(inputs map[string]string) (map[string]string, error) {
	if served := inputs[resolve.InputOverrideKey]; served != "" {
		if !servedURL(served) {
			return nil, fmt.Errorf("a location override for %s must be an https URL, got %q", r.output, served)
		}
		return map[string]string{resolve.ResultURLKey: served}, nil
	}
	output, err := r.release.Output(r.output)
	if err != nil {
		return nil, err
	}
	return map[string]string{resolve.ResultURLKey: output.Reference}, nil
}

// declaredResolver answers a version from what the subject declares. With no
// table, a constraint or a value is the answer as written. With a table, the
// subject's constraint is looked up in it, which turns a minor into the exact
// version a reviewer wrote beside it. One of them is required, because a version
// nobody declared would be a guess. A person's override replaces the answer, as
// it does for a pin read from a file.
type declaredResolver struct {
	// table maps a constraint to the exact version declared for it.
	table map[string]string
}

var (
	constraintPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,2}$`)
	exactPattern      = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
)

// checkDeclaredTable rejects a table whose key is not a version prefix, whose
// value is not an exact version without a leading v, or whose value is not under
// its own key. A table that disagrees with itself would plan a version for a
// constraint it does not match.
func checkDeclaredTable(table map[string]string) error {
	for constraint, version := range table {
		if !constraintPattern.MatchString(constraint) {
			return fmt.Errorf("%s declares %q for %q, but a key must be a version prefix such as 1.36", StrategyDeclared, version, constraint)
		}
		if !exactPattern.MatchString(version) {
			return fmt.Errorf("%s declares %q for %q, but a value must be an exact version such as 1.36.3 with no leading v", StrategyDeclared, version, constraint)
		}
		if version != constraint && !strings.HasPrefix(version, constraint+".") {
			return fmt.Errorf("%s declares %q for %q, which does not match", StrategyDeclared, version, constraint)
		}
	}
	return nil
}

func (r declaredResolver) Resolve(inputs map[string]string) (map[string]string, error) {
	if override := inputs[resolve.InputOverrideKey]; override != "" {
		return map[string]string{resolve.ResultVersionKey: override}, nil
	}
	if err := r.Validate(inputs); err != nil {
		return nil, err
	}
	if len(r.table) > 0 {
		constraint := inputs[resolve.InputConstraintKey]
		version, ok := r.table[constraint]
		if !ok {
			return nil, fmt.Errorf("%s declares no version for %s, only %s", StrategyDeclared, constraint, declaredKeys(r.table))
		}
		return map[string]string{resolve.ResultVersionKey: version}, nil
	}
	declared := inputs[resolve.InputConstraintKey]
	if declared == "" {
		declared = inputs[resolve.InputValueKey]
	}
	return map[string]string{resolve.ResultVersionKey: declared}, nil
}

// Validate rejects a declaration this strategy cannot answer, before any plan is
// made. With a table it takes a constraint and no value. Without one it takes a
// constraint or a value, and exactly one.
func (r declaredResolver) Validate(inputs map[string]string) error {
	if inputs[resolve.InputReleasesKey] != "" {
		return fmt.Errorf("%s takes no %s", StrategyDeclared, resolve.InputReleasesKey)
	}
	constraint, value := inputs[resolve.InputConstraintKey], inputs[resolve.InputValueKey]
	if len(r.table) > 0 {
		if value != "" {
			return fmt.Errorf("%s with a table takes a %s, not a %s", StrategyDeclared, resolve.InputConstraintKey, resolve.InputValueKey)
		}
		if constraint == "" {
			return fmt.Errorf("%s with a table needs a %s declared", StrategyDeclared, resolve.InputConstraintKey)
		}
		return nil
	}
	switch {
	case constraint != "" && value != "":
		return fmt.Errorf("%s takes a %s or a %s, not both", StrategyDeclared, resolve.InputConstraintKey, resolve.InputValueKey)
	case constraint == "" && value == "":
		return fmt.Errorf("%s needs a %s or a %s declared", StrategyDeclared, resolve.InputConstraintKey, resolve.InputValueKey)
	}
	return nil
}

func declaredKeys(table map[string]string) string {
	keys := make([]string, 0, len(table))
	for k := range table {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}
