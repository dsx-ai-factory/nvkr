// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPackerObserverRetentionKeepsIndependentCleanupBudgets(t *testing.T) {
	runtime := &cleanupBudgetPackerRuntime{}
	resources := &packerContainerResources{
		runtime: runtime, containers: []string{"observer", "build"}, volumes: []string{"input", "work"},
		retainedObserver: &packerActivityRetention{container: "observer", work: "work"},
	}
	err := resources.cleanupWithTimeout(10 * time.Millisecond)
	if code(err) != e.CodeTimeout || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("retained observer or independent cleanup error lost: %v", err)
	}
	if runtime.volumeContextErr != nil || !runtime.volumeRemoved {
		t.Fatal("input-volume cleanup inherited the exhausted container budget")
	}
	if len(resources.containers) != 1 || resources.containers[0] != "observer" ||
		len(resources.volumes) != 1 || resources.volumes[0] != "work" {
		t.Fatal("observer dependency closure not retained")
	}
}
