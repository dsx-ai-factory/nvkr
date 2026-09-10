package pipeline

import (
	"context"
	"os"
	"path/filepath"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/ir"
)

func admitBuildKit(node ir.Node, opts RunOptions) error {
	if node.BuilderType != ir.BuilderBuildKit {
		return nil
	}
	if opts.Qualification != nil || opts.execution != nil || opts.Package {
		return e.New(e.CodeEngineCapability, "rootless BuildKit produces wrapped OCI exports, not qualified or legacy-packaged builds")
	}
	if filepath.Base(opts.Runtime) != "docker" {
		return e.New(e.CodeEngineCapability, "rootless BuildKit currently requires the Docker runtime adapter")
	}
	return nil
}

// BuildKit runs as UID 1000, never as host/container root, with its documented
// rootless namespace permissions. It gets neither a host runtime socket nor
// credentials. The native snapshotter avoids a host /dev/fuse dependency.
func buildKitRuntimeArgs() []string {
	return []string{"--user", "1000:1000", "--security-opt", "seccomp=unconfined",
		"--security-opt", "apparmor=unconfined", "--security-opt", "systempaths=unconfined",
		"--entrypoint", "", "-e", "BUILDKITD_FLAGS=--oci-worker-snapshotter=native"}
}

// A private parent prevents other host users from reaching the writable mount.
// Only its child is writable by the fixed container UID. After the container has
// exited, publish the child at the normal collector path and restore host-only
// directory writes. The ordinary collector still hashes the exact output file.
func prepareBuildKitOutput(ctx context.Context, out string) (string, func() error, func(), error) {
	if err := requireRunContext(ctx, "rootless BuildKit output"); err != nil {
		return "", nil, nil, err
	}
	if err := ctx.Err(); err != nil {
		return "", nil, nil, e.Wrap(e.CodeTimeout, err, "rootless BuildKit output canceled")
	}
	parent, err := os.MkdirTemp(filepath.Dir(out), ".nkd-buildkit-*")
	if err != nil {
		return "", nil, nil, e.Wrap(e.CodeBuildFailed, err, "creating private rootless BuildKit output parent")
	}
	cleanup := func() { _ = os.RemoveAll(parent) }
	mount := filepath.Join(parent, "output")
	if err := os.Mkdir(mount, 0o700); err != nil {
		cleanup()
		return "", nil, nil, e.Wrap(e.CodeBuildFailed, err, "creating rootless BuildKit output")
	}
	if err := os.Chmod(mount, 0o777); err != nil {
		cleanup()
		return "", nil, nil, e.Wrap(e.CodeBuildFailed, err, "setting container output access")
	}
	promote := func() error {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "rootless BuildKit output canceled")
		}
		if err := os.Chmod(mount, 0o755); err != nil {
			return e.Wrap(e.CodeBuildFailed, err, "closing container output writes")
		}
		if err := os.Remove(out); err != nil {
			return e.Wrap(e.CodeBuildFailed, err, "replacing empty output collector directory")
		}
		if err := os.Rename(mount, out); err != nil {
			return e.Wrap(e.CodeBuildFailed, err, "publishing rootless BuildKit output")
		}
		return nil
	}
	return mount, promote, cleanup, nil
}
