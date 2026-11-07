// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runpresenter

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/gdamore/tcell/v2"
)

func TestLoadingCommandRemainsExplicitAcrossSizes(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 12}, {20, 6}} {
		screen := sim(t, size[0], size[1])
		state := progress.Snapshot{Mode: "command", Title: "Release metadata", Status: "loading", Note: "Loading 2/2; closure checks pending", Items: []progress.Item{{ID: "output/a", Name: "a", State: "loading"}}}
		NewView(true, true).Draw(screen, state, time.Now(), false)
		screen.Show()
		if !strings.Contains(strings.ToLower(text(screen)), "loading") {
			t.Fatalf("loading status hidden at %v", size)
		}
	}
}

func TestStayOpenJoinsOwnerThenClosesReadOnly(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failure], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			screen := &sessionScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8")}
			var finished atomic.Bool
			closed := false
			screen.onShow = func() {
				if strings.Contains(text(screen), "q confirm close") {
					if !finished.Load() {
						t.Error("result view opened before work joined")
					}
					closed = true
					_ = screen.PostEvent(key(tcell.KeyCtrlC, 0))
				}
			}
			var cause error
			if failure {
				cause = e.New(e.CodeVerifyFailed, "fixture closure failure")
			}
			result := Run(ctx, Options{Screen: screen, Live: true, StayOpen: true}, fixedSource{commandFixture()}, func(context.Context) error { finished.Store(true); return cause })
			if !closed || !screen.restored.Load() || !screen.pumpJoined.Load() || result.PresentationErr != nil || !errors.Is(result.WorkErr, cause) {
				t.Fatalf("read-only transition lost owner or cleanup: %+v", result)
			}
		})
	}
}
