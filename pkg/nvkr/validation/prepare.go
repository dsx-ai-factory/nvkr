// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package validation

import (
	"context"
	"maps"
	"slices"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

// testEnvironment declares what a validation run may use. Only the built-in
// local environment exists; any other name is refused rather than defaulted.
type testEnvironment struct {
	name         string
	capabilities []string
}

var testEnvironments = map[string]testEnvironment{
	"local": {name: "local", capabilities: []string{"docker"}},
}

// executorPlatforms are the container platforms the suite executor runs.
var executorPlatforms = []string{"linux/amd64", "linux/arm64"}

// PreparedValidation is an accepted, frozen selection: ordered suites, their
// exact source-document digests and the admitted test environment. Preparing
// performs no build, Run access or execution.
type PreparedValidation struct {
	environment testEnvironment
	suites      []PreparedSuite
}

// PreparedSuite is one selected registration and its source-document SHA-256.
type PreparedSuite struct {
	Registration
	DocumentSHA256 string
}

// Prepare loads the catalog, resolves selections in order and admits every
// selected suite against the named test environment and the executor.
func Prepare(ctx context.Context, catalog string, selections []string, environment string) (*PreparedValidation, error) {
	if ctx == nil {
		return nil, usage("validation requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "validation canceled before preparation")
	}
	if environment == "" {
		return nil, usage("validation requires a test environment")
	}
	selected, ok := testEnvironments[environment]
	if !ok {
		return nil, e.New(e.CodeUnsupported, "unknown test environment %q", environment)
	}
	registrations, err := LoadRegistrations(ctx, catalog)
	if err != nil {
		return nil, err
	}
	ids, err := registrations.resolve(selections)
	if err != nil {
		return nil, err
	}
	prepared := &PreparedValidation{environment: selected}
	for _, id := range ids {
		r := registrations.entries[id]
		if err := admitSuite(r, selected); err != nil {
			return nil, e.Wrap(e.CodeEngineCapability, err, "suite %q", id)
		}
		prepared.suites = append(prepared.suites, PreparedSuite{Registration: r.detached(), DocumentSHA256: registrations.documents[id]})
	}
	return prepared, nil
}

// resolve expands exact IDs and parent paths in argument order, keeping the
// first occurrence of each suite. Parent paths expand in ID order.
func (c *Registrations) resolve(selections []string) ([]string, error) {
	if len(selections) == 0 {
		return nil, usage("validation requires at least one selection")
	}
	all := slices.Sorted(func(yield func(string) bool) {
		for id := range c.entries {
			if !yield(id) {
				return
			}
		}
	})
	var ids []string
	seen := map[string]bool{}
	for _, selection := range selections {
		parts := strings.Split(selection, "/")
		// Segments may repeat (nvke/nvkectl/nvkectl); each must be a valid name.
		if len(parts) > 3 || slices.ContainsFunc(parts, func(p string) bool { return !segment.MatchString(p) }) {
			return nil, e.New(e.CodeUsage, "selection %q must be a suite ID or parent path", selection)
		}
		var matches []string
		if len(parts) == 3 {
			if _, ok := c.entries[selection]; ok {
				matches = []string{selection}
			}
		} else {
			for _, id := range all {
				if strings.HasPrefix(id, selection+"/") {
					matches = append(matches, id)
				}
			}
		}
		if len(matches) == 0 {
			return nil, e.New(e.CodeUnsupported, "no validator matches selection %q", selection)
		}
		for _, id := range matches {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids, nil
}

// admitSuite refuses requirements the environment or executor cannot meet.
// Producer inputs are derived only for executable outputs of one component.
func admitSuite(r Registration, environment testEnvironment) error {
	for _, capability := range r.Requires.Capabilities {
		if !slices.Contains(environment.capabilities, capability) {
			return e.New(e.CodeEngineCapability, "test environment %q does not provide capability %q", environment.name, capability)
		}
	}
	for _, platform := range r.Requires.Platforms {
		if !slices.Contains(executorPlatforms, platform) {
			return e.New(e.CodeEngineCapability, "executor does not support platform %q", platform)
		}
	}
	for _, role := range slices.Sorted(maps.Keys(r.Inputs)) {
		input := r.Inputs[role]
		if input.Format != lifecycle.OutputExecutable || input.Component != factory.ArtifactTestComponent {
			return e.New(e.CodeEngineCapability, "input role %q requires an unsupported format or component", role)
		}
	}
	return nil
}

// Environment returns the admitted test environment's name.
func (p *PreparedValidation) Environment() string { return p.environment.name }

// Suites returns detached prepared suites in selection order.
func (p *PreparedValidation) Suites() []PreparedSuite {
	result := make([]PreparedSuite, 0, len(p.suites))
	for _, suite := range p.suites {
		suite.Registration = suite.detached()
		result = append(result, suite)
	}
	return result
}
