package factory

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

// ExecuteSourceFetch delegates exactly one Plan-owned source fetch. A fetch job
// receives neither a live producer session nor build, signing, or publication
// authority. Its matching SourceAccess credential is admitted only for Git's
// existing repository-scoped helper; the downstream job receives source bytes and
// a receipt which its producer must independently rehash before consumption.
// This does not change ExecuteNode or RunLocal's capability restrictions.
// Live fetching requires Linux's inherited-directory path and excludes dependency
// vendoring until that runtime's bind mount can authenticate the held checkout.
func ExecuteSourceFetch(ctx context.Context, plan *Plan, nodeID string, stdout, stderr io.Writer) error {
	return executeSourceFetch(ctx, plan, nodeID, os.Environ(), stdout, stderr, (pipeline.Local{}).ExecNode)
}

func executeSourceFetch(ctx context.Context, plan *Plan, nodeID string, environment []string, stdout, stderr io.Writer,
	run func(ir.Node, pipeline.RunOptions) (*pipeline.ArtifactResult, error)) error {
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
	if !ok || node.Kind != NodeFetch || node.Requirements != nil || !irProjectedNode(node) {
		return e.New(e.CodeEngineCapability, "source fetch cannot execute operation %q", nodeID)
	}
	_, instruction, err := plan.producerInstruction(node)
	if err != nil {
		return err
	}
	if instruction.Kind != ir.KindFetch {
		return e.New(e.CodePipelineDrift, "source fetch %q does not bind a fetch instruction", nodeID)
	}
	if len(instruction.VendorCmd) != 0 {
		return e.New(e.CodeEngineCapability, "source fetching cannot authenticate a held checkout mount for dependency vendoring")
	}
	if err := admitSourceFetchEnvironment(plan, node, environment); err != nil {
		return err
	}
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
	workspace, _, err := securepath.OpenDirectory(".")
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "holding source fetch workspace")
	}
	defer func() { _ = workspace.Close() }()
	return executeProducer(ctx, plan, node, plan.Environment.RuntimeBinary(), 1, stdout, stderr,
		func(instruction ir.Node, options pipeline.RunOptions) (*pipeline.ArtifactResult, error) {
			options.SourceWorkspace = workspace
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

func admitSourceFetchEnvironment(plan *Plan, node Node, environment []string) error {
	expected := githubSourceEnvironment(plan, node)
	seen := make(map[string]string)
	for _, pair := range environment {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		credential := key == "NKD_CREDENTIAL" || key == "NKD_REGISTRY_USER" || key == "GITHUB_TOKEN" || key == "GH_TOKEN" ||
			strings.HasPrefix(key, "NKD_PIN_SOURCE_TOKEN_") || strings.HasPrefix(key, "ACTIONS_ID_TOKEN_")
		if credential && value != "" {
			return e.New(e.CodeEngineCapability, "source fetch received an unrelated credential scope %q", key)
		}
		if !strings.HasPrefix(key, "GIT_") && key != sourceTokenEnv {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			return e.New(e.CodeEngineCapability, "source fetch received duplicate environment field %q", key)
		}
		seen[key] = value
		want, allowed := expected[key]
		if !allowed || key != sourceTokenEnv && value != want {
			return e.New(e.CodeEngineCapability, "source fetch received an unselected Git credential or configuration field %q", key)
		}
	}
	if len(expected) != 0 && seen[sourceTokenEnv] == "" {
		return e.New(e.CodeFetchFailed, "source fetch credential is empty")
	}
	for _, key := range sortedKeys(expected) {
		value := expected[key]
		if key == sourceTokenEnv {
			if seen[key] == "" {
				return e.New(e.CodeFetchFailed, "source fetch credential is empty")
			}
			continue
		}
		if seen[key] != value {
			return e.New(e.CodeEngineCapability, "source fetch is missing its exact repository-scoped Git configuration %q", key)
		}
	}
	return nil
}
