// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package validation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func writeCatalogFile(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
func catalogFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"catalog.yaml", "nvke/nvkectl/cli.yaml", "nvke/nvkectl/contract.yaml"} {
		raw, err := os.ReadFile(filepath.Join("../../../inputs/testing/validators", name))
		if err != nil {
			t.Fatal(err)
		}
		writeCatalogFile(t, filepath.Join(root, name), raw)
	}
	p := profile{envelope: envelope{APIVersion: "nvkr.dev/validation-profile/v1", Kind: "ValidationProfile"}, Catalog: "catalog.yaml", Source: profileSource{Kind: "local-plan", Plan: "plan.json"}, Checks: []profileCheck{
		{Name: "one", Validator: "nvke/nvkectl/contract", Inputs: map[string]binding{"executable": {Output: "cli-output", Artifact: "binary"}}},
		{Name: "two", Validator: "nvke/nvkectl/cli", Inputs: map[string]binding{"executable": {Output: "cli-output", Artifact: "binary"}}},
	}}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "profile.json")
	writeCatalogFile(t, path, raw)
	return filepath.Join(root, "catalog.yaml"), path
}
func catalogCode(t *testing.T, err error, code e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("expected %v, got %v", code, err)
	}
}
func TestCatalogDiscoveryDeterministicAndDetached(t *testing.T) {
	path, _ := catalogFixture(t)
	c, err := LoadCatalog(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := canonical.Marshal(c.List())
	if err != nil {
		t.Fatal(err)
	}
	list := c.List()
	if len(list) != 2 || list[0].Validator.ID != "nvke/nvkectl/cli" || !list[0].Available || list[0].RuntimeReadiness != "unverified" {
		t.Fatal(list)
	}
	list[0].Validator.Inputs["executable"] = InputRequirement{Component: "poison"}
	list[0].Validator.Platforms[0] = "poison"
	after, _ := canonical.Marshal(c.List())
	if !bytes.Equal(before, after) {
		t.Fatal("caller mutated catalog")
	}
	writeCatalogFile(t, path, []byte("apiVersion: nvkr.dev/validator-catalog/v1\nkind: ValidatorCatalog\nentries: [nvke/nvkectl/cli.yaml, nvke/nvkectl/contract.yaml]\n"))
	c, err = LoadCatalog(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	after, _ = canonical.Marshal(c.List())
	if !bytes.Equal(before, after) {
		t.Fatal("index order changed discovery")
	}
	_, err = c.Describe("contract")
	catalogCode(t, err, e.CodeUnsupported)
	for _, d := range c.List() {
		if len(d.Validator.Limitations) == 0 || len(d.Validator.Requires) == 0 {
			t.Fatal("missing disclosure")
		}
	}
	// There is no Plan, artifact or Docker fixture: discovery reads metadata only.
}
func TestCatalogUnavailableIsDescribable(t *testing.T) {
	path, profile := catalogFixture(t)
	entry := filepath.Join(filepath.Dir(path), "nvke/nvkectl/contract.yaml")
	raw, _ := os.ReadFile(entry)
	writeCatalogFile(t, entry, bytes.ReplaceAll(raw, []byte("nvke-cli/v1"), []byte("another-repo/v2")))
	c, err := LoadCatalog(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	d, err := c.Describe("nvke/nvkectl/contract")
	if err != nil || d.Available || d.UnavailableReason == "" {
		t.Fatalf("%+v %v", d, err)
	}
	_, err = AdmitProfile(t.Context(), profile, Runtime{})
	catalogCode(t, err, e.CodeEngineCapability)
}
func TestCatalogStrictDocuments(t *testing.T) {
	valid := `{"apiVersion":"nvkr.dev/validator-catalog/v1","kind":"ValidatorCatalog","entries":["one.yaml"]}`
	for _, tc := range []struct{ name, raw string }{
		{"unknown", strings.Replace(valid, `"entries":`, `"extra":true,"entries":`, 1)},
		{"case-alias", strings.Replace(valid, "apiVersion", "APIVersion", 1)},
		{"duplicate", strings.Replace(valid, `"entries":`, `"kind":"ValidatorCatalog","entries":`, 1)},
		{"unknown-version", strings.Replace(valid, "catalog/v1", "catalog/v2", 1)},
		{"wrong-kind", strings.Replace(valid, "ValidatorCatalog", "Validator", 1)},
		{"null", strings.Replace(valid, `["one.yaml"]`, `null`, 1)},
		{"type", strings.Replace(valid, `["one.yaml"]`, `true`, 1)},
		{"multi-document", valid + "\n---\n" + valid},
		{"alias", "apiVersion: nvkr.dev/validator-catalog/v1\nkind: ValidatorCatalog\nentries: &entries [one.yaml]\nother: *entries\n"},
		{"merge", "apiVersion: nvkr.dev/validator-catalog/v1\nkind: ValidatorCatalog\n<<: {entries: [one.yaml]}\n"},
		{"empty", ""},
		{"oversize", strings.Repeat(" ", (1<<20)+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "catalog.yaml")
			writeCatalogFile(t, path, []byte(tc.raw))
			_, err := LoadCatalog(t.Context(), path)
			catalogCode(t, err, e.CodeUsage)
		})
	}
}
func TestCatalogRefusesPathsAndDuplicates(t *testing.T) {
	for _, tc := range []string{"../escape.yaml", "/absolute.yaml", "nvke/../nvke/nvkectl/cli.yaml", "./nvke/nvkectl/cli.yaml", "link.yaml", "linked/cli.yaml", strings.Repeat("a/", 17) + "cli.yaml", "nvke/nvkectl/cli.yaml, nvke/nvkectl/cli.yaml", "nvke/nvkectl/cli.yaml, copy.yaml"} {
		t.Run(tc, func(t *testing.T) {
			path, _ := catalogFixture(t)
			root := filepath.Dir(path)
			raw, _ := os.ReadFile(filepath.Join(root, "nvke/nvkectl/cli.yaml"))
			writeCatalogFile(t, filepath.Join(root, "copy.yaml"), raw)
			if err := os.Symlink("nvke/nvkectl/cli.yaml", filepath.Join(root, "link.yaml")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("nvke/nvkectl", filepath.Join(root, "linked")); err != nil {
				t.Fatal(err)
			}
			writeCatalogFile(t, path, []byte("apiVersion: nvkr.dev/validator-catalog/v1\nkind: ValidatorCatalog\nentries: ["+tc+"]\n"))
			_, err := LoadCatalog(t.Context(), path)
			catalogCode(t, err, e.CodeUsage)
		})
	}
}
func TestCatalogEntryAndAdapterValidation(t *testing.T) {
	path, _ := catalogFixture(t)
	c, err := LoadCatalog(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	original := c.entries["nvke/nvkectl/contract"]
	for _, tc := range []struct {
		name   string
		change func(*Validator)
		code   e.Code
	}{
		{"id", func(v *Validator) { v.ID = "Nvke/cli/contract" }, e.CodeUsage},
		{"protocol", func(v *Validator) { v.Adapter = "shell" }, e.CodeUsage},
		{"source-role", func(v *Validator) { v.SuiteSource.FromInput = "absent" }, e.CodeUsage},
		{"source-mode", func(v *Validator) { v.SuiteSource.Mode = "latest" }, e.CodeUsage},
		{"budget", func(v *Validator) { v.Timeout = "31m" }, e.CodeEngineCapability},
		{"suite", func(v *Validator) { v.Suite = "bootstrap" }, e.CodeEngineCapability},
		{"coverage", func(v *Validator) { v.Coverage = "mixed" }, e.CodeEngineCapability},
		{"capability", func(v *Validator) { v.Requires = []string{} }, e.CodeEngineCapability},
		{"missing-git", func(v *Validator) { v.Requires = []string{"docker"} }, e.CodeEngineCapability},
		{"extra-capability", func(v *Validator) { v.Requires = []string{"docker", "git", "privileged"} }, e.CodeEngineCapability},
		{"platform", func(v *Validator) { v.Platforms = []string{"darwin/arm64"} }, e.CodeEngineCapability},
		{"duplicate-requirements", func(v *Validator) { v.Requires = []string{"docker", "docker"} }, e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := original
			tc.change(&v)
			err := v.validate()
			if err == nil {
				err = (nvkeCLIAdapter{}).validate(v)
			}
			catalogCode(t, err, tc.code)
		})
	}
}

type recordingCatalogAdapter struct {
	admitted, executed     *[]string
	failAdmission, failRun string
	onRun                  func()
}

func (recordingCatalogAdapter) limitations() []string { return (nvkeCLIAdapter{}).limitations() }

func (a recordingCatalogAdapter) validate(v Validator) error { return (nvkeCLIAdapter{}).validate(v) }
func (a recordingCatalogAdapter) prepare(_ context.Context, v Validator, _ profileSource, check profileCheck, _ Runtime, _ []byte) (func(context.Context) error, any, error) {
	*a.admitted = append(*a.admitted, check.Name)
	if check.Name == a.failAdmission {
		return nil, nil, usage("static admission failure")
	}
	suite, artifact := v.Suite, check.Inputs["executable"].Artifact
	return func(context.Context) error {
		if a.onRun != nil {
			a.onRun()
		}
		*a.executed = append(*a.executed, check.Name+":"+suite+":"+artifact)
		if check.Name == a.failRun {
			return usage("suite failure")
		}
		return nil
	}, check.Inputs, nil
}
func mutateProfile(t *testing.T, path string, edit func(*profile)) {
	t.Helper()
	raw, _ := os.ReadFile(path)
	var p profile
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	edit(&p)
	raw, _ = json.Marshal(p)
	writeCatalogFile(t, path, raw)
}
func TestCatalogCompleteAdmissionBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*profile)
		code e.Code
	}{
		{"unknown", func(p *profile) { p.Checks[1].Validator = "nvke/nvkectl/unknown" }, e.CodeUnsupported},
		{"duplicate-name", func(p *profile) { p.Checks[1].Name = p.Checks[0].Name }, e.CodeUsage},
		{"extra-role", func(p *profile) { p.Checks[1].Inputs["extra"] = binding{Output: "x", Artifact: "x"} }, e.CodeUsage},
		{"missing-role", func(p *profile) { p.Checks[1].Inputs = map[string]binding{} }, e.CodeUsage},
		{"unknown-role", func(p *profile) { p.Checks[1].Inputs = map[string]binding{"unknown": {Output: "x", Artifact: "x"}} }, e.CodeUsage},
		{"missing-output", func(p *profile) { p.Checks[1].Inputs["executable"] = binding{Artifact: "x"} }, e.CodeUsage},
		{"mixed-trust", func(p *profile) { p.Source.Reference = "published" }, e.CodeUsage},
		{"published-new-format", func(p *profile) { p.Source.Kind = "published" }, e.CodeUnsupported},
		{"no-checks", func(p *profile) { p.Checks = nil }, e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, path := catalogFixture(t)
			mutateProfile(t, path, tc.edit)
			admitted, executed := []string{}, []string{}
			_, err := admitProfile(t.Context(), path, Runtime{}, map[string]adapter{"nvke-cli/v1": recordingCatalogAdapter{admitted: &admitted, executed: &executed}})
			catalogCode(t, err, tc.code)
			if len(admitted) != 0 || len(executed) != 0 {
				t.Fatal("dispatch before full structural admission")
			}
		})
	}
	_, path := catalogFixture(t)
	admitted, executed := []string{}, []string{}
	_, err := admitProfile(t.Context(), path, Runtime{}, map[string]adapter{"nvke-cli/v1": recordingCatalogAdapter{admitted: &admitted, executed: &executed, failAdmission: "two"}})
	catalogCode(t, err, e.CodeUsage)
	if len(admitted) != 2 || len(executed) != 0 {
		t.Fatal("execution during static admission")
	}
}
func TestCatalogSnapshotOrderAndStopOnFailure(t *testing.T) {
	for _, fail := range []string{"", "one"} {
		t.Run("failure-"+fail, func(t *testing.T) {
			catalog, path := catalogFixture(t)
			originalProfile, _ := os.ReadFile(path)
			admitted, executed := []string{}, []string{}
			invocation, err := admitProfile(t.Context(), path, Runtime{}, map[string]adapter{"nvke-cli/v1": recordingCatalogAdapter{admitted: &admitted, executed: &executed, failRun: fail, onRun: func() {
				writeCatalogFile(t, path, []byte("invalid profile"))
				writeCatalogFile(t, catalog, []byte("invalid catalog"))
				writeCatalogFile(t, filepath.Join(filepath.Dir(path), "nvke/nvkectl/cli.yaml"), []byte("invalid entry"))
			}}})
			if err != nil {
				t.Fatal(err)
			}
			before := invocation.Configuration()
			before[0] = 'x'
			if invocation.Configuration()[0] == 'x' {
				t.Fatal("mutable configuration")
			}
			if !bytes.Contains(invocation.Configuration(), []byte(blobDigest(originalProfile))) {
				t.Fatal("missing document digests")
			}
			err = invocation.Run(t.Context())
			if fail != "" {
				catalogCode(t, err, e.CodeUsage)
			} else if err != nil {
				t.Fatal(err)
			}
			expected := 2
			if fail != "" {
				expected = 1
			}
			if len(executed) != expected {
				t.Fatal(executed)
			}
			if !strings.Contains(executed[0], "one:contract:") || !strings.HasSuffix(executed[0], "/binary") {
				t.Fatal(executed)
			}
			if expected == 2 && !strings.Contains(executed[1], "two:cli:") {
				t.Fatal(executed)
			}
		})
	}
}
func TestCatalogCancellationAndDocumentLimits(t *testing.T) {
	path, _ := catalogFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := LoadCatalog(ctx, path)
	catalogCode(t, err, e.CodeTimeout)
	entries := make([]string, 1025)
	for i := range entries {
		entries[i] = "x.yaml"
	}
	raw, _ := json.Marshal(catalogIndex{envelope: envelope{APIVersion: "nvkr.dev/validator-catalog/v1", Kind: "ValidatorCatalog"}, Entries: entries})
	writeCatalogFile(t, path, raw)
	_, err = LoadCatalog(t.Context(), path)
	catalogCode(t, err, e.CodeUsage)
}

