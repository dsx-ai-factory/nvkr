// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"path/filepath"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func admitBuildKit(node ir.Node, opts RunOptions) error {
	if node.BuilderType != ir.BuilderBuildKit {
		return nil
	}
	if opts.Qualification != nil || opts.execution != nil || opts.Package {
		return e.New(e.CodeEngineCapability, "rootless BuildKit produces wrapped OCI exports, not qualified or legacy-packaged builds")
	}
	if !node.SourceReadOnly || node.WriteDir == "" || len(node.Materials) != 0 {
		return e.New(e.CodeEngineCapability, "rootless BuildKit requires isolated source/output and does not support material inputs")
	}
	switch filepath.Base(opts.Runtime) {
	case "docker", "podman":
	default:
		return e.New(e.CodeEngineCapability,
			"rootless BuildKit requires the Docker or Podman runtime adapter")
	}
	return nil
}

// BuildKit runs as UID 1000, never as host/container root, with its documented
// rootless namespace permissions. It gets neither a host runtime socket nor
// credentials. Overlayfs avoids copying entire source/toolchain trees for each
// Dockerfile step and requires a kernel supporting unprivileged overlay mounts.
// It needs no /dev/fuse; an unsupported host fails rather than falling back to
// the native snapshotter's unbounded full-tree copies. Rootless Podman on an
// SELinux host also needs label separation disabled for this container so the
// nested BuildKit worker can mount proc and devpts. This grants no host device,
// runtime socket, credential, or privileged-container access.
func buildKitRuntimeArgs(runtime string) []string {
	args := []string{"--user", "1000:1000", "--security-opt", "seccomp=unconfined",
		"--security-opt", "apparmor=unconfined", "--security-opt", "systempaths=unconfined",
	}
	if filepath.Base(runtime) == "podman" {
		args = append(args, "--security-opt", "label=disable")
	}
	return append(args, "--entrypoint", "", "-e", "BUILDKITD_FLAGS=--oci-worker-snapshotter=overlayfs")
}
