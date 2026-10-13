// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package development owns the fixed disposable publication namespace contract.
// Its parsed values describe scope; they do not authenticate a GitHub execution.
package development

import (
	"regexp"
	"strconv"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const (
	Issuer             = "https://token.actions.githubusercontent.com"
	WriteSecret        = "NVKR_DEV_REGISTRY_WRITE"
	ReadSecret         = "NVKR_DEV_REGISTRY_READ"
	WriteCredentialRef = "github:" + WriteSecret
	ReadCredentialRef  = "github:" + ReadSecret
	WriteUserVariable  = "NVKR_DEV_REGISTRY_WRITE_USER"
	ReadUserVariable   = "NVKR_DEV_REGISTRY_READ_USER"
)

var namespacePattern = regexp.MustCompile(`^ghcr\.io/([a-z0-9](?:[a-z0-9-]*[a-z0-9])?)/([a-z0-9](?:[a-z0-9._-]*[a-z0-9])?)/development$`)
var leafPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)

// Namespace is an admitted repository-owned development namespace.
type Namespace struct{ prefix, repository string }

func ParseNamespace(prefix string) (Namespace, error) {
	parts := namespacePattern.FindStringSubmatch(prefix)
	if parts == nil {
		return Namespace{}, e.New(e.CodeEngineCapability, "development publication requires ghcr.io/<owner>/<repo>/development")
	}
	return Namespace{prefix: prefix, repository: parts[1] + "/" + parts[2]}, nil
}

func (namespace Namespace) Prefix() string     { return namespace.prefix }
func (namespace Namespace) Repository() string { return namespace.repository }
func (namespace Namespace) Identity() string {
	if namespace.prefix == "" {
		return ""
	}
	return "^" + regexp.QuoteMeta("https://github.com/"+namespace.repository+"/.github/workflows/nvkr-dev.yml@refs/heads/") + "[^[:space:]]+$"
}

// Reference describes one repository in exactly one disposable run attempt.
type Reference struct{ reference, runID, attempt, leaf string }

func (reference Reference) String() string  { return reference.reference }
func (reference Reference) RunID() string   { return reference.runID }
func (reference Reference) Attempt() string { return reference.attempt }
func (reference Reference) Leaf() string    { return reference.leaf }

func (namespace Namespace) Reference(runID, attempt, leaf string) (string, error) {
	parsed, err := namespace.ParseReference(namespace.prefix + "/runs/" + runID + "/attempts/" + attempt + "/" + leaf)
	if err != nil {
		return "", err
	}
	return parsed.String(), nil
}

// ValidateLeaf admits one canonical sibling repository before compilation or
// execution. It matches the publication envelope's existing repository grammar.
func (namespace Namespace) ValidateLeaf(leaf string) error {
	if namespace.prefix == "" || !leafPattern.MatchString(leaf) {
		return e.New(e.CodeEngineCapability, "development output %q requires a canonical sibling publication repository", leaf)
	}
	return nil
}

// ParseReference rejects adjacent namespaces, tags, digests and ambiguous IDs.
func (namespace Namespace) ParseReference(repository string) (Reference, error) {
	prefix := namespace.prefix + "/runs/"
	if namespace.prefix == "" || !strings.HasPrefix(repository, prefix) {
		return Reference{}, e.New(e.CodeEngineCapability, "publication is outside the admitted development namespace")
	}
	parts := strings.Split(strings.TrimPrefix(repository, prefix), "/")
	if len(parts) != 4 || parts[1] != "attempts" || !positiveID(parts[0]) || !positiveID(parts[2]) {
		return Reference{}, e.New(e.CodeEngineCapability, "development publication requires canonical run, attempt and repository names")
	}
	if err := namespace.ValidateLeaf(parts[3]); err != nil {
		return Reference{}, err
	}
	return Reference{reference: repository, runID: parts[0], attempt: parts[2], leaf: parts[3]}, nil
}

func positiveID(value string) bool {
	n, err := strconv.ParseUint(value, 10, 64)
	return err == nil && n != 0 && strconv.FormatUint(n, 10) == value
}
