// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func TestTriggeredV3CancellationBeforePolicyIO(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := PublishTriggeredRunV3(ctx, "/not-opened/native.yaml", "/not-opened/suites.json", lifecycle.QualificationRuntimeOptions{})
	requireCode(t, err, e.CodeTimeout)
	if result != nil {
		t.Fatal("partial output before admission")
	}
}
func TestTriggeredV3IndependentNativeAdmission(t *testing.T) {
	configuration := filesOptionsV3Fixture(t)
	native := evidenceFixture().ToolSmoke.Policy
	native.Cosign.Path = configuration.Suites[0].Registry.CosignBinary
	if err := admitTriggeredSuiteConfigurationV3(native, configuration); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*lifecycle.QualificationPolicy, *SuiteConfigurationV3)
		code e.Code
	}{
		{"missing native smoke", func(n *lifecycle.QualificationPolicy, _ *SuiteConfigurationV3) { n.Smoke = nil }, e.CodeTrustPolicyMissing},
		{"unimplemented native required field", func(n *lifecycle.QualificationPolicy, _ *SuiteConfigurationV3) {
			n.RequiredSuites = []string{"required-native"}
		}, e.CodeEngineCapability},
		{"different complete native pin", func(_ *lifecycle.QualificationPolicy, c *SuiteConfigurationV3) {
			c.Policy.NativePolicyDigest = "sha256:" + strings.Repeat("b", 64)
		}, e.CodeTrustPolicyMissing},
		{"different suite verifier", func(_ *lifecycle.QualificationPolicy, c *SuiteConfigurationV3) {
			c.Suites[0].Registry.CosignDigest = "sha256:" + strings.Repeat("b", 64)
		}, e.CodeUnpinnedInput},
		{"different suite executable", func(_ *lifecycle.QualificationPolicy, c *SuiteConfigurationV3) {
			c.Suites[0].Registry.CosignBinary = "/other/cosign"
		}, e.CodeUnpinnedInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := filesOptionsV3Fixture(t)
			n := evidenceFixture().ToolSmoke.Policy
			n.Cosign.Path = c.Suites[0].Registry.CosignBinary
			tc.edit(&n, &c)
			requireCode(t, admitTriggeredSuiteConfigurationV3(n, c), tc.code)
		})
	}
}
func TestTriggeredV3GeneratedPolicyGateBeforeProviderAndSigner(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("native qualification is Linux only")
	}
	root := t.TempDir()
	t.Chdir(root)
	native := evidenceFixture().ToolSmoke.Policy
	native.Source.Workflow = ".github/workflows/nvkr-run.yml"
	native.Cosign.Path = "/must-not-execute/cosign"
	c := filesOptionsV3Fixture(t)
	digest, err := native.Digest()
	if err != nil {
		t.Fatal(err)
	}
	c.Policy.NativePolicyDigest = digest
	raw, err := canonical.Marshal(native)
	if err != nil {
		t.Fatal(err)
	}
	nativePath := filepath.Join(root, "native.json")
	if err := os.WriteFile(nativePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	raw, err = canonical.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	suitePath := filepath.Join(root, "suite.json")
	if err := os.WriteFile(suitePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "must-not-create")
	result, err := PublishTriggeredRunV3(t.Context(), nativePath, suitePath, lifecycle.QualificationRuntimeOptions{WorkspaceRoot: workspace})
	requireCode(t, err, e.CodePipelineDrift)
	if result != nil {
		t.Fatal("partial result before generated gate")
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatal("provider workspace created before admission")
	}
}
