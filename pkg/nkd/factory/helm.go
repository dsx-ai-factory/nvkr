package factory

import (
	"context"
	"os"
	"path/filepath"

	"github.com/NVIDIA/nke-recipes/internal/transportarchive"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// helmPackageTree normalizes a generated tree or an observed source archive to
// the same deterministic carrier. Source archives have exactly one helm/ root;
// transport extraction rejects escaping links, traversal, duplicates and extra roots.
func helmPackageTree(ctx context.Context, plan *Plan, input Artifact) (string, string, func(), error) {
	noop := func() {}
	if input.Kind == "aicr-bundle" {
		root, relative, err := lifecycleTransportMember(plan, input)
		return root, relative, noop, err
	}
	if input.Kind != "tarball" || input.BuildTarget == "" {
		return "", "", noop, e.New(e.CodeInvalidCatalog, "Helm input %q is not a build source archive", input.ID)
	}
	stage, err := os.MkdirTemp("", "nkd-helm-")
	if err != nil {
		return "", "", noop, e.Wrap(e.CodeInternal, err, "staging Helm source archive")
	}
	cleanup := func() { _ = os.RemoveAll(stage) }
	if err := transportarchive.Unpack(ctx, input.Path, stage, []string{"helm"}); err != nil {
		cleanup()
		return "", "", noop, err
	}
	info, err := os.Lstat(filepath.Join(stage, "helm"))
	if err != nil || !info.IsDir() {
		cleanup()
		return "", "", noop, e.New(e.CodeVerifyFailed, "Helm source archive requires a helm directory")
	}
	return stage, "helm", cleanup, nil
}
