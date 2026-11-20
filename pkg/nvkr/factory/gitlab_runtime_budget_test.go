// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestGitLabNativeJobBudgetReservation(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		started time.Time
		caller  time.Duration
		code    e.Code
	}{
		{name: "fresh native job", started: now},
		{name: "nine minutes of Runner preparation", started: now.Add(-9 * time.Minute)},
		{name: "transfer allowance exhausted", started: now.Add(-10 * time.Minute), code: e.CodeTimeout},
		{name: "late preparation", started: now.Add(-18 * time.Minute), code: e.CodeTimeout},
		{name: "missing provider start", code: e.CodeProvenanceMissing},
		{name: "future provider start", started: now.Add(time.Second), code: e.CodeProvenanceMissing},
		{name: "caller leaves complete budget", started: now, caller: 70 * time.Minute},
		{name: "caller cannot cover execution and completion", started: now, caller: 65 * time.Minute, code: e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			if tc.caller != 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, now.Add(tc.caller))
				defer cancel()
			}
			preparation, deadline, err := gitlabPhaseDeadlines(ctx, tc.started, now, time.Hour)
			if tc.code != "" {
				assertFactoryErrorCode(t, err, tc.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if deadline.Sub(preparation) != time.Hour+gitlabCompletionAllowance {
				t.Fatal("preparation consumed reserved execution or completion time")
			}
			if deadline.After(tc.started.Add(75 * time.Minute)) {
				t.Fatal("native job clock was extended")
			}
		})
	}
}

func TestGitLabFinalizerRevalidationRequiresLiveOwner(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		owner *GitLabFinalizer
		code  e.Code
	}{
		{"nil context", nil, &GitLabFinalizer{}, e.CodeUsage},
		{"nil owner", t.Context(), nil, e.CodeProvenanceMissing},
		{"zero owner", t.Context(), &GitLabFinalizer{}, e.CodeProvenanceMissing},
		{"canceled owner", t.Context(), &GitLabFinalizer{state: &gitlabAdmission{lifetime: canceled}}, e.CodeTimeout},
		{"canceled caller", canceled, &GitLabFinalizer{state: &gitlabAdmission{lifetime: t.Context()}}, e.CodeTimeout},
		{"closed owner", t.Context(), &GitLabFinalizer{state: &gitlabAdmission{lifetime: t.Context(), closed: true}}, e.CodeProvenanceMissing},
	} {
		t.Run(tc.name, func(t *testing.T) { assertFactoryErrorCode(t, tc.owner.Revalidate(tc.ctx), tc.code) })
	}
}
