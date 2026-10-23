// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// NativeBuildPreparation describes freshly generated local inputs. It is not an
// execution receipt; RunLocal independently admits the selected toolchain.
type NativeBuildPreparation struct {
	Definition  string `json:"definition"`
	Environment string `json:"environment"`
	Toolchain   string `json:"toolchain"`
}

// PrepareNativeGo binds selected host CGO producers to the installed Mac SDK.
// It emits ordinary predecessor-pinned output-scoped catalog overlays, retaining
// the original catalog and Linux vendoring image. No source or source pin changes.
func PrepareNativeGo(ctx context.Context, definitionPath, environmentPath, goRoot, compiler, out string) (NativeBuildPreparation, error) {
	if ctx == nil {
		return NativeBuildPreparation{}, e.New(e.CodeUsage, "native Go preparation requires a context")
	}
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return NativeBuildPreparation{}, e.New(e.CodeEngineCapability, "native demo Go preparation currently requires Mac ARM64")
	}
	files := make(map[string]any)
	digest := ""
	prepared, err := prepareLocalInputs(ctx, definitionPath, environmentPath, out, files,
		func(environment *authored.Environment, absolute string) error {
			spec, err := pipeline.CaptureNativeGo(ctx, goRoot, compiler)
			if err != nil {
				return err
			}
			digest, err = spec.Digest()
			if err != nil {
				return err
			}
			files["toolchain.json"] = spec
			environment.Runtime.BuildNetworkPolicy = authored.BuildNetworkProducerDeclared
			if environment.Runtime.NativeToolchains == nil {
				environment.Runtime.NativeToolchains = make(map[string]authored.NativeToolchain)
			}
			environment.Runtime.NativeToolchains[digest] = authored.NativeToolchain{GoManifest: filepath.Join(absolute, "toolchain.json"), GoRoot: goRoot, Compiler: compiler}
			return nil
		},
		func(output authored.DefinitionOutput, name string, component catalog.Component) (catalog.Component, bool, error) {
			if output.Platform != "darwin/arm64" || !component.FromSource || !component.CGO {
				return component, false, nil
			}
			if selected, ok := component.BuildCommandByPlatform[output.Platform]; ok {
				component.BuildCommand = slices.Clone(selected)
			}
			if len(component.BuildCommand) == 0 || component.BuildCommand[0] != "go" {
				return component, false, e.New(e.CodeEngineCapability, "native Go producer %q must declare Go argv in its owning catalog", name)
			}
			component.BuilderType, component.ToolchainImage, component.BuildNetwork = catalog.BuilderNativeGo, digest, ir.NetworkDefault
			if !slices.Contains(component.Platforms, output.Platform) {
				return component, false, e.New(e.CodeEngineCapability, "producer %q does not declare the selected host platform", name)
			}
			return component, true, nil
		})
	if err != nil {
		return NativeBuildPreparation{}, err
	}
	return NativeBuildPreparation{Definition: prepared.Definition, Environment: prepared.Environment, Toolchain: digest}, nil
}

func closeLocalNativeGo(tools map[string]*pipeline.NativeGoToolchain) {
	for _, tool := range tools {
		_ = tool.Close()
	}
}

func admitLocalNativeGo(ctx context.Context, plan *Plan) (map[string]*pipeline.NativeGoToolchain, error) {
	tools := make(map[string]*pipeline.NativeGoToolchain)
	for _, node := range plan.Graph.Nodes {
		if node.Kind != NodeBuild || node.IRNode == "" {
			continue
		}
		_, instruction, err := plan.producerInstruction(node)
		if err != nil {
			closeLocalNativeGo(tools)
			return nil, err
		}
		if instruction.BuilderType != ir.BuilderNativeGo {
			continue
		}
		if plan.Environment.Publication != nil || plan.Environment.Signing != nil {
			closeLocalNativeGo(tools)
			return nil, e.New(e.CodeEngineCapability, "native Go cannot publish or sign")
		}
		if tools[instruction.Toolchain] != nil {
			continue
		}
		location, ok := plan.Environment.Runtime.NativeToolchains[instruction.Toolchain]
		if !ok || location.GoManifest == "" {
			closeLocalNativeGo(tools)
			return nil, e.New(e.CodeEngineCapability, "native Go requires an installed compiler location bound to its exact identity")
		}
		tool, err := pipeline.OpenNativeGo(ctx, location.GoManifest, instruction.Toolchain, location.GoRoot, location.Compiler)
		if err != nil {
			closeLocalNativeGo(tools)
			return nil, err
		}
		tools[instruction.Toolchain] = tool
	}
	return tools, nil
}
