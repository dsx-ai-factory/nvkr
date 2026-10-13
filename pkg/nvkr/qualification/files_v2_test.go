// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
)

func filesOptionsV2Fixture(t *testing.T) FilesOptionsV2 {
	t.Helper()
	_, checks, requirement := suiteTestV2(t)
	base := fileOptionsFixture()
	source := checks.description.Origin
	base.ArtifactFiles.Source = qualificationorigin.SourceRequest{Repository: source.Repository, RunID: source.RunID, Attempt: source.Attempt, Commit: source.Commit, Workflow: source.Workflow, DefaultBranch: source.DefaultBranch}
	return FilesOptionsV2{ArtifactFiles: base.ArtifactFiles, QualificationWorkflow: base.QualificationWorkflow, Signer: base.Signer,
		Policy: PolicyV2{RequiredSuites: []SuiteRequirement{requirement}},
		Suites: []SuiteLocatorV2{{Name: requirement.Expected.Suite.Name, Locator: testobservation.Locator{SchemaVersion: testobservation.LocatorSchemaVersion, Reference: "registry.example.test/observations@sha256:" + strings.Repeat("a", 64), ObservationDigest: requirement.Expected.ObservationDigest}, Registry: testobservation.RegistryOptions{Transport: delivery.RegistryOptions{Anonymous: true}, CosignBinary: base.Signer.CosignBinary, CosignDigest: base.Signer.CosignDigest, Identity: requirement.Identity, Issuer: requirement.Issuer}}}}
}
func TestFilesV2CompleteAdmissionBeforeExternalWork(t *testing.T) {
	if err := admitFilesV2(filesOptionsV2Fixture(t)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*FilesOptionsV2)
		code e.Code
	}{
		{"missing required suite", func(o *FilesOptionsV2) { o.Suites = nil }, e.CodeGateUnmet},
		{"extra suite locator", func(o *FilesOptionsV2) { o.Suites = append(o.Suites, o.Suites[0]) }, e.CodeGateUnmet},
		{"wrong suite name", func(o *FilesOptionsV2) { o.Suites[0].Name = "other" }, e.CodeGateUnmet},
		{"wrong locator digest", func(o *FilesOptionsV2) { o.Suites[0].Locator.ObservationDigest = "sha256:" + strings.Repeat("b", 64) }, e.CodeGateUnmet},
		{"tag locator", func(o *FilesOptionsV2) { o.Suites[0].Locator.Reference = "registry.example.test/observations:latest" }, e.CodeUnpinnedInput},
		{"off-loopback plaintext", func(o *FilesOptionsV2) { o.Suites[0].Registry.Transport.PlainHTTP = true }, e.CodeUsage},
		{"invalid UTF8 trust identity", func(o *FilesOptionsV2) { o.Policy.RequiredSuites[0].Identity = "\xff" }, e.CodeTrustPolicyMissing},
		{"ANSI trust identity", func(o *FilesOptionsV2) { o.Policy.RequiredSuites[0].Identity = "signer\x1b[31m" }, e.CodeTrustPolicyMissing},
		{"bidi trust identity", func(o *FilesOptionsV2) { o.Policy.RequiredSuites[0].Identity = "signer\u202e" }, e.CodeTrustPolicyMissing},
		{"wrong signer", func(o *FilesOptionsV2) { o.Suites[0].Registry.Identity = "other" }, e.CodeUntrustedSigner},
		{"wrong issuer", func(o *FilesOptionsV2) { o.Suites[0].Registry.Issuer = "other" }, e.CodeUntrustedSigner},
		{"different suite verifier pin", func(o *FilesOptionsV2) { o.Suites[0].Registry.CosignDigest = "sha256:" + strings.Repeat("b", 64) }, e.CodeUnpinnedInput},
		{"different artifact verifier pin", func(o *FilesOptionsV2) { o.ArtifactFiles.Consumer.CosignDigest = "sha256:" + strings.Repeat("b", 64) }, e.CodeUnpinnedInput},
		{"installed byte proof unavailable", func(o *FilesOptionsV2) { o.Policy.RequiredSuites[0].RequireAuthenticatedDeployment = true }, e.CodeEngineCapability},
		{"same build workflow", func(o *FilesOptionsV2) { o.QualificationWorkflow = o.ArtifactFiles.Source.Workflow }, e.CodeTrustPolicyMissing},
		{"malformed qualification workflow", func(o *FilesOptionsV2) { o.QualificationWorkflow = "../../unapproved" }, e.CodeTrustPolicyMissing},
		{"missing original candidate", func(o *FilesOptionsV2) { o.ArtifactFiles.CandidatePath = "" }, e.CodeUsage},
		{"invalid source commit", func(o *FilesOptionsV2) { o.ArtifactFiles.Source.Commit = "latest" }, e.CodeTrustPolicyMissing},
		{"implicit credential helper unavailable", func(o *FilesOptionsV2) { o.Suites[0].Registry.Transport.Anonymous = false }, e.CodeTrustPolicyMissing},
		{"credential host substitution", func(o *FilesOptionsV2) {
			o.Suites[0].Registry.Transport = delivery.RegistryOptions{Username: "fixture", Password: "fixture"}
			o.Suites[0].Registry.Credential = sign.Credential{Registry: "wrong.example", Username: "fixture", Password: "fixture"}
		}, e.CodeUsage},
		{"anonymous credential contradiction", func(o *FilesOptionsV2) {
			o.Suites[0].Registry.Credential = sign.Credential{Registry: "registry.example.test", Username: "fixture", Password: "fixture"}
		}, e.CodeUsage},
		{"invalid credential", func(o *FilesOptionsV2) { o.Suites[0].Registry.Credential.Registry = "registry.example.test" }, e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := filesOptionsV2Fixture(t)
			tc.edit(&options)
			requireCode(t, admitFilesV2(options), tc.code)
			// Paths and hostnames are deliberately unavailable. Invalid configuration
			// must preserve its admission error before touching any one of them.
			_, err := PublishFilesV2(t.Context(), options)
			requireCode(t, err, tc.code)
		})
	}
}
func TestFilesV2ContextAndStockExecutableAdmission(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := PublishFilesV2(ctx, filesOptionsV2Fixture(t))
	requireCode(t, err, e.CodeTimeout)
	_, err = PublishFilesV2(t.Context(), filesOptionsV2Fixture(t))
	code := e.CodeEngineCapability
	requireCode(t, err, code)
}

