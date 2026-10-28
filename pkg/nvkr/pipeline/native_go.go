// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// NativeGoSpec records installed compiler inputs without making a hermetic or
// qualified claim. System libraries and the Apple SDK remain host dependencies.
// Paths are transport descriptions, deliberately outside this content identity.
type NativeGoSpec struct {
	SchemaVersion  string `json:"schemaVersion"`
	Platform       string `json:"platform"`
	SDKDigest      string `json:"sdkDigest"`
	CompilerDigest string `json:"compilerDigest"`
}

func (spec NativeGoSpec) Digest() (string, error) {
	if spec.SchemaVersion != canonical.KindNativeGoToolchain || spec.Platform != runtime.GOOS+"/"+runtime.GOARCH || !canonical.IsDigest(spec.SDKDigest) || !canonical.IsDigest(spec.CompilerDigest) {
		return "", e.New(e.CodeEngineCapability, "installed Go manifest requires the current host and exact SDK/compiler identities")
	}
	return canonical.Digest(canonical.KindNativeGoToolchain, spec)
}

// CaptureNativeGo measures the existing SDK and compiler. It does not install,
// download, execute, or copy a compiler into the repository.
func CaptureNativeGo(ctx context.Context, goRoot, compiler string) (NativeGoSpec, error) {
	if ctx == nil {
		return NativeGoSpec{}, e.New(e.CodeUsage, "installed Go capture requires a context")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return NativeGoSpec{}, e.New(e.CodeEngineCapability, "installed Go requires a Mac or Linux host")
	}
	for _, location := range []string{goRoot, compiler} {
		if !filepath.IsAbs(location) || filepath.Clean(location) != location {
			return NativeGoSpec{}, e.New(e.CodeUsage, "installed Go requires clean absolute SDK and compiler paths")
		}
	}
	class, err := platformPackerExecutionClass()
	if err != nil {
		return NativeGoSpec{}, err
	}
	var compilerDigest string
	for _, location := range []string{filepath.Join(goRoot, "bin", "go"), compiler} {
		// These are byte observations, not sealed-executable authority. Darwin
		// cannot execute an anonymous held file; the local lane uses installed paths.
		file, err := heldfile.OpenContext(ctx, location, heldfile.Options{Label: "installed-compiler", MaxBytes: 256 << 20})
		if err != nil {
			return NativeGoSpec{}, e.Wrap(e.CodeEngineCapability, err, "reading installed compiler")
		}
		formatErr := class.format(file, location)
		compilerDigest = file.Digest()
		closeErr := file.Close()
		if formatErr != nil || closeErr != nil {
			return NativeGoSpec{}, e.Wrap(e.CodeEngineCapability, errors.Join(formatErr, closeErr), "admitting installed compiler format")
		}
	}
	tree, err := snapshotRegularTreeContext(ctx, goRoot)
	if err != nil {
		return NativeGoSpec{}, e.Wrap(e.CodeEngineCapability, err, "measuring installed Go SDK")
	}
	return NativeGoSpec{SchemaVersion: canonical.KindNativeGoToolchain, Platform: runtime.GOOS + "/" + runtime.GOARCH, SDKDigest: tree.digest, CompilerDigest: compilerDigest}, nil
}

// NativeGoToolchain is an opaque process-local receipt. Native pathname execution
// is measured before/after each build; it is not protection against a hostile
// same-UID process or a substitute for the qualified sandbox.
type NativeGoToolchain struct {
	mu                       sync.Mutex
	spec                     NativeGoSpec
	digest, goRoot, compiler string
	closed                   bool
}

// OpenNativeGo authenticates a bounded canonical manifest, its semantic pin, and
// the actual installed bytes before a local attempt may reserve its workspace.
func OpenNativeGo(ctx context.Context, manifest, digest, goRoot, compiler string) (*NativeGoToolchain, error) {
	file, err := heldfile.OpenContext(ctx, manifest, heldfile.Options{Label: "installed Go manifest", MaxBytes: 4096})
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "reading installed Go manifest")
	}
	defer func() { _ = file.Close() }()
	raw, err := file.ReadAllContext(ctx, 4096)
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "reading installed Go manifest bytes")
	}
	var spec NativeGoSpec
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "decoding installed Go manifest")
	}
	encoded, err := canonical.Marshal(spec)
	if err != nil || !bytes.Equal(raw, encoded) {
		return nil, e.New(e.CodeInvalidCatalog, "installed Go manifest is not canonical")
	}
	actual, err := spec.Digest()
	if err != nil {
		return nil, err
	}
	if actual != digest {
		return nil, e.New(e.CodeUnverifiedRef, "installed Go manifest differs from selected toolchain identity")
	}
	observed, err := CaptureNativeGo(ctx, goRoot, compiler)
	if err != nil {
		return nil, err
	}
	if observed != spec {
		return nil, e.New(e.CodeUnverifiedRef, "installed Go SDK/compiler differ from selected bytes")
	}
	return &NativeGoToolchain{spec: spec, digest: digest, goRoot: goRoot, compiler: compiler}, nil
}

