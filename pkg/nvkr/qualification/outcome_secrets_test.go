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

// outcomeNonSecretBindings are the reviewed expressions the qualify step's process
// may see without redaction. Any other expression-bound variable must be redacted
// (outcomeSecretEnv), so a new binding, such as a minted token from a step output,
// fails until it is reviewed.
var outcomeNonSecretBindings = []string{"${{ vars.NVKR_CI_STORAGE_READ_USER }}", "${{ vars.NVKR_REGISTRY_WRITE_USER }}"}

// outcomeBindingFindings names each expression-bound variable in scopes that the
// outcome neither redacts nor lists as a reviewed non-secret binding, and counts
// the redacted ones.
func outcomeBindingFindings(scopes []map[string]string) (findings []string, redacted int) {
	for _, env := range scopes {
		for name, value := range env {
			switch {
			case !strings.Contains(value, "${{"):
			case slices.Contains(outcomeSecretEnv, name) || strings.HasPrefix(name, outcomeSourceTokenPrefix):
				redacted++
			case !slices.Contains(outcomeNonSecretBindings, value):
				findings = append(findings, name)
			}
		}
	}
	slices.Sort(findings)
	return findings, redacted
}

// Every expression the generated qualify step's process sees (workflow, job and
// step env) is a redacted secret or a reviewed non-secret binding, its script
// interpolates none, and the OIDC request token its id-token: write grant adds is
// redacted.
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
			if strings.Contains(step.Run, "${{") {
				t.Error("the qualify step's script interpolates an expression; bind it through env instead")
			}
		}
	}
	findings, redacted := outcomeBindingFindings(scopes)
	if len(findings) != 0 {
		t.Errorf("qualify step variables %v are neither redacted from the outcome nor reviewed non-secret bindings", findings)
	}
	// Public source retrieval no longer adds a source-token binding.
	if redacted != 3 {
		t.Fatalf("found %d redacted secret bindings on the qualify step; the scan no longer sees them", redacted)
	}
	for _, binding := range []map[string]string{
		{"NVKR_APP_TOKEN": "${{ steps.app.outputs.token }}"},
		{"NVKR_SECRETS": "${{ toJSON(secrets) }}"},
		{"NVKR_REGISTRY_USER": "${{ secrets.NVKR_REGISTRY_WRITE_USER }}"},
	} {
		if findings, _ := outcomeBindingFindings([]map[string]string{binding}); len(findings) != 1 {
			t.Errorf("binding %v was not reported", binding)
		}
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