func TestLoadSuiteConfigurationV2StrictHeldInput(t *testing.T) {
	options := filesOptionsV2Fixture(t)
	configuration := SuiteConfigurationV2{SchemaVersion: SuiteConfigurationSchemaV2, Policy: options.Policy, Suites: options.Suites}
	raw, err := canonical.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "suite-configuration.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSuiteConfigurationV2(t.Context(), path)
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
		{"unknown schema", []byte(strings.Replace(string(raw), SuiteConfigurationSchemaV2, "nvkr.dev/qualification-suite-configuration/v999", 1))},
		{"unknown field", []byte(strings.Replace(string(raw), `"schemaVersion":`, `"unknown":1,"schemaVersion":`, 1))},
		{"duplicate schema", []byte(strings.Replace(string(raw), `"schemaVersion":`, `"schemaVersion":"duplicate","schemaVersion":`, 1))},
		{"noncanonical", append(append([]byte(nil), raw...), '\n')},
		{"oversized", []byte(strings.Repeat(" ", maxSuiteConfigurationV2Bytes+1))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, tc.raw, 0600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadSuiteConfigurationV2(t.Context(), path)
			requireCode(t, err, e.CodeVerifyFailed)
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = LoadSuiteConfigurationV2(ctx, "does-not-exist")
	requireCode(t, err, e.CodeTimeout)
	_, err = LoadSuiteConfigurationV2(t.Context(), "")
	requireCode(t, err, e.CodeUsage)
}
func TestLoadSuiteConfigurationV2RejectsSignerMismatchWithoutPrintingCredential(t *testing.T) {
	options := filesOptionsV2Fixture(t)
	const secret = "PRIVATE_CREDENTIAL_VALUE_MUST_NOT_APPEAR"
	options.Suites[0].Registry.Transport = delivery.RegistryOptions{Username: "fixture", Password: secret}
	options.Suites[0].Registry.Credential = sign.Credential{Registry: "registry.example.test", Username: "fixture", Password: secret}
	options.Suites[0].Registry.Identity = "different"
	configuration := SuiteConfigurationV2{SchemaVersion: SuiteConfigurationSchemaV2, Policy: options.Policy, Suites: options.Suites}
	raw, err := canonical.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "private-config.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = LoadSuiteConfigurationV2(t.Context(), path)
	requireCode(t, err, e.CodeUntrustedSigner)
	if strings.Contains(err.Error(), secret) {
		t.Fatal("loader exposed credential content")
	}
}

func (f *suiteFixtureV2) Close() error { f.closed = true; return nil }
func TestFilesV2AggregateRetainedByteBudget(t *testing.T) {
	fixture, _, _ := suiteTestV2(t)
	raw, err := fixture.observation.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	size := int64(len(raw)) + fixture.observation.Files[0].Size
	used, err := admitRetainedSuiteBytesV2(t.Context(), fixture, size)
	if err != nil || used != size || fixture.closed {
		t.Fatalf("exact budget: bytes=%d closed=%t err=%v", used, fixture.closed, err)
	}
	excess, _, _ := suiteTestV2(t)
	_, err = admitRetainedSuiteBytesV2(t.Context(), excess, size-used)
	requireCode(t, err, e.CodeVerifyFailed)
	if !excess.closed {
		t.Fatal("excess held observation was not closed immediately")
	}
	if fixture.closed {
		t.Fatal("previous accepted observation was incorrectly closed")
	}
	canceled, _, _ := suiteTestV2(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = admitRetainedSuiteBytesV2(ctx, canceled, maxRetainedSuiteBytesV2)
	requireCode(t, err, e.CodeTimeout)
	if !canceled.closed {
		t.Fatal("canceled intake retained observation bytes")
	}
}

func TestConfiguredFilesV2OwnsLoadAndPrivateModeAdmission(t *testing.T) {
	options := filesOptionsV2Fixture(t)
	configuration := SuiteConfigurationV2{SchemaVersion: SuiteConfigurationSchemaV2, Policy: options.Policy, Suites: options.Suites}
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
	_, err = PublishConfiguredFilesV2(ctx, "does-not-exist", options)
	requireCode(t, err, e.CodeTimeout)
	options.Policy = PolicyV2{}
	options.Suites = nil
	_, err = PublishConfiguredFilesV2(t.Context(), path, options)
	requireCode(t, err, e.CodeEngineCapability)
	for _, mode := range []os.FileMode{0640, 0620, 0604, 0602} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		_, err = LoadSuiteConfigurationV2(t.Context(), path)
		requireCode(t, err, e.CodeVerifyFailed)
		if !strings.Contains(err.Error(), "owner-only") {
			t.Fatal(err)
		}
	}
}
