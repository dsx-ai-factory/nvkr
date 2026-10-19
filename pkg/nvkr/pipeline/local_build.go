// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/assemble"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/profile"
)

// build runs one from-source build node in its pinned toolchain container, then
// records the artifact digest. When the node is isolated (SourceReadOnly + WriteDir),
// the shared source is mounted read-only and a fresh per-build output dir is mounted
// over WriteDir, so any number of build nodes can run concurrently without clobbering
// each other or mutating the shared source.
func (l Local) build(n ir.Node, opts RunOptions) (result *ArtifactResult, returnErr error) {
	if err := admitBuildKit(n, opts); err != nil {
		return nil, err
	}
	if err := admitBuildMaterials(n, opts); err != nil {
		return nil, err
	}
	var executionIdentity profile.VerifiedNodeIdentity
	if opts.execution != nil {
		if err := opts.execution.receipt.RequireFor(opts.ctx(), opts.execution.profile, opts.execution.nodes); err != nil {
			return nil, err
		}
		var ok bool
		executionIdentity, ok = observedNodeIdentity(opts.execution, n.ID)
		if !ok {
			return nil, e.New(e.CodePreflightFailed,
				"build node %q is absent from the qualified execution receipt", n.ID)
		}
	}
	// Nix builder dispatch (ADR-10 §6/§7): a nix component is built from a LOCKED flake
	// inside the pinned nix container, offline. Hermeticity is enforced by the same
	// --network=none the oci-hermetic lane uses (Network==none in the IR), so the locked
	// closure must be pre-realized (no external Nix cache reachable at build time). Defense
	// in depth: fail closed if a nix node reached the runner without its closure pin or the
	// offline guarantee — never silently fall back to an impure build.
	if n.BuilderType == ir.BuilderNix {
		if n.FlakeLockDigest == "" {
			return nil, e.New(e.CodeUnpinnedSource, "nix build %q reached the runner with no locked-closure digest", n.Component)
		}
		if n.Network != ir.NetworkNone {
			return nil, e.New(e.CodeBuildFailed, "nix build %q must run offline (--network=none) so no substituter is reachable; got network=%q", n.Component, n.Network)
		}
	}
	isolated := n.SourceReadOnly && n.WriteDir != ""
	if opts.DryRun {
		fmt.Fprintf(opts.Stdout, "[build:%s] materialize authenticated source and %d material(s); apply %d patch(es); run %s with --network=%s\n",
			n.Component, len(n.Materials), len(n.Patches), n.Toolchain, n.Network)
		return nil, nil
	}
	snapshot, err := materializeSource(n, opts)
	if err != nil {
		return nil, err
	}
	defer snapshot.cleanup()
	// Docker bind-mount sources MUST be absolute: a relative `-v .nvkr/...:/src` is parsed as a
	// (invalid) named volume, not a host path. opts.SourceRoot is relative in CI (the emitted
	// --source-root is repo-relative), so resolve both mount roots against cwd. (l.vendor does
	// the same for its source mount.)
	absSrc, absErr := filepath.Abs(snapshot.Dir)
	if absErr != nil {
		return nil, e.Wrap(e.CodeBuildFailed, absErr, "resolving absolute source dir for %s", n.Component)
	}
	outRoot := privOutDir(opts, n)
	if abs, err := filepath.Abs(outRoot); err == nil {
		outRoot = abs
	}
	if err := os.RemoveAll(outRoot); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "resetting output dir for %s", n.Component)
	}
	if err := os.MkdirAll(outRoot, 0o755); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "creating output dir for %s", n.Component)
	}
	var stagedMaterials *stagedBuildMaterials
	if len(n.Materials) != 0 {
		stagedMaterials, err = opts.BuildMaterials.stage(opts.ctx(), n, filepath.Dir(outRoot))
		if err != nil {
			return nil, err
		}
		defer func() {
			if cleanupErr := stagedMaterials.cleanup(); cleanupErr != nil {
				result = nil
				returnErr = errors.Join(returnErr,
					e.Wrap(e.CodeInternal, cleanupErr, "removing private build-material root"))
			}
		}()
	}
	var buildKit *buildKitTransport
	if n.BuilderType == ir.BuilderBuildKit {
		buildKit, err = prepareBuildKitTransport(opts.ctx(), n, opts, absSrc, buildKitCommandRuntime{opts: opts})
		if err != nil {
			return nil, err
		}
		defer func() {
			if cleanupErr := buildKit.close(); cleanupErr != nil {
				result = nil
				returnErr = errors.Join(returnErr, e.Wrap(e.CodeBuildFailed, cleanupErr, "cleaning BuildKit transport"))
			}
		}()
	}

	cname := containerName(n)
	args := []string{"run", "--rm", "--name", cname}
	if buildKit != nil {
		args = []string{"create", "--name", cname}
		buildKit.resources.containers = append(buildKit.resources.containers, cname)
	}
	// Run the toolchain on the build's target platform (model B): forces the native
	// arch variant of a multi-arch toolchain — or emulation on a foreign host — so
	// GOARCH compilation is native, never a cross-compile. Derived from the IR's arch.
	if n.Platform != "" {
		args = append(args, "--platform", n.Platform)
	}
	if n.Network != "" {
		args = append(args, "--network="+n.Network)
	}
	// Only bound per-build CPU when builds actually run concurrently. `go build`
	// already parallelizes across all cores, so with jobs>1 we share the host cores
	// across the budget to avoid oversubscription; with jobs==1 the single build
	// must use ALL cores (measured optimal on one machine — capping it there was a
	// 7x regression). This is host-dependent execution tuning, deliberately NOT part
	// of the IR: GOMAXPROCS does not affect output bytes, so reproducibility holds.
	if opts.Jobs > 1 {
		procs := runtime.NumCPU() / opts.Jobs
		if procs < 1 {
			procs = 1
		}
		args = append(args, "-e", "GOMAXPROCS="+strconv.Itoa(procs))
	}
	for _, ev := range n.Env {
		args = append(args, "-e", ev.Name+"="+ev.Value)
	}
	if n.BuilderType == ir.BuilderBuildKit {
		args = append(args, buildKitRuntimeArgs(opts.Runtime)...)
	}
	if buildKit != nil {
		args = append(args, buildKitVolumeArgs(filepath.Base(opts.Runtime), buildKit.source, "/src", true)...)
		args = append(args, buildKitVolumeArgs(filepath.Base(opts.Runtime), buildKit.output, path.Join(n.Workdir, n.WriteDir), false)...)
	} else if isolated {
		mountpoint, err := pathWithin(absSrc, n.WriteDir)
		if err != nil {
			return nil, e.Wrap(e.CodeBuildFailed, err, "resolving output mountpoint for %s", n.Component)
		}
		if err := os.MkdirAll(mountpoint, 0o755); err != nil {
			return nil, e.Wrap(e.CodeBuildFailed, err, "preparing source mountpoint for %s", n.Component)
		}
		args = append(args, "-v", absSrc+":/src:ro", "-v", outRoot+":"+path.Join(n.Workdir, n.WriteDir))
	} else {
		args = append(args, "-v", absSrc+":/src")
	}
	if stagedMaterials != nil {
		args = append(args, buildMaterialMountArgs(stagedMaterials.root)...)
	}
	args = append(args, "-w", n.Workdir, n.Toolchain)
	args = append(args, n.Command...)

	fmt.Fprintf(opts.Stdout, "\n=== build %s (%s, --network=%s, isolated=%t) ===\n", n.Component, n.Toolchain, n.Network, isolated)
	opts.progress.printf("[container] build %s — launching %s with --network=%s\n", n.Component, n.Toolchain, n.Network)
	if err := registerContainer(opts, cname); err != nil {
		return nil, err
	}
	if err := runContainerRuntime(opts, args...); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "building %s", n.Component)
	}
	if buildKit != nil {
		if err := runContainerRuntime(opts, "start", "--attach", cname); err != nil {
			return nil, e.Wrap(e.CodeBuildFailed, err, "building %s", n.Component)
		}
		if err := buildKit.collect(opts.ctx(), opts, n, cname); err != nil {
			return nil, err
		}
	}
	opts.reap.forget(cname)

	binPath := artifactHostPath(opts, n)
	artifactRel := artifactOutputRelative(n)
	if !isolated {
		if err := copyCollectedArtifactContext(opts.ctx(), absSrc, n.ArtifactPath, binPath); err != nil {
			return nil, e.Wrap(e.CodeBuildFailed, err, "persisting artifact for %s", n.Component)
		}
	}
	sum, size, _, err := hashRegularBeneathContext(opts.ctx(), outRoot, artifactRel)
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "collecting artifact for %s", n.Component)
	}
	res := &ArtifactResult{
		Component:     n.Component,
		OutputType:    n.OutputType,
		Qualification: ArtifactUnqualified,
		Toolchain:     n.Toolchain,
		InputDigest:   snapshot.InputDigest,
		MaterialManifestDigest: func() string {
			if stagedMaterials == nil {
				return ""
			}
			return stagedMaterials.manifestDigest
		}(),
		BuilderType: n.BuilderType,
		// Local selected and executed this exact in-process dispatch lane. Recording
		// that observation is useful even on an unqualified development report;
		// ProfileDigest/ExecutionBinding below remain the authority boundary.
		EngineClass: n.BuilderType,
		Path:        n.ArtifactPath,
		SHA256:      sum,
		Size:        size,
		AbsPath:     binPath,
	}
	if opts.execution != nil {
		res.Qualification = ArtifactQualifiedExecution
		res.EngineClass = executionIdentity.EngineClass
		res.ProfileDigest = opts.execution.receipt.ProfileDigest()
		res.ExecutionBinding = opts.execution.receipt.ExecutionBinding()
	}
	// When packaging, assemble the compiled binary into its delivery artifact and
	// record THAT digest — a real oci-image/.deb digest, not a raw-binary hash.
	if opts.Package {
		if err := packageArtifact(n, binPath, opts, res); err != nil {
			return nil, err
		}
	}
	return res, nil
}

