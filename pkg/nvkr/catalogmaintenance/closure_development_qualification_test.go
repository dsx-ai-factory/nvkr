// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/development"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
)

func TestRefreshMaintainsDevelopmentQualificationAlongsideProduction(t *testing.T) {
	cfg, promote := qualificationFixture(t)
	cfg.DevelopmentEnvironment = "environment-dev.yaml"
	cfg.DevelopmentQualificationPolicy = "qualification-dev.yaml"
	namespace, err := development.ParseNamespace("ghcr.io/example/nvkr/development")
	if err != nil {
		t.Fatal(err)
	}
	env := string(readFixture(t, cfg.Environment))
	env = strings.Replace(env, "credentialRef: github:GITHUB_TOKEN", "credentialRef: github:NVKR_DEV_REGISTRY_WRITE", 1)
	env = strings.Replace(env, "repositoryPrefix: ghcr.io/example/nvkr/fixture", "repositoryPrefix: ghcr.io/example/nvkr/development", 1)
	start := strings.Index(env, "  identity: ")
	end := strings.Index(env[start:], "\n") + start
	env = env[:start] + "  identity: '" + namespace.Identity() + "'" + env[end:] + "githubDevelopment: true\n"
	writeFixture(t, cfg.DevelopmentEnvironment, []byte(env))
	policy := strings.NewReplacer("source: {repository:", "source: {development: true, repository:", "nvkr-run.yml", "nvkr-dev.yml", "nvkr-qualify.yml", "nvkr-dev-qualify.yml", "nvkr-smoke.yml", "nvkr-dev-smoke.yml").Replace(string(readFixture(t, cfg.QualificationPolicy)))
	writeFixture(t, cfg.DevelopmentQualificationPolicy, []byte(policy))
	if _, err := qualification.GenerateFiles(t.Context(), cfg.Definition, cfg.DevelopmentEnvironment, cfg.DevelopmentQualificationPolicy, "test (deadbeef)", "github", ".", false); err != nil {
		t.Fatal(err)
	}
	before := readFixture(t, qualification.DevelopmentQualificationManifestPath)
	promote("1.1.0")
	result, err := Refresh(t.Context(), cfg, "test (deadbeef)", false, "")
	if err != nil || !result.Changed {
		t.Fatalf("refresh: %+v %v", result, err)
	}
	if bytes.Equal(before, readFixture(t, qualification.DevelopmentQualificationManifestPath)) {
		t.Fatal("development qualification retained stale Plan")
	}
	if err := checkQualification(t, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := qualification.GenerateFiles(t.Context(), cfg.Definition, cfg.DevelopmentEnvironment, cfg.DevelopmentQualificationPolicy, "test (deadbeef)", "github", ".", true); err != nil {
		t.Fatal(err)
	}
}
