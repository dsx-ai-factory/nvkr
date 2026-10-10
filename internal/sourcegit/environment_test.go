// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sourcegit

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func testPath(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	if err := os.WriteFile(filepath.Join(path, "git"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func errorCode(err error) e.Code {
	var coded *e.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

func TestBaselineUsesSelectedPathNotAmbientSearch(t *testing.T) {
	path := testPath(t)
	t.Setenv("PATH", "untrusted-relative-path")
	for _, test := range []struct {
		name, path string
		valid      bool
	}{
		{"selected absolute", path, true},
		{"relative", "." + string(os.PathListSeparator) + path, false},
		{"trailing empty", path + string(os.PathListSeparator), false},
		{"missing Git", t.TempDir(), false},
		{"missing PATH", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := Baseline(test.path)
			if test.valid {
				if err != nil || !slices.Contains(got, "PATH="+path) {
					t.Fatalf("explicit selected Git path not used: %v / %v", got, err)
				}
			} else if got != nil || errorCode(err) != e.CodeEngineCapability {
				t.Fatalf("invalid Git path admitted: %v / %v", got, err)
			}
		})
	}
}

func TestBaselinePathFailureIdentifiesEntryWithoutPrivateValues(t *testing.T) {
	selected := testPath(t)
	separator := string(os.PathListSeparator)
	for _, row := range []struct {
		name, input, diagnostic string
	}{
		{"missing", "", "nonempty PATH"},
		{"relative first", "./private-path" + separator + selected, "PATH entry 1"},
		{"relative second", selected + separator + "./private-path", "PATH entry 2"},
		{"empty second", selected + separator, "PATH entry 2"},
		{"empty middle", selected + separator + separator + selected, "PATH entry 2"},
	} {
		t.Run(row.name, func(t *testing.T) {
			environment, err := Baseline(row.input)
			if environment != nil || errorCode(err) != e.CodeEngineCapability {
				t.Fatalf("invalid PATH admission changed: %v %v", environment, err)
			}
			if !strings.Contains(err.Error(), row.diagnostic) || strings.Contains(err.Error(), "private-path") || strings.Contains(err.Error(), selected) {
				t.Fatalf("PATH diagnostic lacks its entry or exposes private values: %v", err)
			}
		})
	}
}

func TestAdmitRejectsCallerEnvironmentSubstitution(t *testing.T) {
	baseline, err := Baseline(testPath(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func([]string) []string
	}{
		{"arbitrary helper", func(env []string) []string { return replace(env, "GIT_CONFIG_VALUE_0", "!run-secret-stealer") }},
		{"global config", func(env []string) []string { return replace(env, "GIT_CONFIG_GLOBAL", "/secret/config") }},
		{"HOME", func(env []string) []string { return append(env, "HOME=/secret/home") }},
		{"token without mirror", func(env []string) []string { return append(env, Token+"=synthetic") }},
		{"proxy", func(env []string) []string { return append(env, "HTTP_PROXY=http://proxy.invalid") }},
		{"duplicate", func(env []string) []string { return append(env, "GIT_CONFIG_VALUE_0=") }},
		{"missing empty reset", func(env []string) []string {
			return slices.DeleteFunc(env, func(pair string) bool { return strings.HasPrefix(pair, "GIT_CONFIG_VALUE_0=") })
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, env, err := Admit("https://example.test/source", test.change(slices.Clone(baseline)))
			if path != "" || env != nil || errorCode(err) != e.CodeEngineCapability {
				t.Fatalf("unselected environment admitted: %s / %v / %v", path, env, err)
			}
		})
	}
}

func TestAdmitExactPrivateRepositoryConfiguration(t *testing.T) {
	path := testPath(t)
	repository := "https://example.test/source"
	private := []string{"PATH=" + path, "LC_ALL=C", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_COUNT=4", "GIT_CONFIG_KEY_0=url.https://mirror.test/source.insteadOf", "GIT_CONFIG_VALUE_0=" + repository,
		"GIT_CONFIG_KEY_1=credential.helper", "GIT_CONFIG_VALUE_1=", "GIT_CONFIG_KEY_2=credential.https://mirror.test/source.helper",
		"GIT_CONFIG_VALUE_2=" + CredentialHelper, "GIT_CONFIG_KEY_3=credential.useHttpPath", "GIT_CONFIG_VALUE_3=true", Token + "=synthetic-token"}
	for _, test := range []struct {
		name, key, value string
		code             e.Code
	}{
		{"valid", "", "", ""},
		{"wrong repository", "GIT_CONFIG_VALUE_0", "https://foreign.test/source", e.CodeEngineCapability},
		{"credential mirror mismatch", "GIT_CONFIG_KEY_2", "credential.https://foreign.test/source.helper", e.CodeEngineCapability},
		{"arbitrary helper", "GIT_CONFIG_VALUE_2", "!arbitrary-code", e.CodeEngineCapability},
		{"non HTTPS mirror", "GIT_CONFIG_KEY_0", "url.http://mirror.test/source.insteadOf", e.CodeEngineCapability},
		{"empty token", Token, "", e.CodeFetchFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			environment := slices.Clone(private)
			if test.key != "" {
				environment = replace(environment, test.key, test.value)
			}
			executable, admitted, err := Admit(repository, environment)
			if test.code == "" {
				if err != nil || executable != filepath.Join(path, "git") || len(admitted) != len(private) {
					t.Fatalf("exact private configuration not admitted: %s / %v / %v", executable, admitted, err)
				}
			} else if errorCode(err) != test.code || executable != "" || admitted != nil {
				t.Fatalf("unsafe private configuration admitted: %s / %v / %v", executable, admitted, err)
			}
		})
	}
}

func TestAdmitHeldPublicRepositoryTransport(t *testing.T) {
	baseline, err := Baseline(testPath(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		repository string
		allowed    bool
	}{
		{"https://example.test/repository", true},
		{filepath.Join(t.TempDir(), "repository"), true},
		{"file:///tmp/repository", true},
		{"ssh://example.test/repository", false},
		{"git://example.test/repository", false},
		{"http://example.test/repository", false},
		{"ext::arbitrary-command", false},
		{"relative/repository", false},
		{"file://foreign.test/tmp/repository", false},
		{"file:///tmp/repository?query", false},
	} {
		t.Run(test.repository, func(t *testing.T) {
			path, environment, err := Admit(test.repository, baseline)
			if test.allowed {
				if err != nil || path == "" || environment == nil {
					t.Fatalf("supported held transport rejected: %v", err)
				}
			} else if errorCode(err) != e.CodeEngineCapability || path != "" || environment != nil {
				t.Fatalf("unsupported held transport admitted: %v", err)
			}
		})
	}
}

func replace(environment []string, key, value string) []string {
	for index, pair := range environment {
		if strings.HasPrefix(pair, key+"=") {
			environment[index] = key + "=" + value
			return environment
		}
	}
	return append(environment, key+"="+value)
}
