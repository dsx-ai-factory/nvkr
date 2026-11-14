// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

// selectProjectionPilot is the only CLI route to a projection selection.
// Tests trap it to prove generated bootstrap invocations never reach it.
var selectProjectionPilot = projectionPilotContext

// projectionPilotContext selects this binary's reference projection plugin
// over the named transport for top-level generation. It is a diagnostic pilot
// selector: generated commands never pass --projection-pilot, so no generated
// stage reaches the plugin.
func projectionPilotContext(ctx context.Context, transport, engine string) (context.Context, error) {
	if err := factory.ValidateGenerationEngine(engine); err != nil {
		return nil, err
	}
	adapter := factory.ProjectionAdapter{Transport: factory.ProjectionTransport(transport), Release: factoryGeneratorVersion()}
	switch adapter.Transport {
	case factory.ProjectionInProcess:
	default:
		return nil, e.New(e.CodeUsage, "--projection-pilot must be %s", factory.ProjectionInProcess)
	}
	return factory.WithProjectionAdapter(ctx, adapter), nil
}
