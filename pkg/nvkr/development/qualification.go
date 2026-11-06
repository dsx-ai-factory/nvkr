// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package development

import (
	"regexp"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

var producerWorkflowPattern = regexp.MustCompile(`^\.github/workflows/nvkr-dev-([a-z0-9](?:[a-z0-9-]{0,46}[a-z0-9])?)\.yml$`)

// BundleForProducer admits the exact generated development producer namespace.
func BundleForProducer(workflow string) (string, error) {
	if workflow == ProducerWorkflow {
		return "", nil
	}
	parts := producerWorkflowPattern.FindStringSubmatch(workflow)
	if parts == nil {
		return "", e.New(e.CodeEngineCapability, "unsupported development producer workflow")
	}
	if err := ValidateBundle(parts[1]); err != nil {
		return "", err
	}
	return parts[1], nil
}

// QualificationWorkflows derives the isolated qualifier and worker for a producer.
func QualificationWorkflows(workflow string) (string, string, error) {
	bundle, err := BundleForProducer(workflow)
	if err != nil {
		return "", "", err
	}
	if bundle == "" {
		return QualificationWorkflow, SmokeWorkflow, nil
	}
	return ".github/workflows/nvkr-dev-qualify-" + bundle + ".yml", ".github/workflows/nvkr-dev-smoke-" + bundle + ".yml", nil
}

// NamespaceForProducer derives scope from an independently admitted source workflow.
func NamespaceForProducer(repository, workflow string) (Namespace, error) {
	bundle, err := BundleForProducer(workflow)
	if err != nil {
		return Namespace{}, err
	}
	suffix := ""
	if bundle != "" {
		suffix = "-" + bundle
	}
	return ParseNamespace("ghcr.io/" + strings.ToLower(repository) + "/development" + suffix)
}