func TestCatalogProductionCLIAdmissionParityAndRefusal(t *testing.T) {
	for _, output := range []string{"nvkectl", "nvkectl-linux-arm64", "missing", ""} {
		t.Run(output, func(t *testing.T) {
			catalog, path := catalogFixture(t)
			artifact := filepath.Join(filepath.Dir(path), "binary")
			if err := os.WriteFile(artifact, []byte("supplied"), 0o755); err != nil {
				t.Fatal(err)
			}
			plan, err := filepath.Abs("../../../.github/nvkr-run/plan.json")
			if err != nil {
				t.Fatal(err)
			}
			mutateProfile(t, path, func(p *profile) {
				p.Source.Plan = plan
				for i := range p.Checks {
					p.Checks[i].Inputs["executable"] = binding{Output: output, Artifact: artifact}
				}
			})
			workspace := filepath.Join(filepath.Dir(catalog), "no-work-before-admission")
			invocation, err := AdmitProfile(t.Context(), path, Runtime{WorkspaceRoot: workspace})
			if output == "missing" || output == "" {
				catalogCode(t, err, e.CodeUsage)
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(invocation.Configuration(), []byte(`"output":"`+output+`"`)) || !bytes.Contains(invocation.Configuration(), []byte(`"toolchainImage"`)) {
					t.Fatal("missing frozen factory binding")
				}
			}
			if _, err := os.Stat(workspace); !os.IsNotExist(err) {
				t.Fatal("admission created workspace")
			}
		})
	}
}