func buildMaterialMountArgs(root string) []string {
	return []string{"-v", root + ":" + buildInputsContainerRoot + ":ro"}
}

func packageArtifact(n ir.Node, binPath string, opts RunOptions, res *ArtifactResult) error {
	epoch := envEpoch(n.Env)
	if err := os.MkdirAll(opts.PackageDir, 0o755); err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "package dir")
	}
	arch := debArch(n.Platform)
	switch n.OutputType {
	case "oci-image":
		dir := filepath.Join(opts.PackageDir, n.Component+".oci")
		//nolint:staticcheck // Qualified Package is rejected before graph/path I/O; this is the explicit unqualified development lane.
		dig, err := assemble.OCIImage(binPath, n.Component, epoch, arch, dir)
		if err != nil {
			return e.Wrap(e.CodeBuildFailed, err, "oci image for %s", n.Component)
		}
		res.SHA256 = dig
		res.Path = dir
		// the manifest blob's file sha256 equals the manifest (image) digest.
		res.AbsPath = filepath.Join(dir, "blobs", "sha256", strings.TrimPrefix(dig, "sha256:"))
		res.Packaged = true
	case "host-package":
		// Debian arch name matches GOARCH here (amd64, arm64); take it from the node's
		// target platform so a multi-arch build names its .deb correctly.
		out := filepath.Join(opts.PackageDir, fmt.Sprintf("%s_%s_%s.deb", n.Component, orVersion(n.Version), arch))
		//nolint:staticcheck // Qualified Package is rejected before graph/path I/O; this is the explicit unqualified development lane.
		dig, err := assemble.Deb(binPath, n.Component, orVersion(n.Version), epoch, arch, out)
		if err != nil {
			return e.Wrap(e.CodeBuildFailed, err, "deb for %s", n.Component)
		}
		res.SHA256 = dig
		res.Path = out
		res.AbsPath = out
		res.Packaged = true
	default:
		return e.New(e.CodeUnsupported, "component %q: no packager for outputType %q", n.Component, n.OutputType)
	}
	// Record the packaged artifact's own size, not the raw binary's.
	if fi, err := os.Stat(res.AbsPath); err == nil {
		res.Size = fi.Size()
	}
	return nil
}

