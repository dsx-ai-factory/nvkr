// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package sourcegit owns admitted child environments for held source Git and
// ordinary Local Git. Their credential and transport policies remain distinct.
// It describes process configuration, not source, build or release authority.
package sourcegit

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const Token = "NVKR_SOURCE_TOKEN"

// CredentialHelper is the existing repository-scoped Git helper. Never accept
// arbitrary caller-provided shell helpers in the held source route.
const CredentialHelper = `!f() { if [ "$1" = get ]; then printf '%s\n' 'username=x-access-token' "password=$NVKR_SOURCE_TOKEN"; fi; }; f`

// Baseline excludes ambient Git configuration, credentials and proxies. PATH is
// explicit and contains only absolute directories with an executable Git.
func Baseline(searchPath string) ([]string, error) {
	if _, err := executable(searchPath); err != nil {
		return nil, err
	}
	return []string{"PATH=" + searchPath, "LC_ALL=C", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0="}, nil
}

func executable(searchPath string) (string, error) {
	if searchPath == "" {
		return "", e.New(e.CodeEngineCapability, "source Git requires a nonempty PATH containing only absolute executable search directories")
	}
	directories := filepath.SplitList(searchPath)
	for index, directory := range directories {
		if !filepath.IsAbs(directory) {
			return "", e.New(e.CodeEngineCapability, "source Git PATH entry %d is empty or not absolute; provide only absolute executable search directories", index+1)
		}
	}
	for _, directory := range directories {
		// Reuse the standard library's executable check, not an approximate
		// permission test or the process's unrelated ambient PATH.
		if path, err := exec.LookPath(filepath.Join(directory, "git")); err == nil {
			return path, nil
		}
	}
	return "", e.New(e.CodeEngineCapability, "source Git requires Git on its admitted executable search path")
}

// Admit accepts only the fixed public baseline or the existing exact four-item
// credential configuration scoped to this source repository. Factory additionally
// binds that configuration to the selected complete Plan's SourceAccess mirror.
func Admit(repository string, environment []string) (string, []string, error) {
	fields := make(map[string]string, len(environment))
	for _, pair := range environment {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || key == "" {
			return "", nil, e.New(e.CodeEngineCapability, "source Git received an invalid environment field")
		}
		if _, present := fields[key]; present {
			return "", nil, e.New(e.CodeEngineCapability, "source Git received a duplicate environment field %q", key)
		}
		fields[key] = value
	}
	baseline, err := Baseline(fields["PATH"])
	if err != nil {
		return "", nil, err
	}
	expected := make(map[string]string, len(fields))
	for _, pair := range baseline {
		key, value, _ := strings.Cut(pair, "=")
		expected[key] = value
	}
	if fields["GIT_CONFIG_COUNT"] == "4" {
		mirrorKey := fields["GIT_CONFIG_KEY_0"]
		mirror := strings.TrimSuffix(strings.TrimPrefix(mirrorKey, "url."), ".insteadOf")
		parsed, err := url.Parse(mirror)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
			mirrorKey != "url."+mirror+".insteadOf" || repository == "" {
			return "", nil, e.New(e.CodeEngineCapability, "source Git requires its exact HTTPS repository mirror")
		}
		if fields[Token] == "" {
			return "", nil, e.New(e.CodeFetchFailed, "source fetch credential is empty")
		}
		for key, value := range map[string]string{
			"GIT_CONFIG_COUNT": "4", "GIT_CONFIG_KEY_0": mirrorKey, "GIT_CONFIG_VALUE_0": repository,
			"GIT_CONFIG_KEY_1": "credential.helper", "GIT_CONFIG_VALUE_1": "",
			"GIT_CONFIG_KEY_2": "credential." + mirror + ".helper", "GIT_CONFIG_VALUE_2": CredentialHelper,
			"GIT_CONFIG_KEY_3": "credential.useHttpPath", "GIT_CONFIG_VALUE_3": "true", Token: fields[Token],
		} {
			expected[key] = value
		}
	}
	if len(fields) != len(expected) {
		return "", nil, e.New(e.CodeEngineCapability, "source Git received unselected environment fields")
	}
	keys := make([]string, 0, len(expected))
	for key := range expected {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		if value, present := fields[key]; !present || value != expected[key] {
			return "", nil, e.New(e.CodeEngineCapability, "source Git is missing its exact configuration field %q", key)
		}
		result = append(result, key+"="+expected[key])
	}
	if repository != "" && fields[Token] == "" {
		// The held route supports maintained HTTPS sources and absolute local
		// repositories used by developer/native fixtures. Ordinary Local Git
		// has its separate LocalCommand admission and fetch-only credentials;
		// it additionally permits SSH, not arbitrary ambient configuration.
		parsed, err := url.Parse(repository)
		local := filepath.IsAbs(repository) && filepath.Clean(repository) == repository
		if err == nil && parsed.Scheme == "file" && parsed.Host == "" && parsed.User == nil &&
			parsed.RawQuery == "" && parsed.Fragment == "" && filepath.IsAbs(parsed.Path) && filepath.Clean(parsed.Path) == parsed.Path {
			local = true
		}
		https := err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
		if !local && !https {
			return "", nil, e.New(e.CodeEngineCapability, "held source fetching requires HTTPS or an absolute local repository; Git/SSH remain ordinary Local capabilities")
		}
	}
	path, err := executable(fields["PATH"])
	return path, result, err
}

// SearchPath reads only the caller's selected PATH, rejecting duplicate values.
func SearchPath(environment []string) (string, error) {
	var path string
	seen := false
	for _, pair := range environment {
		key, value, ok := strings.Cut(pair, "=")
		if ok && key == "PATH" {
			if seen {
				return "", e.New(e.CodeEngineCapability, "source Git received duplicate PATH")
			}
			path, seen = value, true
		}
	}
	if !seen {
		path = os.Getenv("PATH") // Existing callers without an explicit PATH use only this nonsecret selector.
	}
	return path, nil
}
