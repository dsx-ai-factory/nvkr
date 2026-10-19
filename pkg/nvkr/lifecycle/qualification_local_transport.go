// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// evidenceFiles freezes the complete local evidence handoff from authenticated
// owners. These bytes carry no process-local authority; a consumer must verify
// every signature and replay the inputs against independently captured payloads.
// Payload transport and durable publication are separate operations.
func (qualified *localVerifiedQualification) evidenceFiles(ctx context.Context) (map[string][]byte, error) {
	if qualified == nil {
		return nil, e.New(e.CodeGateUnmet, "local qualification verification is not live")
	}
	operation, finish, err := qualified.build.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	if err := qualified.requireLive(operation); err != nil {
		return nil, err
	}
	if qualified.signature == "" {
		return nil, e.New(e.CodeProvenanceMissing, "local qualification requires its retained signature")
	}
	build := qualified.build
	files := map[string][]byte{
		"build-evidence.json": []byte(build.document),
		"build-evidence.sig":  []byte(build.signature),
		"tool-smoke.json":     []byte(qualified.smoke.raw),
		"tool-smoke.sig":      []byte(qualified.smoke.signature),
		"qualification.json":  []byte(qualified.raw),
		"qualification.sig":   []byte(qualified.signature),
		"plan.json":           append([]byte(nil), build.inputs.planBytes...),
		"lifecycle.json":      append([]byte(nil), build.inputs.runBytes...),
		"compile-inputs.json": append([]byte(nil), build.inputs.inputBytes...),
	}
	if build.capture.materialBytes != "" {
		files["disk-materials.json"] = []byte(build.capture.materialBytes)
	}
	if err := qualified.requireLive(operation); err != nil {
		return nil, err
	}
	return files, nil
}
