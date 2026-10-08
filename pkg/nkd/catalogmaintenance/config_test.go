// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
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
		{"weekly", func(c *Config) { c.Schedule = "0 6 * * 1" }, ""},
		{"schema", func(c *Config) { c.APIVersion = "future" }, e.CodeInvalidCatalog},
		{"escape", func(c *Config) { c.Catalog = "../catalog" }, e.CodeUsage},
		{"shell path", func(c *Config) { c.Definition = "$(touch pwn)" }, e.CodeUsage},
		{"absolute", func(c *Config) { c.Environment = "/env.yaml" }, e.CodeUsage},
		{"duplicate", func(c *Config) { c.Sources = []string{"first", "first"} }, e.CodeUsage},
		{"unknown cohort", func(c *Config) { c.SameRelease = [][]string{{"first", "absent"}} }, e.CodeUsage},
		{"cron injection", func(c *Config) { c.Schedule = "0 6 * * 1\nrun: evil" }, e.CodeUsage},
		{"cron out of range", func(c *Config) { c.Schedule = "60 6 * * 1" }, e.CodeUsage},
		{"cron too frequent", func(c *Config) { c.Schedule = "* * * * *" }, e.CodeUsage},
		{"validation", func(c *Config) { c.Validation = "qualified" }, e.CodeUsage},
		{"PR without execution", func(c *Config) {
			c.PullRequest = &PullRequest{ClientIDVariable: "BOT_CLIENT_ID", PrivateKeySecret: "BOT_PRIVATE_KEY"}
		}, e.CodeUsage},
		{"mutable checkout", func(c *Config) {
			c.SourceCheckout = &SourceCheckout{Repository: "owner/repo", Ref: "main", Secret: "SOURCE_TOKEN"}
		}, e.CodeUsage},
		{"secret injection", func(c *Config) {
			c.SourceCheckout = &SourceCheckout{Repository: "owner/repo", Ref: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Secret: "X }}"}
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
		{"unknown", "apiVersion: nkd.dev/catalog-maintenance/v1\nunknown: true\n", false},
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
	_, err = Refresh(ctx, Config{}, "test", false)
	assertCode(t, err, e.CodeTimeout)
}

func TestMaintenanceLoadRequiresOneStrictDocument(t *testing.T) {
	const valid = `apiVersion: nkd.dev/catalog-maintenance/v1
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
		{"additional conflicting policy", valid + "---\n" + valid + "schedule: '0 6 * * 1'\n", false},
		{"malformed trailing document", valid + "---\ninvalid: [\n", false},
		{"empty second document", valid + "---\n", false},
		{"unknown field", valid + "unknownPolicy: true\n", false},
		{"duplicate field", valid + "validation: local-run\n", false},
		{"nested unknown field", valid + "sourceCheckout: {unknown: true}\n", false},
		{"nested duplicate field", valid + "sourceCheckout: {secret: FIRST, secret: SECOND}\n", false},
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
