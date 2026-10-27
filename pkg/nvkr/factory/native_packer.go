// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// NativePackerPreparation identifies a local dependency bundle, not execution
// authority. The selected Run independently opens and authenticates the bundle.
type NativePackerPreparation struct {
	Layout    string `json:"layout"`
	Bundle    string `json:"bundle"`
	Toolchain string `json:"toolchain"`
	Platform  string `json:"platform"`
}

// PrepareNativePackerBuild selects native assembly for existing Packer producers.
// It preserves source pins, Linux material producers and the vendoring image;
// host requirements and declared Packer variables are explicit local overrides.
// The caller must supply an Environment declaring the matching hardware class.
func PrepareNativePackerBuild(ctx context.Context, definitionPath, environmentPath, layout, bundle, digest, out string, variables map[string]string) (NativeBuildPreparation, error) {
	return PrepareNativePackerBuildWithAccelerator(ctx, definitionPath, environmentPath, layout, bundle, digest, out, "hvf", variables)
}

// PrepareNativePackerBuildWithAccelerator admits an explicit Mac acceleration
// contract. Accelerator selection does not establish source/kernel compatibility.
// Both modes require a matching independently authored Environment class.
func PrepareNativePackerBuildWithAccelerator(ctx context.Context, definitionPath, environmentPath, layout, bundle, digest, out, accelerator string, variables map[string]string) (NativeBuildPreparation, error) {
	if ctx == nil {
		return NativeBuildPreparation{}, e.New(e.CodeUsage, "native Packer input preparation requires a context")
	}
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return NativeBuildPreparation{}, e.New(e.CodeEngineCapability, "native Packer input preparation currently requires Mac ARM64")
	}
	if accelerator != "hvf" && accelerator != "tcg" {
		return NativeBuildPreparation{}, e.New(e.CodeEngineCapability, "native Mac Packer requires explicit hvf or tcg acceleration")
	}
	files := make(map[string]any)
	prepared, err := prepareLocalInputs(ctx, definitionPath, environmentPath, out, files,
		func(environment *authored.Environment, _ string) error {
			toolchain, err := openNativePackerBundle(ctx, layout, bundle, digest, runtime.GOOS+"/"+runtime.GOARCH)
			if err != nil {
				return err
			}
			declared := make([]pipeline.PackerVariable, 0, len(variables))
			for _, key := range slices.Sorted(maps.Keys(variables)) {
				declared = append(declared, pipeline.PackerVariable{Name: key, Value: variables[key]})
			}
			_, variableErr := toolchain.NativeVariables(ctx, declared)
			closeErr := toolchain.Close()
			if variableErr != nil {
				return errors.Join(variableErr, closeErr)
			}
			if closeErr != nil {
				return e.Wrap(e.CodeBuildFailed, closeErr, "closing native Packer preparation")
			}
			absolute, err := filepath.Abs(layout)
			if err != nil {
				return e.Wrap(e.CodeUsage, err, "resolving native Packer dependency layout")
			}
			environment.Runtime.BuildNetworkPolicy = authored.BuildNetworkProducerDeclared
			if environment.Runtime.NativeToolchains == nil {
				environment.Runtime.NativeToolchains = make(map[string]authored.NativeToolchain)
			}
			environment.Runtime.NativeToolchains[digest] = authored.NativeToolchain{Layout: absolute, Bundle: bundle}
			return nil
		},
		func(output authored.DefinitionOutput, name string, component catalog.Component) (catalog.Component, bool, error) {
			if component.BuilderType != catalog.BuilderVMImage || component.Packer == nil {
				return component, false, nil
			}
			if output.Platform != "linux/arm64" || component.BuildRequirements == nil || component.BuildRequirements.Resources == nil {
				return component, false, e.New(e.CodeEngineCapability, "native Packer producer %q requires an explicit ARM64 guest and resource budget", name)
			}
			component.ToolchainImage = digest
			component.BuildRequirements = execution.Clone(component.BuildRequirements)
			component.BuildRequirements.AllowedHostPlatforms = []execution.HostPlatform{execution.HostPlatformDarwinARM64}
			component.BuildRequirements.Capabilities = []execution.CapabilityRequirement{{Name: execution.CapabilityHVF, Policy: execution.CapabilityRequired}}
			if accelerator == "tcg" {
				component.BuildRequirements.Capabilities = []execution.CapabilityRequirement{
					{Name: execution.CapabilityHVF, Policy: execution.CapabilityForbidden},
				}
			}
			packer := *component.Packer
			packer.Variables = maps.Clone(packer.Variables)
			for _, key := range slices.Sorted(maps.Keys(variables)) {
				if _, ok := packer.Variables[key]; !ok {
					return component, false, e.New(e.CodeInvalidCatalog, "producer %q does not declare overridden Packer variable %q", name, key)
				}
				packer.Variables[key] = variables[key]
			}
			component.Packer = &packer
			return component, true, nil
		})
	if err != nil {
		return NativeBuildPreparation{}, err
	}
	return NativeBuildPreparation{Definition: prepared.Definition, Environment: prepared.Environment, Toolchain: digest}, nil
}

