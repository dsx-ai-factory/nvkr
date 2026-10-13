// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
)

func filesOptionsV3Fixture(t *testing.T) SuiteConfigurationV3 {
	t.Helper()
	_, _, requirement := suiteTestV3(t)
	digest := evidenceFixture().ToolSmoke.Policy.Cosign.Digest
	return SuiteConfigurationV3{SchemaVersion: SuiteConfigurationSchemaV3,
		Policy: PolicyV3{SchemaVersion: PolicySchemaV3, NativePolicyDigest: evidenceFixture().ToolSmoke.PolicyDigest, RequiredSuites: []SuiteRequirement{requirement}},
		Suites: []SuiteLocatorV3{{Name: requirement.Expected.Suite.Name, Locator: testobservation.Locator{SchemaVersion: testobservation.LocatorSchemaVersion, Reference: "registry.example.test/observations@sha256:" + strings.Repeat("a", 64), ObservationDigest: requirement.Expected.ObservationDigest}, Registry: testobservation.RegistryOptions{Transport: delivery.RegistryOptions{Anonymous: true}, CosignBinary: "/must-not-execute/cosign", CosignDigest: digest, Identity: requirement.Identity, Issuer: requirement.Issuer}}}}
}
func TestSuiteV3CompleteConfigurationAdmissionBeforeExternalWork(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*SuiteConfigurationV3)
		code e.Code
	}{
		{"missing suite", func(c *SuiteConfigurationV3) { c.Suites = nil }, e.CodeGateUnmet},
		{"extra suite", func(c *SuiteConfigurationV3) { c.Suites = append(c.Suites, c.Suites[0]) }, e.CodeGateUnmet},
		{"wrong name", func(c *SuiteConfigurationV3) { c.Suites[0].Name = "other" }, e.CodeGateUnmet},
		{"wrong observation digest", func(c *SuiteConfigurationV3) {
			c.Suites[0].Locator.ObservationDigest = "sha256:" + strings.Repeat("b", 64)
		}, e.CodeGateUnmet},
		{"wrong signer", func(c *SuiteConfigurationV3) { c.Suites[0].Registry.Identity = "other" }, e.CodeUntrustedSigner},
		{"wrong issuer", func(c *SuiteConfigurationV3) { c.Suites[0].Registry.Issuer = "other" }, e.CodeUntrustedSigner},
		{"tag locator", func(c *SuiteConfigurationV3) {
			c.Suites[0].Locator.Reference = "registry.example.test/observations:latest"
		}, e.CodeUnpinnedInput},
		{"offloopback HTTP", func(c *SuiteConfigurationV3) { c.Suites[0].Registry.Transport.PlainHTTP = true }, e.CodeUsage},
		{"implicit helpers", func(c *SuiteConfigurationV3) { c.Suites[0].Registry.Transport.Anonymous = false }, e.CodeTrustPolicyMissing},
		{"anonymous credential", func(c *SuiteConfigurationV3) {
			c.Suites[0].Registry.Credential = sign.Credential{Registry: "registry.example.test", Username: "fixture", Password: "fixture"}
		}, e.CodeUsage},
		{"credential host substitution", func(c *SuiteConfigurationV3) {
			c.Suites[0].Registry.Transport = delivery.RegistryOptions{Username: "fixture", Password: "fixture"}
			c.Suites[0].Registry.Credential = sign.Credential{Registry: "wrong.example", Username: "fixture", Password: "fixture"}
		}, e.CodeUsage},
		{"strong deployment unavailable", func(c *SuiteConfigurationV3) { c.Policy.RequiredSuites[0].RequireAuthenticatedDeployment = true }, e.CodeEngineCapability},
		{"unsafe signer", func(c *SuiteConfigurationV3) { c.Policy.RequiredSuites[0].Identity = "signer\u202e" }, e.CodeTrustPolicyMissing},
	} {
		t.Run(tc.name, func(t *testing.T) { c := filesOptionsV3Fixture(t); tc.edit(&c); requireCode(t, c.Validate(), tc.code) })
	}
}
func TestFilesV3RefusesBeforeIO(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := PublishFilesV3(ctx)
	requireCode(t, err, e.CodeTimeout)
	_, err = PublishFilesV3(t.Context())
	requireCode(t, err, e.CodeEngineCapability)
}

