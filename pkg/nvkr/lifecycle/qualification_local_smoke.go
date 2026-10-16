// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"sync/atomic"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

type localToolSmokeReport struct {
	SchemaVersion       string            `json:"schemaVersion"`
	Lane                string            `json:"lane"`
	BuildEvidenceDigest string            `json:"buildEvidenceDigest"`
	PolicyDigest        string            `json:"policyDigest"`
	Platform            string            `json:"platform"`
	Image               string            `json:"image"`
	RuntimeImage        string            `json:"runtimeImage"`
	RuntimeIdentity     string            `json:"runtimeIdentity"`
	Members             []ToolSmokeMember `json:"members"`
}

// localToolSmokeReceipt borrows the independently verified build. It is issued
// only after every selected tool passes and runtime cleanup succeeds. It cannot
// be used as a hosted receipt or reconstructed from its report.
type localToolSmokeReceipt struct {
	raw    string
	build  *localVerifiedBuild
	closed *atomic.Bool
}

func (receipt *localToolSmokeReceipt) Close() error {
	if receipt != nil && receipt.closed != nil {
		receipt.closed.Store(true)
	}
	return nil
}

func (receipt *localToolSmokeReceipt) description(ctx context.Context) (localToolSmokeReport, error) {
	if receipt == nil || receipt.closed == nil || receipt.closed.Load() || receipt.raw == "" {
		return localToolSmokeReport{}, e.New(e.CodeGateUnmet, "local smoke receipt is not live")
	}
	if err := receipt.build.requireLive(ctx); err != nil {
		return localToolSmokeReport{}, err
	}
	var report localToolSmokeReport
	if err := canonical.UnmarshalExact([]byte(receipt.raw), &report); err != nil {
		return localToolSmokeReport{}, e.Wrap(e.CodeGateUnmet, err, "decoding held local smoke report")
	}
	return report, nil
}

func (build *localVerifiedBuild) requireLive(ctx context.Context) error {
	if build == nil || build.lifetime == nil || build.document == "" {
		return e.New(e.CodeGateUnmet, "local smoke requires an authenticated build owner")
	}
	build.lifetime.mu.Lock()
	closing := build.lifetime.closing
	build.lifetime.mu.Unlock()
	if closing {
		return e.New(e.CodeGateUnmet, "local build owner is closing")
	}
	current, err := prepareLocalBuildEvidence(ctx, build.inputs, build.capture, build.policy)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, []byte(build.document)) {
		return e.New(e.CodeVerifyFailed, "local build bindings changed after verification")
	}
	return nil
}

func localSmokeMembers(ctx context.Context, build *localVerifiedBuild) ([]ToolSmokeMember, error) {
	if err := build.requireLive(ctx); err != nil {
		return nil, err
	}
	planned, err := toolSmokePlanScope(build.inputs.plan, build.policy.Smoke)
	if err != nil {
		return nil, err
	}
	checked := make([]ArtifactCheckOutput, 0, len(build.inputs.run.Observed))
	for _, output := range build.inputs.run.Observed {
		selected, err := smokeOutputBuild(build.inputs.plan, output.Name)
		if err != nil {
			return nil, err
		}
		// This projection is private shared scope input, never a hosted A receipt.
		checked = append(checked, ArtifactCheckOutput{Name: output.Name, Kind: output.Kind, BuildDigest: output.BuildDigest, Subject: output.Digest, Platform: selected.Platform, Overall: "pass"})
	}
	return deriveToolSmokeMembers(planned, checked, build.capture.ordinary)
}

func runLocalToolSmoke(ctx context.Context, build *localVerifiedBuild, options NativeToolSmokeOptions) (*localToolSmokeReceipt, error) {
	return runLocalToolSmokeWith(ctx, build, options, runtime.GOOS+"/"+runtime.GOARCH, openNativeSmokeRuntime)
}

func runLocalToolSmokeWith(ctx context.Context, build *localVerifiedBuild, options NativeToolSmokeOptions, platform string, open func(context.Context, pipeline.ToolSmokeSelection) (nativeSmokeRuntime, error)) (receipt *localToolSmokeReceipt, resultErr error) {
	operation, finish, err := build.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	ctx = operation
	members, err := localSmokeMembers(ctx, build)
	if err != nil {
		return nil, err
	}
	if platform != build.policy.Platform || !qualificationNativePlatform(platform) || len(members) == 0 {
		return nil, e.New(e.CodeEngineCapability, "local smoke requires the exact native platform and nonempty tool scope")
	}
	for _, member := range members {
		if member.Platform != platform {
			return nil, e.New(e.CodeEngineCapability, "local smoke cannot omit a nonnative member")
		}
	}
	if open == nil {
		return nil, e.New(e.CodeUsage, "local smoke requires a runtime adapter")
	}
	digest, err := build.policy.Digest()
	if err != nil {
		return nil, err
	}
	report := localToolSmokeReport{SchemaVersion: "nvkr.dev/local-tool-smoke/v1", Lane: factory.TierLocalExecution.String(), BuildEvidenceDigest: pipeline.FileDigest([]byte(build.document)), PolicyDigest: digest, Platform: platform, Image: build.policy.Smoke.Images[platform], Members: []ToolSmokeMember{}}
	if options.RuntimeExecutable == "" {
		options.RuntimeExecutable = "docker"
	}
	owner, err := open(ctx, pipeline.ToolSmokeSelection{RuntimeExecutable: options.RuntimeExecutable, Image: report.Image, Platform: platform})
	if err != nil {
		return nil, err
	}
	if owner == nil {
		return nil, e.New(e.CodeEngineCapability, "local smoke returned no runtime owner")
	}
	closed := false
	defer func() {
		if !closed {
			if err := owner.Close(); err != nil {
				resultErr = errors.Join(resultErr, e.Wrap(e.CodeEngineCapability, err, "closing local smoke runtime"))
			}
		}
		if resultErr != nil {
			receipt = nil
		}
	}()
	for _, member := range members {
		if err := build.requireLive(ctx); err != nil {
			return nil, err
		}
		observed, err := runHeldSmokeMember(ctx, build.capture.ordinary, owner, member)
		if err != nil {
			return nil, err
		}
		evaluated, err := evaluateNativeSmokeMember(member, observed)
		if err != nil {
			return nil, err
		}
		if report.RuntimeImage != "" && (report.RuntimeImage != observed.Image() || report.RuntimeIdentity != observed.RuntimeIdentity()) {
			return nil, e.New(e.CodeVerifyFailed, "local smoke runtime changed between tools")
		}
		report.RuntimeImage, report.RuntimeIdentity = observed.Image(), observed.RuntimeIdentity()
		report.Members = append(report.Members, evaluated)
	}
	err = owner.Close()
	closed = true
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "closing local smoke runtime")
	}
	if err := build.requireLive(ctx); err != nil {
		return nil, err
	}
	raw, err := canonical.Marshal(report)
	if err != nil {
		return nil, err
	}
	if len(raw) > 4<<20 {
		return nil, e.New(e.CodeVerifyFailed, "local smoke report exceeds its byte bound")
	}
	return &localToolSmokeReceipt{raw: string(raw), build: build, closed: new(atomic.Bool)}, nil
}
