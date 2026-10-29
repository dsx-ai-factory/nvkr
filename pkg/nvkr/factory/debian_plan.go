// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"path/filepath"
	"runtime"

	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Raw package export is a local, unqualified artifact capability. Hosted jobs
// and independently invoked nodes do not own its durable lifecycle staging.
func rejectUnjoinedDebianPlan(plan *Plan, adapter string) error {
	for _, output := range plan.Definition.Outputs {
		if output.Format == authored.OutputDebianPackage {
			return e.New(e.CodeEngineCapability, "%s does not admit raw Debian outputs; use local lifecycle execution", adapter)
		}
	}
	return nil
}

func admitLocalDebianHost(plan *Plan, options LocalRunOptions) error {
	for _, output := range plan.Definition.Outputs {
		if output.Format != authored.OutputDebianPackage {
			continue
		}
		if options.DryRun {
			continue
		}
		if filepath.Base(options.Runtime) != plan.Environment.RuntimeBinary() {
			return e.New(e.CodeEngineCapability, "raw Debian execution runtime differs from its Environment")
		}
		if runtime.GOOS != "linux" || output.Platform != "linux/"+runtime.GOARCH {
			return e.New(e.CodeEngineCapability, "raw Debian execution requires its native Linux host")
		}
	}
	return nil
}