func TestLoadSuiteConfigurationV3StrictHeldInput(t *testing.T) {
	options := filesOptionsV3Fixture(t)
	configuration := SuiteConfigurationV3{SchemaVersion: SuiteConfigurationSchemaV3, Policy: options.Policy, Suites: options.Suites}
	raw, err := canonical.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "suite-configuration.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSuiteConfigurationV3(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := canonical.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Fatal("configuration meaning or nested option field names changed")
	}
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"unknown schema", []byte(strings.Replace(string(raw), SuiteConfigurationSchemaV3, "nvkr.dev/qualification-suite-configuration/v999", 1))},
		{"unknown field", []byte(strings.Replace(string(raw), `"schemaVersion":`, `"unknown":1,"schemaVersion":`, 1))},
		{"duplicate schema", []byte(strings.Replace(string(raw), `"schemaVersion":`, `"schemaVersion":"duplicate","schemaVersion":`, 1))},
		{"noncanonical", append(append([]byte(nil), raw...), '\n')},
		{"oversized", []byte(strings.Repeat(" ", maxSuiteConfigurationV3Bytes+1))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, tc.raw, 0600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadSuiteConfigurationV3(t.Context(), path)
			requireCode(t, err, e.CodeVerifyFailed)
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = LoadSuiteConfigurationV3(ctx, "does-not-exist")
	requireCode(t, err, e.CodeTimeout)
	_, err = LoadSuiteConfigurationV3(t.Context(), "")
	requireCode(t, err, e.CodeUsage)
}
func TestLoadSuiteConfigurationV3RejectsSignerMismatchWithoutPrintingCredential(t *testing.T) {
	options := filesOptionsV3Fixture(t)
	const secret = "PRIVATE_CREDENTIAL_VALUE_MUST_NOT_APPEAR"
	options.Suites[0].Registry.Transport = delivery.RegistryOptions{Username: "fixture", Password: secret}
	options.Suites[0].Registry.Credential = sign.Credential{Registry: "registry.example.test", Username: "fixture", Password: secret}
	options.Suites[0].Registry.Identity = "different"
	configuration := SuiteConfigurationV3{SchemaVersion: SuiteConfigurationSchemaV3, Policy: options.Policy, Suites: options.Suites}
	raw, err := canonical.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "private-config.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = LoadSuiteConfigurationV3(t.Context(), path)
	requireCode(t, err, e.CodeUntrustedSigner)
	if strings.Contains(err.Error(), secret) {
		t.Fatal("loader exposed credential content")
	}
}

func (f *suiteFixtureV3) Close() error { f.closed = true; return nil }
func TestFilesV3AggregateRetainedByteBudget(t *testing.T) {
	fixture, _, _ := suiteTestV3(t)
	raw, err := fixture.observation.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	size := int64(len(raw)) + fixture.observation.Files[0].Size
	used, err := admitRetainedSuiteBytesV3(t.Context(), fixture, size)
	if err != nil || used != size || fixture.closed {
		t.Fatalf("exact budget: bytes=%d closed=%t err=%v", used, fixture.closed, err)
	}
	excess, _, _ := suiteTestV3(t)
	_, err = admitRetainedSuiteBytesV3(t.Context(), excess, size-used)
	requireCode(t, err, e.CodeVerifyFailed)
	if !excess.closed {
		t.Fatal("excess held observation was not closed immediately")
	}
	if fixture.closed {
		t.Fatal("previous accepted observation was incorrectly closed")
	}
	canceled, _, _ := suiteTestV3(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = admitRetainedSuiteBytesV3(ctx, canceled, maxRetainedSuiteBytesV3)
	requireCode(t, err, e.CodeTimeout)
	if !canceled.closed {
		t.Fatal("canceled intake retained observation bytes")
	}
}

func TestConfiguredFilesV3OwnsLoadAndPrivateModeAdmission(t *testing.T) {
	options := filesOptionsV3Fixture(t)
	configuration := SuiteConfigurationV3{SchemaVersion: SuiteConfigurationSchemaV3, Policy: options.Policy, Suites: options.Suites}
	raw, err := canonical.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "configuration.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = PublishFilesV3(ctx)
	requireCode(t, err, e.CodeTimeout)
	options.Policy = PolicyV3{}
	options.Suites = nil
	_, err = PublishFilesV3(t.Context())
	requireCode(t, err, e.CodeEngineCapability)
	for _, mode := range []os.FileMode{0640, 0620, 0604, 0602} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		_, err = LoadSuiteConfigurationV3(t.Context(), path)
		requireCode(t, err, e.CodeVerifyFailed)
		if !strings.Contains(err.Error(), "owner-only") {
			t.Fatal(err)
		}
	}
}

func TestSuiteConfigurationV3ReadBoundaryPreservesCancellationCode(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	expired, finish := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer finish()
	for _, tc := range []struct {
		name     string
		ctx      context.Context
		cause    error
		code     e.Code
		expected error
	}{
		{"context canceled during hold", canceled, errors.New("hold interrupted"), e.CodeTimeout, context.Canceled},
		{"context deadline during read", expired, errors.New("read interrupted"), e.CodeTimeout, context.DeadlineExceeded},
		{"operation cancellation", t.Context(), context.Canceled, e.CodeTimeout, context.Canceled},
		{"operation deadline", t.Context(), context.DeadlineExceeded, e.CodeTimeout, context.DeadlineExceeded},
		{"ordinary file refusal", t.Context(), os.ErrPermission, e.CodeVerifyFailed, os.ErrPermission},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := suiteConfigurationReadErrorV3(tc.ctx, tc.cause, "holding suite configuration")
			requireCode(t, err, tc.code)
			if !errors.Is(err, tc.expected) {
				t.Fatal("read boundary lost original cause")
			}
		})
	}
}
