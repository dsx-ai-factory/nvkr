// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/safetext"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
)

// Configuration is independently selected execution and deployment mapping.
// It cannot supply artifact receipts, credentials or passing test verdicts.
type PublishedSource struct {
	Reference string `json:"reference"`
	Policy    string `json:"policy"`
}

type Configuration struct {
	ToolsSource PublishedSource `json:"toolsSource"`
	Runner      Options         `json:"runner"`
	Timeouts    struct {
		Run     string `json:"run,omitempty"`
		Command string `json:"command,omitempty"`
		Cleanup string `json:"cleanup,omitempty"`
		Startup string `json:"startup,omitempty"`
	} `json:"timeouts"`
}

// RunReferenceFiles authenticates the immutable published source through the existing lifecycle
// owner, retains its receipt through observation, execution and cleanup, then
// closes it. Configuration is strict JSON; relative access and tools-policy paths resolve beside it.
// Results describe raw reports and API observations, never qualification or pass.
func RunReferenceFiles(ctx context.Context, reference, policyPath, configPath string, runtimeOptions lifecycle.QualificationRuntimeOptions) (output testresults.Output, resultErr error) {
	if ctx == nil {
		return testresults.Output{}, e.New(e.CodeUsage, "conformance requires context")
	}
	if err := ctx.Err(); err != nil {
		return testresults.Output{}, e.Wrap(e.CodeTimeout, err, "conformance canceled before configuration admission")
	}
	for _, value := range []string{reference, policyPath, configPath, runtimeOptions.WorkspaceRoot} {
		if strings.TrimSpace(value) == "" {
			return testresults.Output{}, e.New(e.CodeUsage, "conformance requires a publication reference, policy, configuration and workspace")
		}
	}
	if strings.Contains(reference, "://") {
		return testresults.Output{}, e.New(e.CodeUnverifiedRef, "conformance requires an OCI publication reference, not a URL")
	}
	if err := delivery.ValidatePublicationReference(reference); err != nil {
		return testresults.Output{}, err
	}
	if err := heldfile.RequireExecutableSupport(); err != nil {
		return testresults.Output{}, capability(err)
	}
	options, toolsSource, err := loadConfiguration(ctx, configPath, runtimeOptions.WorkspaceRoot)
	if err != nil {
		return testresults.Output{}, err
	}
	// Refuse unavailable local capabilities and bad byte pins before provider or
	// registry work. Produce captures these same pins again for its held lifetime.
	preflight, err := admit(ctx, options)
	if err != nil {
		return testresults.Output{}, err
	}
	preflight.close()
	toolsReceipt, _, err := lifecycle.CheckQualificationReference(ctx, toolsSource.Reference, toolsSource.Policy, runtimeOptions)
	if err != nil {
		if toolsReceipt != nil {
			_ = toolsReceipt.Close()
		}
		return testresults.Output{}, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, toolsReceipt.Close())
		if resultErr != nil && output.SchemaVersion != "" {
			output.CaptureStatus = testresults.CapturePartial
		}
	}()
	options.ToolArtifacts = toolsReceipt
	receipt, _, err := lifecycle.CheckQualificationReference(ctx, reference, policyPath, runtimeOptions)
	if err != nil {
		if receipt != nil {
			_ = receipt.Close()
		}
		return testresults.Output{}, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, receipt.Close())
		if resultErr != nil && output.SchemaVersion != "" {
			output.CaptureStatus = testresults.CapturePartial
		}
	}()
	options.Artifacts = receipt
	producer, err := New(options)
	if err != nil {
		return testresults.Output{}, err
	}
	return producer.Produce(ctx)
}

func loadConfiguration(ctx context.Context, path, workspace string) (Options, PublishedSource, error) {
	const limit = 1 << 20
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "conformance configuration", MaxBytes: limit})
	if err != nil {
		return Options{}, PublishedSource{}, captureFailure(ctx, err, e.CodeUsage, "opening conformance configuration")
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, limit)
	if err != nil {
		return Options{}, PublishedSource{}, captureFailure(ctx, err, e.CodeUsage, "reading conformance configuration")
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return Options{}, PublishedSource{}, e.Wrap(e.CodeUsage, err, "conformance configuration requires one unambiguous JSON document")
	}
	var config Configuration
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Options{}, PublishedSource{}, e.Wrap(e.CodeUsage, err, "decoding conformance configuration")
	}
	options := config.Runner
	for _, timeout := range []struct {
		value    string
		fallback time.Duration
		out      *time.Duration
	}{
		{config.Timeouts.Run, 2 * time.Hour, &options.RunTimeout},
		{config.Timeouts.Command, time.Minute, &options.CommandTimeout},
		{config.Timeouts.Cleanup, 5 * time.Minute, &options.CleanupTimeout},
		{config.Timeouts.Startup, 5 * time.Minute, &options.StartupTimeout},
	} {
		*timeout.out = timeout.fallback
		if timeout.value != "" {
			value, err := time.ParseDuration(timeout.value)
			if err != nil {
				return Options{}, PublishedSource{}, e.Wrap(e.CodeUsage, err, "conformance timeout requires a duration")
			}
			*timeout.out = value
		}
	}
	configPath, err := filepath.Abs(path)
	if err != nil {
		return Options{}, PublishedSource{}, e.Wrap(e.CodeUsage, err, "resolving conformance configuration path")
	}
	if options.Kubeconfig.Path != "" && !filepath.IsAbs(options.Kubeconfig.Path) {
		options.Kubeconfig.Path = filepath.Join(filepath.Dir(configPath), options.Kubeconfig.Path)
	}
	options.WorkspaceRoot, err = filepath.Abs(workspace)
	if err != nil {
		return Options{}, PublishedSource{}, e.Wrap(e.CodeUsage, err, "resolving conformance workspace")
	}
	if strings.Contains(config.ToolsSource.Reference, "://") || delivery.ValidatePublicationReference(config.ToolsSource.Reference) != nil || !safetext.Valid(config.ToolsSource.Policy, 4096, false) || strings.TrimSpace(config.ToolsSource.Policy) != config.ToolsSource.Policy {
		return Options{}, PublishedSource{}, e.New(e.CodeUsage, "conformance requires an independent published tools source and policy")
	}
	if !filepath.IsAbs(config.ToolsSource.Policy) {
		config.ToolsSource.Policy = filepath.Join(filepath.Dir(configPath), config.ToolsSource.Policy)
	}
	options, err = validateOptions(options)
	return options, config.ToolsSource, err
}
