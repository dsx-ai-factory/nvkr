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
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/safetext"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
)

// LocalConfiguration selects a completed local tools Run and one disposable
// cluster. Run workspaces are relative to the repository working directory;
// private kubeconfig paths are relative to this configuration file.
type LocalConfiguration struct {
	ToolsWorkspace string        `json:"toolsWorkspace"`
	Runner         Options       `json:"runner"`
	Bindings       LocalBindings `json:"bindings"`
	Timeouts       struct {
		Run     string `json:"run,omitempty"`
		Command string `json:"command,omitempty"`
		Cleanup string `json:"cleanup,omitempty"`
		Startup string `json:"startup,omitempty"`
	} `json:"timeouts"`
}

// RunLocalFiles checks and holds both complete local Runs, then executes the
// same upstream producer used by published validation. No publication, signer
// policy or qualification authority is synthesized from these local outputs.
func RunLocalFiles(ctx context.Context, productWorkspace, configPath string, runtimeOptions lifecycle.QualificationRuntimeOptions) (output testresults.Output, resultErr error) {
	defer func() {
		if resultErr != nil && output.SchemaVersion != "" {
			output.CaptureStatus = testresults.CapturePartial
		}
	}()
	if ctx == nil {
		return output, e.New(e.CodeUsage, "local conformance requires context")
	}
	if err := ctx.Err(); err != nil {
		return output, e.Wrap(e.CodeTimeout, err, "local conformance canceled before admission")
	}
	if !localPathText(productWorkspace) || !localPathText(configPath) || !localPathText(runtimeOptions.WorkspaceRoot) {
		return output, e.New(e.CodeUsage, "local conformance requires explicit product, configuration and report workspaces")
	}
	if err := heldfile.RequireExecutableSupport(); err != nil {
		return output, capability(err)
	}
	config, options, err := loadLocalConfiguration(ctx, configPath, runtimeOptions.WorkspaceRoot)
	if err != nil {
		return output, err
	}
	preflight, err := admit(ctx, options)
	if err != nil {
		return output, err
	}
	preflight.close()
	sources, err := captureLocalRunSources(ctx, productWorkspace, config.ToolsWorkspace, lifecycle.CaptureLocalDiagnosticArtifacts)
	if err != nil {
		return output, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, sources.closeWith((*lifecycle.LocalDiagnosticArtifacts).Close))
	}()
	producer, err := NewLocal(sources.product, sources.tools, options, config.Bindings)
	if err != nil {
		return output, err
	}
	return producer.Produce(ctx)
}

type localRunSources struct {
	product, tools *lifecycle.LocalDiagnosticArtifacts
}

type localRunCapture func(context.Context, string) (*lifecycle.LocalDiagnosticArtifacts, error)

// The private seam tests capture/cleanup ownership without substituting an
// execution capability. Only concrete lifecycle owners reach NewLocal.
func captureLocalRunSources(ctx context.Context, productWorkspace, toolsWorkspace string, capture localRunCapture) (localRunSources, error) {
	var sources localRunSources
	tools, err := capture(ctx, toolsWorkspace)
	if err != nil {
		return sources, err
	}
	if filepath.Clean(productWorkspace) == filepath.Clean(toolsWorkspace) {
		return localRunSources{product: tools, tools: tools}, nil
	}
	product, err := capture(ctx, productWorkspace)
	if err != nil {
		return sources, errors.Join(err, tools.Close())
	}
	return localRunSources{product: product, tools: tools}, nil
}

func (sources localRunSources) closeWith(closeOwner func(*lifecycle.LocalDiagnosticArtifacts) error) error {
	if sources.product == sources.tools {
		return closeOwner(sources.product)
	}
	return errors.Join(closeOwner(sources.product), closeOwner(sources.tools))
}

func loadLocalConfiguration(ctx context.Context, path, workspace string) (LocalConfiguration, Options, error) {
	var config LocalConfiguration
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "local conformance configuration", MaxBytes: 1 << 20})
	if err != nil {
		return config, Options{}, captureFailure(ctx, err, e.CodeUsage, "opening local conformance configuration")
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, 1<<20)
	if err != nil {
		return config, Options{}, err
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return config, Options{}, e.Wrap(e.CodeUsage, err, "local conformance requires unambiguous JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return config, Options{}, e.Wrap(e.CodeUsage, err, "decoding local conformance configuration")
	}
	options := config.Runner
	for _, timeout := range []struct {
		value    string
		fallback time.Duration
		out      *time.Duration
	}{
		{config.Timeouts.Run, 4 * time.Hour, &options.RunTimeout},
		{config.Timeouts.Command, time.Minute, &options.CommandTimeout},
		{config.Timeouts.Cleanup, 5 * time.Minute, &options.CleanupTimeout},
		{config.Timeouts.Startup, 5 * time.Minute, &options.StartupTimeout},
	} {
		*timeout.out = timeout.fallback
		if timeout.value != "" {
			value, err := time.ParseDuration(timeout.value)
			if err != nil {
				return config, Options{}, e.Wrap(e.CodeUsage, err, "local conformance timeout requires a duration")
			}
			*timeout.out = value
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return config, Options{}, e.Wrap(e.CodeUsage, err, "local conformance configuration path")
	}
	if !filepath.IsAbs(options.Kubeconfig.Path) {
		options.Kubeconfig.Path = filepath.Join(filepath.Dir(abs), options.Kubeconfig.Path)
	}
	options.WorkspaceRoot, err = filepath.Abs(workspace)
	if err != nil || !localPathText(workspace) || !localPathText(config.ToolsWorkspace) {
		return config, Options{}, e.New(e.CodeUsage, "local conformance requires explicit report and tools Run workspaces")
	}
	options, err = validateOptions(options)
	return config, options, err
}

func localPathText(value string) bool {
	return safetext.Valid(value, 4096, false) && strings.TrimSpace(value) == value
}
