// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// GitHubActionRefs contains the catalog references admitted by the adapter.
type GitHubActionRefs struct{ Checkout, Upload, Download, SetupGo string }

// ResolveGitHubActions admits the action interfaces used by the GitHub renderer.
// Only supported repositories and major versions live in code; releases and
// commits belong to the catalog and are bound into each generated Run.
func ResolveGitHubActions(pins map[string]catalog.ActionPin) (GitHubActionRefs, error) {
	var refs GitHubActionRefs
	for _, requirement := range []struct {
		name, major string
		ref         *string
	}{
		{"checkout", "7", &refs.Checkout}, {"download-artifact", "8", &refs.Download},
		{"setup-go", "7", &refs.SetupGo}, {"upload-artifact", "7", &refs.Upload},
	} {
		pin, ok := pins[requirement.name]
		if !ok {
			return GitHubActionRefs{}, e.New(e.CodeUnpinnedSource, "catalog is missing GitHub action %q", requirement.name)
		}
		if err := pin.Validate(); err != nil {
			return GitHubActionRefs{}, err
		}
		if pin.Repo != "actions/"+requirement.name || !strings.HasPrefix(pin.Version, "v"+requirement.major+".") {
			return GitHubActionRefs{}, e.New(e.CodeEngineCapability, "GitHub adapter requires actions/%s major %s; got %s %s", requirement.name, requirement.major, pin.Repo, pin.Version)
		}
		*requirement.ref = pin.Uses()
	}
	return refs, nil
}

// ResolveGitHubAuthAction admits the cloud authentication action that only the
// generated qualification publish job uses (ADR-31 §7). Like the actions above,
// only its repository and major version live in code.
func ResolveGitHubAuthAction(pins map[string]catalog.ActionPin) (string, error) {
	pin, ok := pins["google-auth"]
	if !ok {
		return "", e.New(e.CodeUnpinnedSource, "catalog is missing GitHub action %q", "google-auth")
	}
	if err := pin.Validate(); err != nil {
		return "", err
	}
	if pin.Repo != "google-github-actions/auth" || !strings.HasPrefix(pin.Version, "v3.") {
		return "", e.New(e.CodeEngineCapability, "GitHub adapter requires google-github-actions/auth major 3; got %s %s", pin.Repo, pin.Version)
	}
	return pin.Uses(), nil
}