func (tool *NativeGoToolchain) Close() error {
	if tool == nil {
		return nil
	}
	tool.mu.Lock()
	defer tool.mu.Unlock()
	tool.closed = true
	return nil
}

// lockNativeGo admits the options and receipt together. The caller owns the
// returned lock through execution, or releases it after an early capability check.
func lockNativeGo(node ir.Node, options RunOptions) (*NativeGoToolchain, error) {
	if options.NativeGo == nil || options.Qualification != nil || options.execution != nil || !options.Launch || options.Package {
		return nil, e.New(e.CodeEngineCapability, "native Go execution requires a local unqualified toolchain receipt")
	}
	tool := options.NativeGo
	tool.mu.Lock()
	if tool.closed || tool.digest != node.Toolchain || tool.spec.Platform != node.Platform {
		tool.mu.Unlock()
		return nil, e.New(e.CodeUnverifiedRef, "native Go receipt is closed or belongs to another producer toolchain")
	}
	return tool, nil
}

func admitNativeGo(node ir.Node, options RunOptions) error {
	if node.BuilderType != ir.BuilderNativeGo {
		return nil
	}
	tool, err := lockNativeGo(node, options)
	if err != nil {
		return err
	}
	tool.mu.Unlock()
	return nil
}

func buildNativeGo(node ir.Node, options RunOptions) (*ArtifactResult, error) {
	tool, err := lockNativeGo(node, options)
	if err != nil {
		return nil, err
	}
	defer tool.mu.Unlock()
	ctx := options.ctx()
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 20*time.Minute)
		defer cancel()
	}
	options.Ctx = ctx
	check := func() error {
		observed, err := CaptureNativeGo(ctx, tool.goRoot, tool.compiler)
		if err != nil {
			return err
		}
		if observed != tool.spec {
			return e.New(e.CodeUnverifiedRef, "installed compiler inputs changed during the native build")
		}
		return nil
	}
	if err := check(); err != nil {
		return nil, err
	}
	if options.DryRun {
		return nil, nil
	}
	source, err := materializeSource(node, options)
	if err != nil {
		return nil, err
	}
	defer source.cleanup()
	parent, err := filepath.Abs(filepath.Dir(source.Dir))
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "resolving native Go private state")
	}
	state, err := os.MkdirTemp(parent, "native-go-")
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "creating native Go state")
	}
	defer os.RemoveAll(state)
	environment := map[string]string{
		"HOME": filepath.Join(state, "home"), "TMPDIR": filepath.Join(state, "tmp"),
		"GOCACHE": filepath.Join(state, "cache"), "GOPATH": filepath.Join(state, "path"), "GOMODCACHE": filepath.Join(state, "modules"),
	}
	for _, directory := range environment {
		if err := os.Mkdir(directory, 0700); err != nil {
			return nil, e.Wrap(e.CodeBuildFailed, err, "preparing native Go private directories")
		}
	}
	for _, variable := range node.Env {
		environment[variable.Name] = variable.Value
	}
	environment["PATH"] = filepath.Join(tool.goRoot, "bin") + ":/usr/bin:/bin:/usr/sbin:/sbin"
	environment["GOROOT"], environment["CC"] = tool.goRoot, tool.compiler
	environment["GOENV"], environment["GOWORK"], environment["GOPROXY"], environment["GOSUMDB"] = "off", "off", "off", "off"
	environment["GOFLAGS"] += " -mod=vendor"
	command := execCmd(options, filepath.Join(tool.goRoot, "bin", "go"), node.Command[1:]...)
	command.Dir = source.Dir
	for name, value := range environment {
		command.Env = append(command.Env, name+"="+value)
	}
	sort.Strings(command.Env)
	command.Stdout, command.Stderr = options.Stdout, options.Stderr
	fmt.Fprintf(options.Stdout, "\n=== native Go build %s (local, unqualified) ===\n", node.Component)
	if err := runOwnedCommand(ctx, command); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "native Go build %s", node.Component)
	}
	if err := check(); err != nil {
		return nil, err
	}
	binPath := artifactHostPath(options, node)
	if err := os.MkdirAll(filepath.Dir(binPath), 0755); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "preparing native output")
	}
	if err := copyCollectedArtifactContext(ctx, source.Dir, node.ArtifactPath, binPath); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "collecting native Go output")
	}
	sum, size, _, err := hashRegularBeneathContext(ctx, privOutDir(options, node), artifactOutputRelative(node))
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "observing native Go output")
	}
	return &ArtifactResult{Component: node.Component, OutputType: node.OutputType, Qualification: ArtifactUnqualified, Toolchain: node.Toolchain, InputDigest: source.InputDigest, BuilderType: ir.BuilderNativeGo, EngineClass: ir.BuilderNativeGo, Path: node.ArtifactPath, SHA256: sum, Size: size, AbsPath: binPath}, nil
}