func envEpoch(env []ir.EnvVar) int64 {
	for _, ev := range env {
		if ev.Name == "SOURCE_DATE_EPOCH" {
			if v, err := strconv.ParseInt(ev.Value, 10, 64); err == nil {
				return v
			}
		}
	}
	return 0
}

func orVersion(v string) string {
	if v == "" {
		return "0.0.0"
	}
	return v
}

// debArch maps a build node's platform (linux/<arch>) to the Debian arch used in a
// .deb filename. GOARCH and Debian's arch names coincide for the supported set
// (amd64, arm64); an unset platform uses the default amd64 architecture.
func debArch(platform string) string {
	if i := strings.LastIndex(platform, "/"); i >= 0 {
		if a := platform[i+1:]; a != "" {
			return a
		}
	}
	return "amd64"
}

// privOutDir is the per-build writable output directory on the host, shared by
// build (mount + collect) and verify (re-hash) so both resolve the same file. It is
// absolute because `docker -v` requires an absolute host path.
func privOutDir(opts RunOptions, n ir.Node) string {
	abs, _ := filepath.Abs(filepath.Join(filepath.Dir(opts.SourceRoot), "out", sanitizeName(n.Component)))
	return abs
}

// ArtifactPath is the exported resolver for a build node's produced binary on the
// host, so the packaging step can locate what a prior build compiled.
func ArtifactPath(opts RunOptions, n ir.Node) string { return artifactHostPath(opts, n) }

// artifactHostPath resolves the durable collected artifact path. Source snapshots
// are private and ephemeral, so every lane persists its output under privOutDir.
func artifactHostPath(opts RunOptions, n ir.Node) string {
	return filepath.Join(privOutDir(opts, n), artifactOutputRelative(n))
}

func artifactOutputRelative(n ir.Node) string {
	rel := filepath.Clean(n.ArtifactPath)
	if n.WriteDir != "" {
		if r, err := filepath.Rel(filepath.Clean(n.WriteDir), rel); err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			rel = r
		}
	}
	return rel
}
