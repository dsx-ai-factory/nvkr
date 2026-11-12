// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"sync"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
)

const authenticityConcurrency = 16

// Consume results in canonical output order. Later failures cannot replace an
// earlier subject's error simply by finishing first. Cancel and join the bounded
// speculative window before returning any failure; no verifier outlives its call.
func authenticateReleaseOutputs(ctx context.Context, outputs []delivery.PublicationOutput, verify func(context.Context, string) error) ([]Artifact, error) {
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	results := make([]chan error, len(outputs))
	launch := func(i int) {
		results[i] = make(chan error, 1)
		workers.Go(func() { results[i] <- verify(ctx, outputs[i].Reference) })
	}
	for i := 0; i < min(len(outputs), authenticityConcurrency); i++ {
		launch(i)
	}
	artifacts := make([]Artifact, len(outputs))
	for i, output := range outputs {
		select {
		case <-ctx.Done():
			return nil, authenticityContext(ctx)
		case err := <-results[i]:
			if err != nil {
				return nil, err
			}
		}
		if err := authenticityContext(ctx); err != nil {
			return nil, err
		}
		artifacts[i] = output.Output
		if next := i + authenticityConcurrency; next < len(outputs) {
			launch(next)
		}
	}
	return artifacts, authenticityContext(ctx)
}
