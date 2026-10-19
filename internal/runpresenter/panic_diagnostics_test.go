// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runpresenter

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/rundiagnostics"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/gdamore/tcell/v2"
)

type diagnosticPanicScreen struct {
	*sessionScreen
	reader, restoration bool
}

func (s *diagnosticPanicScreen) ChannelEvents(events chan<- tcell.Event, quit <-chan struct{}) {
	if s.reader {
		defer s.pumpJoined.Store(true)
		panic("private reader sentinel")
	}
	s.sessionScreen.ChannelEvents(events, quit)
}

func (s *diagnosticPanicScreen) Fini() {
	s.sessionScreen.Fini()
	if s.restoration {
		panic("private restoration sentinel")
	}
}

func TestRecoveredPanicsRetainPrivateBoundedStacksAfterJoinedReturn(t *testing.T) {
	for _, origin := range []string{"worker", "renderer", "reader", "restoration"} {
		t.Run(origin, func(t *testing.T) {
			var diagnostics rundiagnostics.Buffer
			screen := &diagnosticPanicScreen{sessionScreen: &sessionScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8"), panicDraw: origin == "renderer"}, reader: origin == "reader", restoration: origin == "restoration"}
			if origin == "restoration" {
				var once sync.Once
				screen.onShow = func() { once.Do(func() { _ = screen.PostEvent(key(tcell.KeyCtrlC, 0)) }) }
			}
			options := Options{Screen: screen, Live: origin != "restoration", Diagnostics: &diagnostics}
			var joined atomic.Bool
			var work func(context.Context) error
			if options.Live {
				work = func(ctx context.Context) error {
					defer joined.Store(true)
					if origin == "worker" {
						panic("private worker sentinel")
					}
					<-ctx.Done()
					return nil
				}
			}
			state := fixture()
			result := Run(t.Context(), options, fixedSource{state}, work)
			if !screen.restored.Load() || !screen.pumpJoined.Load() || options.Live && !joined.Load() {
				t.Fatal("panic returned before restoration and owned joins")
			}
			raw, _ := diagnostics.Snapshot()
			if len(raw) > rundiagnostics.MaxBytes || !strings.Contains(string(raw), "private "+origin) || !strings.Contains(string(raw), "goroutine ") || !strings.Contains(string(raw), "session.go:") {
				t.Fatalf("panic value or bounded stack was lost: %s", raw)
			}
			for _, err := range []error{result.WorkErr, result.PresentationErr} {
				if err != nil && strings.Contains(err.Error(), "private") {
					t.Fatal("private panic details escaped into public error")
				}
			}
			public, err := json.Marshal(state)
			if err != nil || strings.Contains(string(public), "private") {
				t.Fatal("private panic reached public snapshot")
			}
		})
	}
}

type diagnosticStringer struct{ called *atomic.Bool }

func (s diagnosticStringer) String() string {
	s.called.Store(true)
	panic("must not format arbitrary panic objects")
}

func TestPrivatePanicCaptureAvoidsFormatterCallbacksAndBoundsValues(t *testing.T) {
	var diagnostics rundiagnostics.Buffer
	var called atomic.Bool
	capturePanic(&diagnostics, "worker", diagnosticStringer{called: &called})
	raw, _ := diagnostics.Snapshot()
	if called.Load() || !strings.Contains(string(raw), "diagnosticStringer") {
		t.Fatal("panic capture invoked an arbitrary formatter or lost its type")
	}
	capturePanic(&diagnostics, "worker", strings.Repeat("private", rundiagnostics.MaxBytes))
	raw, _ = diagnostics.Snapshot()
	if len(raw) > rundiagnostics.MaxBytes || !strings.Contains(string(raw), "[value truncated]") {
		t.Fatal("panic value capture exceeded its bound")
	}
}

func TestTerminalFailuresUseAvailabilityCode(t *testing.T) {
	err := terminalError("terminal input failed")
	if coded, ok := err.(*e.Error); !ok || coded.Code != e.CodeUnavailable {
		t.Fatalf("terminal availability failure misclassified: %v", err)
	}
}
