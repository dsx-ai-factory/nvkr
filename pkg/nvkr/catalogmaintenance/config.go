// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package catalogmaintenance owns review-only catalog refreshes and their
// GitHub workflow renderer. It grants no build, publication or release authority.
package catalogmaintenance

import (
	"context"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/documentpath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"sigs.k8s.io/yaml"
)

const SchemaVersion = "nvkr.dev/catalog-maintenance/v1"

// Config is operator-owned maintenance policy, not a public lifecycle object.
// Constraints stay in the catalog. This config never authorizes widening them.
type Config struct {
	APIVersion  string     `json:"apiVersion"`
	Name        string     `json:"name"`
	Catalog     string     `json:"catalog"`
	Sources     []string   `json:"sources"`
	SameRelease [][]string `json:"sameRelease,omitempty"`
	Definition  string     `json:"definition"`
	Environment string     `json:"environment"`
	Validation  string     `json:"validation"`
	// DevelopmentEnvironment and QualificationPolicy name the other committed
	// generated bundles that share this catalog's pins. They are generation
	// inputs for the existing owners, not authority: each is checked, regenerated
	// and transported with the production Run, and a committed bundle the policy
	// does not name is refused.
	DevelopmentEnvironment         string `json:"developmentEnvironment,omitempty"`
	QualificationPolicy            string `json:"qualificationPolicy,omitempty"`
	DevelopmentQualificationPolicy string `json:"developmentQualificationPolicy,omitempty"`
	// AdditionalRuns names further committed Run bundles, each compiled from its
	// own Definition and Environment, that also consume this catalog's pins, such
	// as named nightly bundles. They join the same closure under the same rules.
	AdditionalRuns []RunInput `json:"additionalRuns,omitempty"`
}

// RunInput selects one generated Run bundle by its authored inputs.
type RunInput struct {
	Definition  string `json:"definition"`
	Environment string `json:"environment"`
}

// maxAdditionalRuns bounds the closure a single refresh regenerates.
const maxAdditionalRuns = 16

var safeName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
var safePath = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
var repositoryName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)
var secretName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

func safeRelative(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.HasPrefix(value, "../") && !path.IsAbs(value) && path.Clean(value) == value && safePath.MatchString(value)
}

// ValidateSchedule admits an empty schedule (manual dispatch only) or a reviewed
// cron: each field is '*' or one in-range decimal integer, and the minute is fixed.
func ValidateSchedule(schedule string) error {
	if schedule == "" {
		return nil
	}
	fields := strings.Fields(schedule)
	if len(fields) != 5 || strings.Join(fields, " ") != schedule {
		return e.New(e.CodeUsage, "schedule requires five cron fields")
	}
	// Deliberately narrow: no aliases, steps, lists or injection syntax.
	bounds := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}
	for i, field := range fields {
		if field == "*" {
			continue
		}
		n, err := strconv.Atoi(field)
		if err != nil || strconv.Itoa(n) != field || n < bounds[i][0] || n > bounds[i][1] {
			return e.New(e.CodeUsage, "unsupported cron field %q; use '*' or an in-range decimal integer", field)
		}
	}
	if fields[0] == "*" {
		return e.New(e.CodeUsage, "maintenance schedule must select one minute of the hour")
	}
	return nil
}

