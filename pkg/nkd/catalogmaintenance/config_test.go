package catalogmaintenance

import (
	"context"
	"os"
	"path/filepath"
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
