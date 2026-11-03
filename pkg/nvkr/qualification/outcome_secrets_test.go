// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Every value the generated qualify step's process sees bound to a secret or the
// job token (workflow, job and step env), and the OIDC request token its
// id-token: write grant adds, must be redacted.
func TestQualificationGenerationOutcomeSecretsCoverStep(t *testing.T) {
	bundle, policy := generationBundle(t)
	workflow := decodeQualificationWorkflow(t, bundle.files[policy.QualificationWorkflow])
	qualify := workflow.Jobs["qualify"]
	if qualify.Permissions["id-token"] != "write" || !slices.Contains(outcomeSecretEnv, "ACTIONS_ID_TOKEN_REQUEST_TOKEN") {
		t.Fatal("the OIDC request token of the id-token: write job is not redacted")
	}
	scopes := []map[string]string{workflow.Env, qualify.Env}
	for _, step := range qualify.Steps {
		if strings.Contains(step.Run, "./nvkr-exec run qualify ") {
			scopes = append(scopes, step.Env)
		}
	}
	covered := 0
	for _, env := range scopes {
		for name, value := range env {
			if !strings.Contains(value, "secrets.") && !strings.Contains(value, "github.token") {
				continue
			}
			covered++
			if !slices.Contains(outcomeSecretEnv, name) && !strings.HasPrefix(name, outcomeSourceTokenPrefix) {
				t.Errorf("qualify step secret %s is not redacted from the outcome", name)
			}
		}
	}
	if covered < 4 {
		t.Fatalf("found %d secret-bound env vars on the qualify step; the scan no longer sees them", covered)
	}
}

func TestQualificationOutcomeSecrets(t *testing.T) {
	environ := []string{"NVKR_GITHUB_TOKEN=github-token-value", "NVKR_REGISTRY_PASSWORD=registry-password-value", "NVKR_REGISTRY_USER=registry-user",
		"NVKR_CI_STORAGE_TOKEN=", "NVKR_PIN_SOURCE_TOKEN_0=source-token-value", "ACTIONS_ID_TOKEN_REQUEST_TOKEN=oidc-request-token", "HOME=/home/runner"}
	values, err := OutcomeSecrets(environ)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"github-token-value", "registry-password-value", "source-token-value", "oidc-request-token"}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("got %v, want %v", values, want)
	}
	for _, short := range []string{"NVKR_REGISTRY_PASSWORD=abc", "NVKR_PIN_SOURCE_TOKEN_1= x ", "NVKR_CI_STORAGE_TOKEN=long-enough-line\nabc"} {
		_, err := OutcomeSecrets(append(slices.Clone(environ), short))
		var coded *e.Error
		if !errors.As(err, &coded) || coded.Code != e.CodeUsage || strings.Contains(err.Error(), "abc") {
			t.Fatalf("%s: got %v, want a usage error that does not repeat the value", short, err)
		}
	}
}
