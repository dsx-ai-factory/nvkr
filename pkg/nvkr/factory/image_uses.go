// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"fmt"
	"sort"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// ImageRoute describes required materialization, never observed availability or
// execution authority. Local IDs and native toolchains are not OCI references.
type ImageRoute string

const (
	ImageAnonymous     ImageRoute = "anonymous"
	ImageScopedReader  ImageRoute = "scoped-reader"
	ImageMeasuredLocal ImageRoute = "measured-local-image"
	ImageNativeBundle  ImageRoute = "native-bundle"
)

// ImageUse is one selected consumer, before job grouping or renderer filtering.
// ConsumerID is the scheduler node ID; startup uses have a runtime/ prefix.
// Role is producer, vendor, utility, inspector, diagnostic, startup, or service.
// Startup/service uses describe the shared startup configuration per placement.
// Reference is an immutable registry subject, local image ID, or native digest,
// according to Route. Reader contains references only, never credentials.
// Returned values are descriptions; callers must authenticate runtime bytes.
type ImageUse struct {
	ConsumerID string
	Role       string
	Reference  string
	Platform   string
	Placement  string
	Route      ImageRoute
	Reader     authored.ToolchainReader
}

// ImageUses validates the complete Plan, then inventories selected execution
// images independently of rendered jobs. engine is local or github. The local
// view describes existing native/local sources; it does not prepare them or
// admit a GitHub reader to local execution. GitHub refuses unsupported routes.
func (plan *Plan) ImageUses(engine string) ([]ImageUse, error) {
	if plan == nil {
		return nil, e.New(e.CodeUsage, "image inventory requires a Plan")
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return plan.imageUses(engine)
}

// imageUses is also used inside admission after complete Plan validation.
func (plan *Plan) imageUses(engine string) ([]ImageUse, error) {
	if engine != "github" && engine != "local" {
		return nil, e.New(e.CodeEngineCapability, "image inventory has unsupported engine %q", engine)
	}
	classifier, err := plan.Environment.ToolchainAccess.ImageClassifier()
	if err != nil {
		return nil, err
	}
	var uses []ImageUse
	placements := map[string]string{}
	appendUse := func(use ImageUse, native bool) error {
		if use.Reference == "" {
			return nil
		}
		resolved, err := plan.resolveImageUse(classifier, engine, use, native)
		if err != nil {
			return err
		}
		uses = append(uses, resolved)
		return nil
	}

	for _, node := range plan.Graph.Nodes {
		var execution Node
		if engine == "github" {
			execution, err = plan.GitHubExecutionFor(node)
		} else {
			execution, err = plan.executionForProducer(node)
		}
		if err != nil {
			return nil, err
		}
		if engine == "github" && (plan.Environment.Runtime.RuntimeImage != "" || len(plan.Environment.Runtime.Services) > 0) {
			platform, err := authored.GitHubRunnerPlatform(githubBootstrapRunner(execution.RunsOn))
			if err != nil {
				return nil, err
			}
			placements[execution.RunsOn] = platform
		}
		role, native := "utility", false
		if node.IRNode != "" {
			_, instruction, err := plan.producerInstruction(node)
			if err != nil {
				return nil, err
			}
			switch instruction.Kind {
			case ir.KindBuild:
				role = "producer"
				if engine == "local" && instruction.ToolchainPlatform != "" {
					execution.Platform = instruction.ToolchainPlatform
				}
				_, declaredNative := plan.Environment.Runtime.NativeToolchains[instruction.Toolchain]
				native = declaredNative && (instruction.BuilderType == ir.BuilderVMImage || instruction.BuilderType == ir.BuilderNativeGo)
			case ir.KindFetch:
				role = "vendor"
			}
		} else if node.Kind == NodeSBOM {
			role = "inspector"
		}
		if err := appendUse(ImageUse{ConsumerID: node.ID, Role: role, Reference: execution.Container, Platform: execution.Platform, Placement: execution.RunsOn}, native); err != nil {
			return nil, err
		}
	}
	// Startup images execute before any step. They cannot use per-step readers.
	if engine == "github" {
		for _, placement := range sortedKeys(placements) {
			if err := appendUse(ImageUse{ConsumerID: "runtime/container/" + placement, Role: "startup", Reference: plan.Environment.Runtime.RuntimeImage, Platform: placements[placement], Placement: placement}, false); err != nil {
				return nil, err
			}
			for i, image := range plan.Environment.Runtime.Services {
				if err := appendUse(ImageUse{ConsumerID: fmt.Sprintf("runtime/service/%d/%s", i, placement), Role: "service", Reference: image, Platform: placements[placement], Placement: placement}, false); err != nil {
					return nil, err
				}
			}
		}
	}
	sort.Slice(uses, func(i, j int) bool { return uses[i].ConsumerID < uses[j].ConsumerID })
	return uses, nil
}

// resolveImageUse is shared by complete inventory and selected diagnostics.
func (plan *Plan) resolveImageUse(classifier authored.ToolchainImageClassifier, engine string, use ImageUse, native bool) (ImageUse, error) {
	if canonical.IsDigest(use.Reference) {
		if engine == "github" {
			return ImageUse{}, e.New(e.CodeEngineCapability, "GitHub image consumer %q requires a registry subject", use.ConsumerID)
		}
		use.Route = ImageMeasuredLocal
		if native {
			use.Route = ImageNativeBundle
		}
	} else {
		private, err := classifier.ClassifyImage(use.Reference)
		if err != nil {
			return ImageUse{}, e.Wrap(e.CodeEngineCapability, err, "image access for %s %q", use.Role, use.ConsumerID)
		}
		use.Route = ImageAnonymous
		if private {
			access := plan.Environment.ToolchainAccess
			if engine != "github" || plan.Environment.GitHubARC != "native-kvm-v1" || use.Role == "startup" || use.Role == "service" || access.Reader == nil {
				return ImageUse{}, e.New(e.CodeEngineCapability, "private image for %s %q requires native-kvm-v1 and a declared toolchainAccess reader; private startup and ordinary hosted/local routes are unsupported", use.Role, use.ConsumerID)
			}
			use.Route, use.Reader = ImageScopedReader, *access.Reader
		}
	}
	return use, nil
}

// runtimeImageAccess projects the admitted inventory without reimplementing its
// classification policy. A missing consumer is allowed only when it has no image.
func (plan *Plan) runtimeImageAccess(node Node, engine string) (pipeline.ImageAccess, error) {
	if plan == nil {
		return "", e.New(e.CodeUsage, "runtime image admission requires a Plan")
	}
	uses, err := plan.imageUses(engine)
	if err != nil {
		return "", err
	}
	for _, use := range uses {
		if use.ConsumerID != node.ID {
			continue
		}
		switch use.Route {
		case ImageAnonymous:
			return pipeline.ImageAnonymous, nil
		case ImageScopedReader:
			return pipeline.ImageScopedReader, nil
		case ImageMeasuredLocal:
			return pipeline.ImageMeasuredLocal, nil
		case ImageNativeBundle:
			return "", nil
		default:
			return "", e.New(e.CodeEngineCapability, "unsupported runtime image route")
		}
	}
	if node.Container != "" {
		return "", e.New(e.CodeEngineCapability, "image consumer is absent from admitted inventory")
	}
	return "", nil
}

func (plan *Plan) operationImageEngine() string {
	if plan.Environment.GitHubARC != "" || len(plan.Environment.GitHubRunners) > 0 {
		return "github"
	}
	return "local"
}
