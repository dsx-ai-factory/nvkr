// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/internal/qualificationprogress"
)

type localQualificationProgressKey struct{}

// The qualification owner updates this private tracker synchronously. Context
// propagation lets nested capture/smoke/transport owners report their own real
// boundaries without changing authority-bearing arguments or opening a channel.
type localQualificationProgress struct {
	observer         func(qualificationprogress.Event)
	stage, item      string
	completed, total int
}

func withLocalQualificationProgress(ctx context.Context, observer func(qualificationprogress.Event)) (context.Context, *localQualificationProgress) {
	progress := &localQualificationProgress{observer: observer}
	return context.WithValue(ctx, localQualificationProgressKey{}, progress), progress
}

func localProgress(ctx context.Context) *localQualificationProgress {
	progress, _ := ctx.Value(localQualificationProgressKey{}).(*localQualificationProgress)
	return progress
}

func (progress *localQualificationProgress) emit(event qualificationprogress.Event) {
	if progress == nil || progress.observer == nil || event.Validate() != nil {
		return
	}
	// A presentation panic must not skip signing-owner or process cleanup.
	defer func() { _ = recover() }()
	progress.observer(event)
}

func (progress *localQualificationProgress) start(stage string) {
	if progress == nil {
		return
	}
	progress.stage, progress.item = stage, ""
	progress.completed, progress.total = 0, 0
	progress.emit(qualificationprogress.Event{Kind: qualificationprogress.KindStage, Stage: stage, State: qualificationprogress.StateRunning})
}

func (progress *localQualificationProgress) finish() {
	if progress == nil {
		return
	}
	progress.emit(qualificationprogress.Event{Kind: qualificationprogress.KindStage, Stage: progress.stage, State: qualificationprogress.StateCompleted, Completed: progress.completed, Total: progress.total})
}

func (progress *localQualificationProgress) fail() {
	if progress == nil || progress.stage == "" {
		return
	}
	if progress.item != "" {
		progress.emit(qualificationprogress.Event{Kind: qualificationprogress.KindItem, Stage: progress.stage, State: qualificationprogress.StateFailed, Item: progress.item, Completed: progress.completed, Total: progress.total})
	}
	progress.emit(qualificationprogress.Event{Kind: qualificationprogress.KindStage, Stage: progress.stage, State: qualificationprogress.StateFailed, Completed: progress.completed, Total: progress.total})
}

func (progress *localQualificationProgress) startItem(item string, completed, total int) {
	if progress == nil || progress.stage == "" {
		return
	}
	progress.item, progress.completed, progress.total = item, completed, total
	progress.emit(qualificationprogress.Event{Kind: qualificationprogress.KindItem, Stage: progress.stage, State: qualificationprogress.StateRunning, Item: item, Completed: completed, Total: total})
}

func (progress *localQualificationProgress) finishItem() {
	if progress == nil || progress.item == "" {
		return
	}
	progress.completed++
	progress.emit(qualificationprogress.Event{Kind: qualificationprogress.KindItem, Stage: progress.stage, State: qualificationprogress.StateCompleted, Item: progress.item, Completed: progress.completed, Total: progress.total})
	progress.item = ""
}
