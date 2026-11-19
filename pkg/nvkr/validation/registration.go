// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package validation

import (
	"context"
	"maps"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/safetext"
)

const (
	maxRegistrationDirectoryEntries = 1024
	maxRegistrations                = 1024
	maxRegistrationBytes            = 1 << 20
	maxRegistrationCatalogBytes     = 16 << 20
)

// Registration declares one suite's inputs, test origin, image, command and
// requirements. It is a description: loading it neither proves that its
// requirements can be met nor executes anything.
type Registration struct {
	envelope         `yaml:",inline"`
	ID               string                       `json:"id" yaml:"id"`
	Description      string                       `json:"description" yaml:"description"`
	Inputs           map[string]RegistrationInput `json:"inputs" yaml:"inputs"`
	TestOrigin       FromInput                    `json:"testOrigin" yaml:"testOrigin"`
	Image            FromInput                    `json:"image" yaml:"image"`
	WorkingDirectory string                       `json:"workingDirectory,omitempty" yaml:"workingDirectory,omitempty"`
	Acquisition      *Acquisition                 `json:"acquisition,omitempty" yaml:"acquisition,omitempty"`
	Command          []string                     `json:"command" yaml:"command"`
	Environment      map[string]string            `json:"environment,omitempty" yaml:"environment,omitempty"`
	Requires         Requirements                 `json:"requires" yaml:"requires"`
	Timeout          string                       `json:"timeout" yaml:"timeout"`
}

// RegistrationInput names the exact Run output, producer component and format of one input role.
type RegistrationInput struct {
	Output    string `json:"output" yaml:"output"`
	Component string `json:"component" yaml:"component"`
	Format    string `json:"format" yaml:"format"`
}

// FromInput derives a value from the named input role's producer.
type FromInput struct {
	FromInput string `json:"fromInput" yaml:"fromInput"`
}

// Acquisition is optional dependency preparation, declared separately from the test command.
type Acquisition struct {
	Command []string `json:"command" yaml:"command"`
}

// Requirements are declared needs; a TestEnvironment must satisfy them before any build.
type Requirements struct {
	Platforms    []string `json:"platforms" yaml:"platforms"`
	Capabilities []string `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
}

var (
	environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,255}$`)
	inputReference  = regexp.MustCompile(`^inputs\.([a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?)\.(path|version)$`)
)

// validate checks one decoded registration against the identity derived from its path.
func (r *Registration) validate(id string) error {
	if r.APIVersion != "nvkr.dev/validator-registration/v1" || r.Kind != "ValidatorRegistration" {
		return usage("unsupported registration apiVersion or kind")
	}
	if !validID(r.ID) || r.ID != id {
		return e.New(e.CodeUsage, "registration id must equal its catalog path identity %q", id)
	}
	if !validText(r.Description) {
		return usage("registration requires a safe description")
	}
	if len(r.Inputs) == 0 || len(r.Inputs) > 64 {
		return usage("registration requires 1..64 input roles")
	}
	for role, input := range r.Inputs {
		if !segment.MatchString(role) || !safetext.Valid(input.Output, 256, false) || !validText(input.Component) || !validText(input.Format) {
			return usage("invalid registration input role")
		}
	}
	if _, ok := r.Inputs[r.TestOrigin.FromInput]; !ok {
		return usage("testOrigin must name a declared input role")
	}
	if _, ok := r.Inputs[r.Image.FromInput]; !ok {
		return usage("image must name a declared input role")
	}
	if r.WorkingDirectory != "" && !relativeDirectory(r.WorkingDirectory) {
		return usage("workingDirectory must be a clean relative path within the test origin")
	}
	if !r.validCommand(r.Command) {
		return usage("command requires 1..256 safe, non-empty arguments with declared input references")
	}
	if r.Acquisition != nil && !r.validCommand(r.Acquisition.Command) {
		return usage("acquisition command requires 1..256 safe, non-empty arguments with declared input references")
	}
	if len(r.Environment) > 256 {
		return usage("registration environment exceeds 256 bindings")
	}
	for _, name := range slices.Sorted(maps.Keys(r.Environment)) {
		value := r.Environment[name]
		if !environmentName.MatchString(name) || !safetext.Valid(value, 4096, true) || !r.validReferences(value) {
			return e.New(e.CodeUsage, "invalid environment binding %q", name)
		}
	}
	if !validList(r.Requires.Platforms, false, func(p string) bool {
		system, arch, ok := strings.Cut(p, "/")
		return ok && segment.MatchString(system) && segment.MatchString(arch)
	}) {
		return usage("requires.platforms requires 1..64 unique os/arch values")
	}
	if !validList(r.Requires.Capabilities, true, segment.MatchString) {
		return usage("requires.capabilities requires unique lowercase names")
	}
	timeout, err := time.ParseDuration(r.Timeout)
	if err != nil || timeout <= 0 {
		return usage("timeout requires a positive duration")
	}
	sort.Strings(r.Requires.Platforms)
	sort.Strings(r.Requires.Capabilities)
	return nil
}

// validReferences admits only ${inputs.<declared role>.path|version}; other text is literal.
func (r *Registration) validReferences(value string) bool {
	for {
		start := strings.Index(value, "${")
		if start < 0 {
			return true
		}
		end := strings.IndexByte(value[start:], '}')
		if end < 0 {
			return false
		}
		match := inputReference.FindStringSubmatch(value[start+2 : start+end])
		if match == nil {
			return false
		}
		if _, ok := r.Inputs[match[1]]; !ok {
			return false
		}
		value = value[start+end+1:]
	}
}

