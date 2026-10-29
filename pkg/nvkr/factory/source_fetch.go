// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/sourcegit"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// executeGitHubSourceFetch delegates exactly one admitted Plan-owned source fetch. A fetch job
// receives neither a live producer session nor build, signing, or publication
// authority. Its matching SourceAccess credential is admitted only for Git's
// existing repository-scoped helper; the downstream job receives source bytes and
// a receipt which its producer must independently rehash before consumption.
// Hosted run-node fetch dispatch uses this admission. Ordinary ExecuteNode
// and RunLocal retain their existing developer-fetch behavior; they do not acquire
// this route's additional environment and held-directory isolation guarantees.
// Live fetching requires Linux's inherited-directory path. Dependency vendoring
// uses the source owner's pinned runtime and private-volume transport, not a host bind.
func executeGitHubSourceFetch(ctx context.Context, plan *Plan, nodeID string, stdout, stderr io.Writer) error {
	if err := admitGitHubPlan(ctx, plan); err != nil {
		return err
	}
	return executeSourceFetch(ctx, plan, nodeID, os.Environ(), stdout, stderr, (pipeline.Local{}).ExecNode)
}

func executeSourceFetch(ctx context.Context, plan *Plan, nodeID string, environment []string, stdout, stderr io.Writer,
	run func(ir.Node, pipeline.RunOptions) (*pipeline.ArtifactResult, error)) (returnErr error) {
	if ctx == nil {
		return e.New(e.CodeUsage, "source fetch requires a context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "source fetch canceled")
	}
	if plan == nil || run == nil {
		return e.New(e.CodeUsage, "source fetch requires a complete Plan and an executor")
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	if err := rejectUnsupportedRuntimeStorage(plan, "source fetching"); err != nil {
		return err
	}
	nodes, artifacts := planIndex(plan)
	node, ok := nodes[nodeID]
	if !ok {
		return e.New(e.CodeInvalidCatalog, "source fetch Plan has no node %q", nodeID)
	}
	// Complete Plan validation already rejects copied producer requirements and
	// missing references; retain these checks as defensive source-only admission.
	if node.Kind != NodeFetch || node.Requirements != nil || !irProjectedNode(node) {
		return e.New(e.CodeEngineCapability, "source fetch cannot execute operation %q", nodeID)
	}
	_, instruction, err := plan.producerInstruction(node)
	if err != nil {
		return err
	}
	// producerInstruction authenticates the scheduler/instruction kind pairing.
	// This guard is defensive, not an independently reachable valid-Plan branch.
	if instruction.Kind != ir.KindFetch {
		return e.New(e.CodePipelineDrift, "source fetch %q does not bind a fetch instruction", nodeID)
	}
	bounded, cancel, err := githubUtilityContext(ctx, plan, nodeID)
	if err != nil {
		return err
	}
	defer cancel()
	ctx = bounded
	childEnvironment, err := admitSourceFetchEnvironment(plan, node, environment)
	if err != nil {
		return err
	}
	workspace, err := pipeline.HoldSourceWorkspace(ctx)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, workspace.Close()) }()
	for _, output := range node.Outputs {
		if err := admitSourceFetchPath(ctx, artifacts[output].Path, artifacts[output].Kind == "source-closure"); err != nil {
			return err
		}
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	return executeProducer(ctx, plan, node, plan.Environment.RuntimeBinary(), 1, stdout, stderr,
		func(instruction ir.Node, options pipeline.RunOptions) (*pipeline.ArtifactResult, error) {
			options.SourceWorkspace = workspace
			options.SourceGitEnvironment = childEnvironment
			return run(instruction, options)
		}, nil)
}

// Existing path components must be real directories; a fresh missing suffix is
// created by the existing fetch owner, never by this admission check.
func admitSourceFetchPath(ctx context.Context, path string, directory bool) error {
	root, _, err := securepath.OpenDirectory(".")
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "opening source fetch root")
	}
	defer func() { _ = root.Close() }()
	parts := strings.Split(filepath.ToSlash(path), "/")
	for index, part := range parts {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "source fetch path admission canceled")
		}
		child, _, err := securepath.OpenChild(root, part, directory || index < len(parts)-1)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return e.Wrap(e.CodeFetchFailed, err, "source fetch path contains an unsafe component")
		}
		_ = root.Close()
		root = child
	}
	return nil
}

func admitSourceFetchEnvironment(plan *Plan, node Node, environment []string) ([]string, error) {
	expected := githubSourceEnvironment(plan, node)
	// Even public sources must not consult a runner's credential helper or global
	// Git configuration. Only the exact selected private helper overrides this
	// baseline. The pipeline supplies its own empty child HOME; host HOME, SSH,
	// proxy, registry and CI variables are not forwarded.
	path, err := sourcegit.SearchPath(environment)
	if err != nil {
		return nil, err
	}
	baseline, err := sourcegit.Baseline(path)
	if err != nil {
		return nil, err
	}
	child := make(map[string]string, len(baseline)+len(expected))
	for _, pair := range baseline {
		key, value, _ := strings.Cut(pair, "=")
		child[key] = value
	}
	for key, value := range expected {
		child[key] = value
	}
	seen := make(map[string]string)
	for _, pair := range environment {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		credential := key == "NVKR_CREDENTIAL" || key == "NVKR_REGISTRY_USER" || key == "GITHUB_TOKEN" || key == "GH_TOKEN" ||
			key == "ACTIONS_RUNTIME_TOKEN" || key == "DOCKER_CONFIG" || key == "REGISTRY_AUTH_FILE" ||
			strings.HasPrefix(key, "NVKR_PIN_SOURCE_TOKEN_") || strings.HasPrefix(key, "ACTIONS_ID_TOKEN_")
		if credential {
			return nil, e.New(e.CodeEngineCapability, "source fetch received an unrelated credential scope %q", key)
		}
		switch strings.ToUpper(key) {
		case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY":
			if value != "" {
				return nil, e.New(e.CodeEngineCapability, "source fetch does not support ambient proxy configuration %q", key)
			}
		}
		if !strings.HasPrefix(key, "GIT_") && key != sourceTokenEnv {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, e.New(e.CodeEngineCapability, "source fetch received duplicate environment field %q", key)
		}
		seen[key] = value
		want, allowed := child[key]
		if !allowed || key != sourceTokenEnv && value != want {
			return nil, e.New(e.CodeEngineCapability, "source fetch received an unselected Git credential or configuration field %q", key)
		}
		child[key] = value
	}
	if len(expected) != 0 && seen[sourceTokenEnv] == "" {
		return nil, e.New(e.CodeFetchFailed, "source fetch credential is empty")
	}
	for _, key := range sortedKeys(expected) {
		value := expected[key]
		if key == sourceTokenEnv {
			continue
		}
		observed, present := seen[key]
		if !present || observed != value {
			return nil, e.New(e.CodeEngineCapability, "source fetch is missing its exact repository-scoped Git configuration %q", key)
		}
	}
	result := make([]string, 0, len(child))
	for _, key := range sortedKeys(child) {
		result = append(result, key+"="+child[key])
	}
	_, instruction, err := plan.producerInstruction(node)
	if err != nil {
		return nil, err
	}
	_, result, err = sourcegit.Admit(instruction.Repo, result)
	return result, err
}
