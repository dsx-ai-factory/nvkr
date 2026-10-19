// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func validConfig() Config {
	return Config{APIVersion: SchemaVersion, Name: "test", Catalog: "catalog", Sources: []string{"first", "second"}, SameRelease: [][]string{{"first", "second"}}, Definition: "definition.yaml", Environment: "environment.yaml", Validation: "static"}
}

func TestMaintenanceConfigAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config)
		code   e.Code
	}{
		{"valid", func(*Config) {}, ""},
		{"schema", func(c *Config) { c.APIVersion = "future" }, e.CodeInvalidCatalog},
		{"escape", func(c *Config) { c.Catalog = "../catalog" }, e.CodeUsage},
		{"shell path", func(c *Config) { c.Definition = "$(touch pwn)" }, e.CodeUsage},
		{"absolute", func(c *Config) { c.Environment = "/env.yaml" }, e.CodeUsage},
		{"duplicate", func(c *Config) { c.Sources = []string{"first", "first"} }, e.CodeUsage},
		{"unknown cohort", func(c *Config) { c.SameRelease = [][]string{{"first", "absent"}} }, e.CodeUsage},
		{"validation", func(c *Config) { c.Validation = "qualified" }, e.CodeUsage},
		{"dependent bundles", func(c *Config) {
			c.DevelopmentEnvironment, c.QualificationPolicy = "environment-dev.yaml", "qualification.yaml"
		}, ""},
		{"development qualification", func(c *Config) {
			c.DevelopmentEnvironment = "dev.yaml"
			c.DevelopmentQualificationPolicy = "dev-qualification.yaml"
		}, ""},
		{"development qualification missing environment", func(c *Config) { c.DevelopmentQualificationPolicy = "dev-qualification.yaml" }, e.CodeUsage},
		{"development qualification escape", func(c *Config) {
			c.DevelopmentEnvironment = "dev.yaml"
			c.DevelopmentQualificationPolicy = "../dev-qualification.yaml"
		}, e.CodeUsage},
		{"development escape", func(c *Config) { c.DevelopmentEnvironment = "../dev.yaml" }, e.CodeUsage},
		{"development shell path", func(c *Config) { c.DevelopmentEnvironment = "$(touch pwn)" }, e.CodeUsage},
		{"development equals production", func(c *Config) { c.DevelopmentEnvironment = c.Environment }, e.CodeUsage},
		{"qualification absolute", func(c *Config) { c.QualificationPolicy = "/qualification.yaml" }, e.CodeUsage},
		{"additional runs", func(c *Config) {
			c.DevelopmentEnvironment = "environment-dev.yaml"
			c.AdditionalRuns = []RunInput{{"nightly.json", "environment-nightly.json"}, {c.Definition, "environment-other.yaml"}}
		}, ""},
		{"additional run escape", func(c *Config) { c.AdditionalRuns = []RunInput{{"../nightly.json", "environment-nightly.json"}} }, e.CodeUsage},
		{"additional run shell path", func(c *Config) { c.AdditionalRuns = []RunInput{{"nightly.json", "$(touch pwn)"}} }, e.CodeUsage},
		{"additional run empty", func(c *Config) { c.AdditionalRuns = []RunInput{{}} }, e.CodeUsage},
		{"additional run repeats production", func(c *Config) { c.AdditionalRuns = []RunInput{{c.Definition, c.Environment}} }, e.CodeUsage},
		{"additional run repeats development", func(c *Config) {
			c.DevelopmentEnvironment = "environment-dev.yaml"
			c.AdditionalRuns = []RunInput{{c.Definition, c.DevelopmentEnvironment}}
		}, e.CodeUsage},
		{"additional run repeated", func(c *Config) {
			c.AdditionalRuns = []RunInput{{"nightly.json", "environment-nightly.json"}, {"nightly.json", "environment-nightly.json"}}
		}, e.CodeUsage},
		{"too many additional runs", func(c *Config) {
			for i := 0; i <= maxAdditionalRuns; i++ {
				c.AdditionalRuns = append(c.AdditionalRuns, RunInput{"nightly.json", fmt.Sprintf("environment-%d.json", i)})
			}
		}, e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig()
			tc.change(&c)
			err := c.Validate()
			if tc.code != "" {
				assertCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMaintenanceLoadRejectsAmbiguousAndUnsafeFiles(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		link    bool
	}{
		{"unknown", "apiVersion: nvkr.dev/catalog-maintenance/v1\nunknown: true\n", false},
		{"duplicate", "apiVersion: a\napiVersion: b\n", false},
		{"symlink", "{}", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.yaml")
			writeFixture(t, p, []byte(tc.content))
			if tc.link {
				link := p + ".link"
				if err := os.Symlink(p, link); err != nil {
					t.Fatal(err)
				}
				p = link
			}
			_, err := Load(t.Context(), p)
			assertCode(t, err, e.CodeInvalidCatalog)
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := Load(ctx, "missing")
	assertCode(t, err, e.CodeTimeout)
	var missingContext context.Context
	_, err = Load(missingContext, "missing")
	assertCode(t, err, e.CodeUsage)
	_, err = Refresh(ctx, Config{}, "test", false, "")
	assertCode(t, err, e.CodeTimeout)
}

func TestMaintenanceLoadRequiresOneStrictDocument(t *testing.T) {
	const valid = `apiVersion: nvkr.dev/catalog-maintenance/v1
name: test
catalog: catalog
sources: [first, second]
sameRelease: [[first, second]]
definition: definition.yaml
environment: environment.yaml
validation: static
`
	jsonConfig, err := json.Marshal(validConfig())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		content string
		valid   bool
	}{
		{"single document", valid, true},
		{"single JSON document", string(jsonConfig), true},
		{"explicit document boundaries", "---\n" + valid + "...\n# trailing comment\n", true},
		{"additional unknown policy", valid + "---\nunknownPolicy: true\n", false},
		{"additional conflicting policy", valid + "---\n" + strings.Replace(valid, "name: test", "name: other", 1), false},
		{"malformed trailing document", valid + "---\ninvalid: [\n", false},
		{"empty second document", valid + "---\n", false},
		{"unknown field", valid + "unknownPolicy: true\n", false},
		{"duplicate field", valid + "validation: local-run\n", false},
		{"retired source checkout", valid + "sourceCheckout: {repository: owner/repo}\n", false},
		{"unknown dependent bundle field", valid + "qualification: qualification.yaml\n", false},
		{"anchor", strings.Replace(valid, "name: test", "name: &policy test", 1), false},
		{"alias", strings.Replace(valid, "sources: [first, second]", "sources: [&source first, *source]", 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.yaml")
			writeFixture(t, p, []byte(tc.content))
			got, err := Load(t.Context(), p)
			if !tc.valid {
				assertCode(t, err, e.CodeInvalidCatalog)
				if !reflect.DeepEqual(got, Config{}) {
					t.Fatalf("invalid document returned partial policy: %+v", got)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, validConfig()) {
				t.Fatalf("valid config = %+v, error = %v", got, err)
			}
		})
	}
}

func TestValidateSchedule(t *testing.T) {
	for _, tc := range []struct {
		name, schedule string
		code           e.Code
	}{
		{"manual only", "", ""},
		{"nightly", DefaultSchedule, ""},
		{"weekly", "0 6 * * 1", ""},
		{"injection", "0 6 * * 1\nrun: evil", e.CodeUsage},
		{"out of range", "60 6 * * 1", e.CodeUsage},
		{"too frequent", "* * * * *", e.CodeUsage},
		{"step", "0 */6 * * *", e.CodeUsage},
		{"alias", "@daily", e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSchedule(tc.schedule)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertCode(t, err, tc.code)
		})
	}
}