func (config Config) Validate() error {
	if config.APIVersion != SchemaVersion {
		return e.New(e.CodeInvalidCatalog, "unsupported catalog maintenance schema %q", config.APIVersion)
	}
	if !safeName.MatchString(config.Name) || len(config.Name) > 64 {
		return e.New(e.CodeUsage, "maintenance name requires a bounded lowercase identifier")
	}
	for _, value := range []string{config.Catalog, config.Definition, config.Environment} {
		if !safeRelative(value) {
			return e.New(e.CodeUsage, "maintenance paths must be clean repository-relative paths: %q", value)
		}
	}
	for _, value := range []string{config.DevelopmentEnvironment, config.QualificationPolicy, config.DevelopmentQualificationPolicy} {
		if value != "" && !safeRelative(value) {
			return e.New(e.CodeUsage, "maintenance paths must be clean repository-relative paths: %q", value)
		}
	}
	if config.DevelopmentQualificationPolicy != "" && config.DevelopmentEnvironment == "" {
		return e.New(e.CodeUsage, "development qualification requires developmentEnvironment")
	}
	if config.DevelopmentEnvironment != "" && config.DevelopmentEnvironment == config.Environment {
		return e.New(e.CodeUsage, "developmentEnvironment must differ from the production environment")
	}
	if len(config.AdditionalRuns) > maxAdditionalRuns {
		return e.New(e.CodeUsage, "maintenance admits at most %d additional runs", maxAdditionalRuns)
	}
	runs := map[RunInput]bool{{config.Definition, config.Environment}: true}
	if config.DevelopmentEnvironment != "" {
		runs[RunInput{config.Definition, config.DevelopmentEnvironment}] = true
	}
	for _, run := range config.AdditionalRuns {
		if !safeRelative(run.Definition) || !safeRelative(run.Environment) {
			return e.New(e.CodeUsage, "maintenance paths must be clean repository-relative paths: %q, %q", run.Definition, run.Environment)
		}
		if runs[run] {
			return e.New(e.CodeUsage, "additional run %s with %s repeats another selected run", run.Definition, run.Environment)
		}
		runs[run] = true
	}
	if len(config.Sources) == 0 || len(config.Sources) > 128 {
		return e.New(e.CodeUsage, "maintenance requires 1–128 explicit source roots")
	}
	selected := map[string]bool{}
	for _, source := range config.Sources {
		if !safeName.MatchString(source) || selected[source] {
			return e.New(e.CodeUsage, "invalid or repeated maintenance source %q", source)
		}
		selected[source] = true
	}
	grouped := map[string]bool{}
	for _, group := range config.SameRelease {
		if len(group) < 2 {
			return e.New(e.CodeUsage, "same-release groups require at least two sources")
		}
		for _, source := range group {
			if !selected[source] || grouped[source] {
				return e.New(e.CodeUsage, "same-release source %q must be selected once", source)
			}
			grouped[source] = true
		}
	}
	if config.Validation != "static" && config.Validation != "local-run" {
		return e.New(e.CodeUsage, "maintenance validation must be static or local-run")
	}
	return nil
}

// ValidateSourceRepository admits the owner/repository of a private source that
// the workflow transports through a local Git rewrite; empty means none.
func ValidateSourceRepository(repository string) error {
	if repository != "" && (!repositoryName.MatchString(repository) || strings.HasSuffix(repository, ".git")) {
		return e.New(e.CodeUsage, "source repository requires owner/repository without a .git suffix")
	}
	return nil
}

func Load(ctx context.Context, filename string) (Config, error) {
	if ctx == nil {
		return Config{}, e.New(e.CodeUsage, "maintenance load requires a context")
	}
	held, err := heldfile.OpenContext(ctx, filename, heldfile.Options{Label: "catalog maintenance", MaxBytes: 64 << 10})
	if err != nil {
		return Config{}, contextError(ctx, e.Wrap(e.CodeInvalidCatalog, err, "opening maintenance config"))
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, 64<<10)
	if err != nil {
		return Config{}, contextError(ctx, e.Wrap(e.CodeInvalidCatalog, err, "reading maintenance config"))
	}
	// Admit the complete document stream before strict typed decoding, which
	// otherwise accepts only the first YAML document and ignores trailing policy.
	if _, err := documentpath.Decode(raw); err != nil {
		return Config{}, e.Wrap(e.CodeInvalidCatalog, err, "decoding maintenance config")
	}
	var config Config
	if err := yaml.UnmarshalStrict(raw, &config); err != nil {
		return Config{}, e.Wrap(e.CodeInvalidCatalog, err, "decoding maintenance config")
	}
	return config, config.Validate()
}

func contextError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "catalog maintenance canceled")
	}
	return err
}