// PrepareNativePacker captures explicit native tools through the existing
// binary-bundle owner, then validates them through the normal Packer resolver.
// The manifest uses delivery.BinaryBundleRequest; it neither builds an OS image
// nor publishes remotely. Installed dynamic libraries remain host dependencies
// of this local, unqualified execution lane.
func PrepareNativePacker(ctx context.Context, manifest, out string) (result NativePackerPreparation, returnedErr error) {
	if ctx == nil {
		return result, e.New(e.CodeUsage, "native Packer preparation requires a context")
	}
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return result, e.Wrap(e.CodeTimeout, err, "preparing native Packer dependencies")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return result, e.New(e.CodeEngineCapability, "native Packer preparation requires Mac or Linux")
	}
	if filepath.IsAbs(out) || filepath.Clean(out) != out || !strings.HasPrefix(out, ".nvkr/") || strings.ContainsAny(out, "\r\n\x00") {
		return result, e.New(e.CodeUsage, "native Packer preparation requires a fresh repository-relative .nvkr directory")
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		return result, e.New(e.CodeUsage, "native Packer preparation destination already exists or cannot be inspected")
	}
	held, info, err := securepath.OpenRegular(manifest)
	if err != nil {
		return result, e.Wrap(e.CodeUsage, err, "opening native Packer file selection")
	}
	defer held.Close()
	if info.Size() > 1<<20 {
		return result, e.New(e.CodeUsage, "native Packer file selection exceeds 1 MiB")
	}
	raw, err := io.ReadAll(io.LimitReader(held, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return result, e.Wrap(e.CodeUsage, err, "reading bounded native Packer file selection")
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return result, e.Wrap(e.CodeUsage, err, "invalid native Packer file selection")
	}
	var request delivery.BinaryBundleRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return result, e.Wrap(e.CodeUsage, err, "decoding native Packer file selection")
	}
	if request.OS != runtime.GOOS || request.Arch != runtime.GOARCH || request.Purpose != "packer-toolchain" || request.ReleaseSetDigest != "" || request.InventoryDigest != "" {
		return result, e.New(e.CodeEngineCapability, "native Packer preparation requires matching host tools and local packer-toolchain purpose")
	}
	for _, file := range request.Files {
		if !filepath.IsAbs(file.Path) || filepath.Clean(file.Path) != file.Path {
			return result, e.New(e.CodeUsage, "native Packer files require clean absolute source paths")
		}
	}
	root, err := os.MkdirTemp("", "nvkr-native-packer-preparation-")
	if err != nil {
		return result, e.Wrap(e.CodeBuildFailed, err, "creating native Packer staging")
	}
	defer func() {
		if err := os.RemoveAll(root); err != nil {
			returnedErr = e.Wrap(e.CodeBuildFailed, errors.Join(returnedErr, err), "cleaning native Packer staging")
		}
	}()
	bundle, err := delivery.BuildBinaryBundle(ctx, filepath.Join(root, "bundle"), request)
	if err != nil {
		return result, err
	}
	const name = "packer-tools"
	publication := delivery.PublicationRequest{Objects: []delivery.ObjectInput{{
		Name: name, Kind: "binary-bundle", Path: bundle.LayoutPath, Layout: true,
		ArtifactType: delivery.MediaBinaryBundle, SemanticDigest: bundle.SemanticDigest, OS: request.OS, Arch: request.Arch,
	}}}
	staged := filepath.Join(root, "layout")
	if _, err := delivery.BuildPublicationRoot(ctx, staged, publication); err != nil {
		return result, err
	}
	toolchain, err := openNativePackerBundle(ctx, staged, name, bundle.SemanticDigest, request.OS+"/"+request.Arch)
	if err != nil {
		return result, err
	}
	if err := toolchain.Close(); err != nil {
		return result, e.Wrap(e.CodeBuildFailed, err, "closing admitted native Packer preparation")
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return result, e.Wrap(e.CodeBuildFailed, err, "creating native Packer dependency parent")
	}
	// The delivery owner publishes a new local layout only after complete native
	// validation. Failure above cannot leave a destination that looks usable.
	if _, err := delivery.BuildPublicationRoot(ctx, out, publication); err != nil {
		return result, err
	}
	return NativePackerPreparation{Layout: out, Bundle: name, Toolchain: bundle.SemanticDigest, Platform: request.OS + "/" + request.Arch}, nil
}