func relativeDirectory(dir string) bool {
	return pathText(dir) && !strings.HasPrefix(dir, "/") && !strings.Contains(dir, "\\") && path.Clean(dir) == dir &&
		dir != "." && dir != ".." && !strings.HasPrefix(dir, "../")
}

func (r *Registration) validCommand(arguments []string) bool {
	if len(arguments) == 0 || len(arguments) > 256 {
		return false
	}
	for _, argument := range arguments {
		if !validText(argument) || !r.validReferences(argument) {
			return false
		}
	}
	return true
}

func validList(values []string, allowEmpty bool, valid func(string) bool) bool {
	if (!allowEmpty && len(values) == 0) || len(values) > 64 || !unique(values) {
		return false
	}
	for _, value := range values {
		if !valid(value) {
			return false
		}
	}
	return true
}

// Registrations holds validated registrations discovered beneath one catalog directory.
type Registrations struct {
	entries map[string]Registration
	// documents maps each registration ID to the hex SHA-256 of the exact file
	// bytes it was decoded from, so a validation can record which version of the
	// registration it used.
	documents map[string]string
}

// LoadRegistrations discovers {repository}/{suite-group}/{suite}.yaml beneath
// path through one held directory. Any other entry, symlink or invalid
// document fails the whole catalog. It performs no acquisition or execution.
func LoadRegistrations(ctx context.Context, path string) (*Registrations, error) {
	if ctx == nil {
		return nil, usage("validator catalog requires context")
	}
	if !pathText(path) {
		return nil, usage("validator catalog requires a directory path")
	}
	// Cleaning drops a trailing separator or dot, which would otherwise make
	// the final component follow a symlink; the root open then refuses links.
	root, err := openCatalogRoot(ctx, filepath.Clean(path))
	if err != nil {
		return nil, configurationError(ctx, err, "opening validator catalog")
	}
	defer func() { _ = root.Close() }()
	return loadRegistrationRoot(ctx, root)
}

func loadRegistrationRoot(ctx context.Context, root *catalogRoot) (*Registrations, error) {
	list := func(dir string) ([]string, error) {
		names, err := root.names(ctx, dir, maxRegistrationDirectoryEntries)
		if err != nil {
			return nil, configurationError(ctx, err, "listing validator catalog")
		}
		if len(names) == 0 && dir == "" {
			return nil, usage("validator catalog has no registrations")
		}
		if len(names) == 0 {
			return nil, e.New(e.CodeUsage, "validator catalog directory %q is empty", dir)
		}
		return names, nil
	}
	result := &Registrations{entries: map[string]Registration{}, documents: map[string]string{}}
	budget := maxRegistrationCatalogBytes
	repositories, err := list("")
	if err != nil {
		return nil, err
	}
	for _, repository := range repositories {
		if !segment.MatchString(repository) {
			return nil, e.New(e.CodeUsage, "invalid repository directory %q", repository)
		}
		groups, err := list(repository)
		if err != nil {
			return nil, err
		}
		for _, group := range groups {
			if !segment.MatchString(group) {
				return nil, e.New(e.CodeUsage, "invalid suite group directory %q", repository+"/"+group)
			}
			files, err := list(repository + "/" + group)
			if err != nil {
				return nil, err
			}
			for _, file := range files {
				suite, ok := strings.CutSuffix(file, ".yaml")
				relative := repository + "/" + group + "/" + file
				if !ok || !segment.MatchString(suite) {
					return nil, e.New(e.CodeUsage, "catalog entry %q must be a <suite>.yaml registration", relative)
				}
				if len(result.entries) == maxRegistrations {
					return nil, usage("validator catalog exceeds its registration limit")
				}
				raw, err := root.read(ctx, relative, maxRegistrationBytes)
				if err != nil {
					return nil, configurationError(ctx, err, "reading validator registration")
				}
				if budget -= len(raw); budget < 0 {
					return nil, usage("validator catalog exceeds its byte limit")
				}
				var registration Registration
				if err := decodeDocument(raw, &registration); err != nil {
					return nil, e.Wrap(e.CodeUsage, err, "registration %q", relative)
				}
				if err := registration.validate(repository + "/" + group + "/" + suite); err != nil {
					return nil, e.Wrap(e.CodeUsage, err, "registration %q", relative)
				}
				result.entries[registration.ID] = registration
				result.documents[registration.ID] = blobDigest(raw)
			}
		}
	}
	return result, nil
}

// List returns detached registrations in ID order.
func (c *Registrations) List() []Registration {
	ids := slices.Sorted(maps.Keys(c.entries))
	result := make([]Registration, 0, len(ids))
	for _, id := range ids {
		result = append(result, c.entries[id].detached())
	}
	return result
}

// Describe requires an exact registration ID; parent paths and aliases are not accepted.
func (c *Registrations) Describe(id string) (Registration, error) {
	r, ok := c.entries[id]
	if !ok {
		return Registration{}, e.New(e.CodeUnsupported, "unknown validator %q", id)
	}
	return r.detached(), nil
}

func (r Registration) detached() Registration {
	r.Inputs = maps.Clone(r.Inputs)
	r.Command = slices.Clone(r.Command)
	if r.Acquisition != nil {
		r.Acquisition = &Acquisition{Command: slices.Clone(r.Acquisition.Command)}
	}
	r.Environment = maps.Clone(r.Environment)
	r.Requires.Platforms = slices.Clone(r.Requires.Platforms)
	r.Requires.Capabilities = slices.Clone(r.Requires.Capabilities)
	return r
}
