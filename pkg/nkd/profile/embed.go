// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package profile

import (
	"context"
	"runtime"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"sigs.k8s.io/yaml"
)

const maxAuthoredBuilderProfileBytes int64 = 4 << 20

// Field names are stable keys shared by the profile and the host observer so
// preflight can compare them.
const (
	FieldRunnerOS          = "runner.os"
	FieldTargetArch        = "target.arch"
	FieldRuntimeCapability = "host.container-runtime-capability"
	FieldKernel            = "runner.kernel"
	FieldClockPolicy       = "build.clock-policy"
)

// Embedded returns the default operator-controlled builder profile for a target
// arch. It pins target.arch as hard-fail because arch is demonstrated
// byte-determinative (the multi-arch model-B work proved a distinct, per-arch
// artifact set). runner.os / the runtime capability / runner.kernel are recorded but
// advisory: nkdctl's hermetic pinned-by-digest toolchain containers with a fixed
// clock/locale policy have NOT been demonstrated to let those fields change output,
// and NKD v1 §8.2.2 forbids marking a field hard-fail until such a divergence is
// demonstrated. As evidence accrues (e.g. a two-machine bake-off surfaces a
// kernel-sensitive lane) a field graduates to hard-fail. This is the honest
// reconciliation toward NKD v1's profile-bounded thesis: the host dependence is made
// explicit and pinned, not assumed away.
func Embedded(arch string) BuilderProfile {
	if arch == "" {
		arch = "amd64"
	}
	return BuilderProfile{
		SchemaVersion:    SchemaVersion,
		Identity:         "nkd-local-" + arch + "-v1",
		Version:          "1",
		HostControlClass: OperatorControlled,
		Fields: []Field{
			{Name: FieldTargetArch, Value: arch, HardFail: true, MeasurementStage: MeasurementHost},
			{Name: FieldRunnerOS, Value: "linux", HardFail: false, MeasurementStage: MeasurementHost},
			{Name: FieldRuntimeCapability, Value: "available", HardFail: false, MeasurementStage: MeasurementHost},
			{Name: FieldKernel, Value: "any", HardFail: false, MeasurementStage: MeasurementHost},
			{Name: FieldClockPolicy, Value: "SOURCE_DATE_EPOCH+TZ=UTC+LC_ALL=C", HardFail: false, MeasurementStage: MeasurementHost},
		},
	}
}

// LoadContext captures an authored profile through a bounded, nonblocking,
// no-follow regular-file capability and observes cancellation throughout the copy.
// The returned value is still unqualified authoring data, not authentication proof.
func LoadContext(ctx context.Context, path string) (BuilderProfile, error) {
	if err := profileLoadContextError(ctx); err != nil {
		return BuilderProfile{}, err
	}
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{
		Label: "authored-builder-profile", MaxBytes: maxAuthoredBuilderProfileBytes,
	})
	if err != nil {
		if ctxErr := profileLoadContextError(ctx); ctxErr != nil {
			return BuilderProfile{}, ctxErr
		}
		return BuilderProfile{}, e.Wrap(e.CodeInvalidCatalog, err, "reading builder profile %s", path)
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, maxAuthoredBuilderProfileBytes)
	if err != nil {
		if ctxErr := profileLoadContextError(ctx); ctxErr != nil {
			return BuilderProfile{}, ctxErr
		}
		return BuilderProfile{}, e.Wrap(e.CodeInvalidCatalog, err, "reading builder profile %s", path)
	}
	var p BuilderProfile
	if err := yaml.UnmarshalStrict(raw, &p); err != nil {
		return BuilderProfile{}, e.Wrap(e.CodeInvalidCatalog, err, "parsing builder profile %s", path)
	}
	if err := p.Validate(); err != nil {
		return BuilderProfile{}, err
	}
	return p, nil
}

func profileLoadContextError(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "builder profile load requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "builder profile load canceled")
	}
	return nil
}

// ObserveHost gathers the current host's facts as a field map for Preflight. These
// are runtime observations (host kernel, arch, runtime) — deliberately NOT part of
// the IR or any digest, so reproducibility is unaffected; they exist only to check
// the host against the selected profile before a build.
func ObserveHost() map[string]string {
	facts := map[string]string{
		FieldRunnerOS:          runtime.GOOS,
		FieldTargetArch:        runtime.GOARCH,
		FieldClockPolicy:       "SOURCE_DATE_EPOCH+TZ=UTC+LC_ALL=C",
		FieldKernel:            kernelRelease(),
		FieldRuntimeCapability: containerRuntimeCapability(),
	}
	return facts
}

func containerRuntimeCapability() string {
	if _, err := findExecutable("docker"); err == nil {
		return "available"
	}
	if _, err := findExecutable("podman"); err == nil {
		return "available"
	}
	return ""
}
