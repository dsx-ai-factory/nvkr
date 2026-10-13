// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runpresenter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/gdamore/tcell/v2"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

type fixedSource struct{ state progress.Snapshot }

func (s fixedSource) Snapshot() progress.Snapshot { return s.state }

func fixture() progress.Snapshot {
	return progress.Snapshot{Attempt: "test-attempt", Mode: "live", Phase: "building", Status: "pending", Lane: "static-unqualified", Outputs: []progress.Output{
		{Name: "image", State: "required", Platform: "linux/arm64", Dependencies: []string{"cli"}, Operations: []string{"image/build"}},
		{Name: "cli", State: "observed", Platform: "linux/amd64"},
	}, Operations: []progress.Operation{{ID: "image/build", State: "running", Kind: "build", Outputs: []string{"image"}}}, Activity: []progress.Activity{{Sequence: 1, Text: "operation started"}, {Sequence: 2, Text: "output observed"}}}
}

func sim(t *testing.T, w, h int) tcell.SimulationScreen {
	t.Helper()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	screen.SetSize(w, h)
	t.Cleanup(screen.Fini)
	return screen
}
func text(screen tcell.SimulationScreen) string {
	cells, w, _ := screen.GetContents()
	var b strings.Builder
	for i, c := range cells {
		if i > 0 && i%w == 0 {
			b.WriteByte('\n')
		}
		if len(c.Runes) > 0 {
			for _, r := range c.Runes {
				b.WriteRune(r)
			}
		} else {
			b.WriteByte(' ')
		}
	}
	return b.String()
}
func key(k tcell.Key, r rune) *tcell.EventKey { return tcell.NewEventKey(k, r, tcell.ModNone) }

func TestResponsiveSelectionAndUnicodeSafety(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 16}, {40, 12}, {20, 6}, {0, 0}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			screen := sim(t, size[0], size[1])
			v := NewView(true, true)
			state := fixture()
			state.Outputs[0].Name = "wide界é👩‍💻[red]literal\x1b]52;evil\a\u202e"
			v.Draw(screen, state, time.Now(), false)
			screen.Show()
			if size[0] > 0 && size[1] > 0 {
				got := text(screen)
				if !strings.Contains(got, "NVKR") || !strings.Contains(got, "cancel") {
					t.Fatalf("critical mode/control hidden: %q", got)
				}
				if strings.ContainsRune(got, '\x1b') || strings.ContainsRune(got, '\u202e') {
					t.Fatal("live control rendered")
				}
			}
			screen.SetSize(120, 40)
			v.Draw(screen, state, time.Now(), false)
			screen.Show()
			got := text(screen)
			if !strings.Contains(got, "Outputs observed 1/2") || !strings.Contains(got, "observed") {
				t.Fatal("monochrome state/counter lost")
			}
			v.selected = state.Outputs[0].Name
			v.focus = 1
			v.Draw(screen, state, time.Now(), false)
			screen.Show()
			got = text(screen)
			if !strings.Contains(got, "[red]literal") || !strings.Contains(got, `\x1b`) {
				t.Fatalf("hostile text became markup or lost its safe detail: %q", got)
			}
		})
	}
}

func TestMonochromePanesUseTerminalDefaultForegroundAndBackground(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 16}} {
		for focus := range 3 {
			t.Run(fmt.Sprintf("%dx%d/pane%d", size[0], size[1], focus), func(t *testing.T) {
				screen := sim(t, size[0], size[1])
				view := NewView(true, true)
				view.focus = focus
				view.Draw(screen, fixture(), time.Now(), false)
				screen.Show()
				cells, width, height := screen.GetContents()
				for row := 3; row < height-1; row++ {
					for column := range width {
						foreground, background, _ := cells[row*width+column].Style.Decompose()
						if foreground != tcell.ColorDefault || background != tcell.ColorDefault {
							t.Fatalf("pane forced theme at %d,%d: foreground=%v background=%v", column, row, foreground, background)
						}
					}
				}
				if got := text(screen); !strings.Contains(got, []string{"[1 Outputs]", "[2 Details]", "[3 Activity]"}[focus]) || strings.Contains(got, "[focus]") {
					t.Fatalf("monochrome focus lost: %s", got)
				}
			})
		}
	}
}

func TestFocusedKeysAndLayerPrecedence(t *testing.T) {
	v := NewView(true, true)
	screen := sim(t, 80, 24)
	v.Draw(screen, fixture(), time.Now(), false)
	v.Input(key(tcell.KeyDown, 0))
	if v.selected != "image" {
		t.Fatal("sorted row navigation failed")
	}
	v.Input(key(tcell.KeyEnter, 0))
	v.Draw(screen, fixture(), time.Now(), false)
	v.Input(key(tcell.KeyDown, 0))
	if v.selected != "image" {
		t.Fatal("detail key changed output")
	}
	v.linkIndex = 0
	v.Input(key(tcell.KeyEnter, 0))
	if v.selected != "cli" {
		t.Fatal("dependency was not reachable")
	}
	v.Input(key(tcell.KeyEscape, 0))
	if v.selected != "image" {
		t.Fatal("Back lost identity")
	}
	v.Input(key(tcell.KeyRune, '/'))
	if v.Input(key(tcell.KeyRune, 'q')) != None || v.query != "q" {
		t.Fatal("query q canceled execution")
	}
	if v.Input(key(tcell.KeyCtrlC, 0)) != Cancel {
		t.Fatal("search swallowed Ctrl-C")
	}
	v.Input(key(tcell.KeyEscape, 0))
	v.Input(key(tcell.KeyEscape, 0))
	v.Input(key(tcell.KeyEscape, 0))
	if v.query != "" {
		t.Fatal("unlayered Escape failed to clear search")
	}
	if v.Input(key(tcell.KeyRune, 'q')) != None || !v.confirmQuit || v.Input(key(tcell.KeyRune, 'y')) != Cancel {
		t.Fatal("live q did not require explicit confirmation")
	}
	read := NewView(false, true)
	if read.Input(key(tcell.KeyRune, 'q')) != None || read.Input(key(tcell.KeyRune, 'y')) != Close {
		t.Fatal("viewer q changed owner")
	}
}

func TestQuitConfirmationDefaultsAndModalIsolation(t *testing.T) {
	for _, live := range []bool{true, false} {
		t.Run(fmt.Sprint(live), func(t *testing.T) {
			v := NewView(live, true)
			screen := sim(t, 80, 24)
			v.Draw(screen, fixture(), time.Now(), false)
			selected := v.selected
			if action := v.Input(key(tcell.KeyRune, 'Q')); action != None || !v.confirmQuit {
				t.Fatal("q acted before confirmation")
			}
			for _, event := range []*tcell.EventKey{key(tcell.KeyRune, 'q'), key(tcell.KeyTab, 0), key(tcell.KeyDown, 0), key(tcell.KeyRune, 'f'), key(tcell.KeyRune, '/'), key(tcell.KeyRune, '3'), key(tcell.KeyRune, '?')} {
				if v.Input(event) != None || !v.confirmQuit || v.focus != 0 || v.selected != selected || v.searching || v.help || v.filter != 0 {
					t.Fatal("modal navigation leaked or repeated q confirmed")
				}
			}
			for _, dismiss := range []*tcell.EventKey{key(tcell.KeyRune, 'n'), key(tcell.KeyRune, 'N'), key(tcell.KeyEscape, 0), key(tcell.KeyEnter, 0)} {
				if v.Input(dismiss) != None || v.confirmQuit {
					t.Fatal("default Stay was not safe")
				}
				v.Input(key(tcell.KeyRune, 'q'))
			}
			if v.Input(key(tcell.KeyCtrlC, 0)) != Cancel || v.confirmQuit {
				t.Fatal("modal blocked immediate Ctrl-C")
			}
			v.Input(key(tcell.KeyRune, 'q'))
			want := Close
			if live {
				want = Cancel
			}
			if v.Input(key(tcell.KeyRune, 'Y')) != want || v.confirmQuit {
				t.Fatal("explicit yes did not perform mode-specific action")
			}
		})
	}
}

func TestTabsSeparatorsAndConfirmationContinueDuringResize(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 16}, {20, 6}, {0, 0}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			v := NewView(false, true)
			state := fixture()
			state.Mode = "recorded"
			state.Status = "complete"
			screen := sim(t, size[0], size[1])
			v.Draw(screen, state, time.Now(), false)
			screen.Show()
			if size[0] >= 40 {
				got := text(screen)
				for _, tab := range []string{"[1 Outputs]", "2 Details", "3 Activity"} {
					if !strings.Contains(got, tab) {
						t.Fatalf("tab missing %q: %s", tab, got)
					}
				}
				if !strings.Contains(got, "-----") {
					t.Fatal("pane separation absent")
				}
			}
			v.Input(key(tcell.KeyRune, '2'))
			expectedFocus := 1
			if size[0] < 40 || size[1] < 12 {
				expectedFocus = 0
			}
			if v.focus != expectedFocus {
				t.Fatal("tab shortcut violated visible browsing admission")
			}
			v.Input(key(tcell.KeyBacktab, 0))
			if v.focus != 0 {
				t.Fatal("reverse tab did not restore outputs")
			}
			v.Input(key(tcell.KeyRune, 'q'))
			state.Outputs[0].State = "observed"
			v.Draw(screen, state, time.Now(), false)
			screen.Show()
			if !v.confirmQuit {
				t.Fatal("recorded complete state dismissed viewer confirmation")
			}
			if size[0] > 0 && size[1] > 0 {
				got := text(screen)
				if !strings.Contains(got, "Close") {
					t.Fatalf("confirmation hidden: %s", got)
				}
				if size[0] >= 40 && !strings.Contains(got, "2/2") {
					t.Fatal("modal froze snapshot counts")
				}
			}
			screen.SetSize(80, 24)
			v.Draw(screen, state, time.Now(), false)
			screen.Show()
			if !strings.Contains(text(screen), "Stay (default)") {
				t.Fatal("resized modal lost safe default")
			}
			v.Draw(screen, state, time.Now(), true)
			if v.confirmQuit {
				t.Fatal("external cancellation retained modal")
			}
		})
	}
}

func TestConfirmedCancellationWaitsForJoinedOwner(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	screen := &sessionScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8")}
	started, cleaning, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	screen.onShow = func() {
		once.Do(func() {
			<-started
			_ = screen.PostEvent(key(tcell.KeyRune, 'q'))
			_ = screen.PostEvent(key(tcell.KeyRune, 'y'))
		})
	}
	returned := make(chan Result, 1)
	cause := e.New(e.CodeTimeout, "owning canceled result")
	go func() {
		returned <- Run(ctx, Options{Screen: screen, Live: true}, fixedSource{fixture()}, func(workCtx context.Context) error {
			close(started)
			<-workCtx.Done()
			close(cleaning)
			<-release
			return cause
		})
	}()
	select {
	case <-cleaning:
	case <-ctx.Done():
		t.Fatal("confirmation did not request owner cancellation")
	}
	select {
	case <-returned:
		t.Fatal("confirmed cancellation bypassed join")
	default:
	}
	close(release)
	select {
	case result := <-returned:
		if !errors.Is(result.WorkErr, cause) || !screen.pumpJoined.Load() || !screen.restored.Load() {
			t.Fatal("confirmed cancellation changed owning result or skipped joins")
		}
	case <-ctx.Done():
		t.Fatal("confirmed cancellation never joined")
	}
}

func TestTinyLiveConfirmationRetainsMembershipAndMeaningfulPrompt(t *testing.T) {
	v := NewView(true, true)
	screen := sim(t, 20, 6)
	state := fixture()
	v.Draw(screen, state, time.Now(), false)
	v.Input(key(tcell.KeyRune, 'q'))
	v.Draw(screen, state, time.Now(), false)
	screen.Show()
	got := text(screen)
	for _, wanted := range []string{"1/2 observed", "Cancel Run?", "y / N stay", "[1 O]"} {
		if !strings.Contains(got, wanted) {
			t.Fatalf("tiny confirmation lost %q: %s", wanted, got)
		}
	}
	if v.Input(key(tcell.KeyEnter, 0)) != None || v.confirmQuit {
		t.Fatal("tiny confirmation Enter did not stay")
	}
}

func TestOwnerCompletionAndExternalCancelWinOpenConfirmation(t *testing.T) {
	for _, mode := range []string{"complete", "external-cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			screen := &sessionScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8")}
			started, release := make(chan struct{}), make(chan struct{})
			shows := 0
			finalFrame := ""
			screen.onShow = func() {
				shows++
				finalFrame = text(screen.SimulationScreen)
				if shows == 1 {
					<-started
					_ = screen.PostEvent(key(tcell.KeyRune, 'q'))
				}
				if strings.Contains(finalFrame, "Cancel the current Run?") {
					if mode == "external-cancel" {
						cancel()
					} else {
						close(release)
					}
				}
			}
			result := Run(ctx, Options{Screen: screen, Live: true}, fixedSource{fixture()}, func(workCtx context.Context) error {
				close(started)
				if mode == "complete" {
					select {
					case <-release:
						return nil
					case <-workCtx.Done():
						return canceled(workCtx.Err())
					}
				}
				<-workCtx.Done()
				return canceled(workCtx.Err())
			})
			if strings.Contains(finalFrame, "Cancel the current Run?") {
				t.Fatal("joined completion required approval")
			}
			if mode == "complete" && result.WorkErr != nil {
				t.Fatalf("modal changed completed owning result: %v", result.WorkErr)
			}
			if mode == "external-cancel" && result.WorkErr == nil {
				t.Fatal("modal blocked external cancellation")
			}
		})
	}
}

func TestConcurrentStateKeepsSelectionAndHistoryAnchor(t *testing.T) {
	v := NewView(true, true)
	screen := sim(t, 80, 24)
	state := fixture()
	v.Draw(screen, state, time.Now(), false)
	v.selected = "image"
	v.filter = 1
	state.Outputs[0].State = "observed"
	v.Draw(screen, state, time.Now(), false)
	if v.selected != "image" {
		t.Fatal("telemetry changed pinned filtered identity")
	}
	v.focus = 1
	v.Draw(screen, state, time.Now(), false)
	screen.Show()
	if !strings.Contains(text(screen), "Outside current filter") {
		t.Fatal("hidden pinned selection was ambiguous")
	}
	v.focus = 2
	v.rows = 1
	v.activityStart = 0
	v.follow = false
	v.anchor = 1
	for i := 3; i < 50; i++ {
		state.Activity = append(state.Activity, progress.Activity{Sequence: uint64(i), Text: "new entry"})
	}
	v.Draw(screen, state, time.Now(), false)
	if v.activityStart != 0 || v.anchor != 1 || v.follow {
		t.Fatal("new entries stole scroll")
	}
	state.Activity = state.Activity[10:]
	state.Dropped = 10
	v.Draw(screen, state, time.Now(), false)
	screen.Show()
	if !strings.Contains(text(screen), "evicted") {
		t.Fatal("eviction silently changed history")
	}
	v.Input(key(tcell.KeyEnd, 0))
	if !v.follow {
		t.Fatal("End failed to resume")
	}
}

func TestExplicitFilterAndSearchChooseVisibleSelection(t *testing.T) {
	v := NewView(true, true)
	screen := sim(t, 80, 24)
	state := fixture()
	v.Draw(screen, state, time.Now(), false)
	if v.selected != "cli" {
		t.Fatal("initial stable ordering changed")
	}
	v.Input(key(tcell.KeyRune, 'f'))
	if v.selected != "image" || len(v.visible) != 1 {
		t.Fatal("explicit Active filter retained hidden observed output")
	}
	state.Outputs[0].State = "observed"
	v.Draw(screen, state, time.Now(), false)
	if v.selected != "image" {
		t.Fatal("telemetry stole pinned output")
	}
	v.Input(key(tcell.KeyEscape, 0))
	v.Input(key(tcell.KeyRune, '/'))
	v.Input(key(tcell.KeyRune, 'c'))
	v.Input(key(tcell.KeyEnter, 0))
	if v.selected != "cli" {
		t.Fatal("applied search did not select its visible match")
	}
	v.Input(key(tcell.KeyRune, '/'))
	v.Input(key(tcell.KeyRune, 'z'))
	v.Input(key(tcell.KeyEnter, 0))
	if v.selected != "" || len(v.visible) != 0 {
		t.Fatal("empty search retained invisible selection")
	}
}

func TestDependencyRoundTripsKeepBoundedRecentBackHistory(t *testing.T) {
	v := NewView(true, true)
	screen := sim(t, 80, 24)
	state := fixture()
	v.selected = "image"
	v.focus = 1
	for range 1000 {
		v.Draw(screen, state, time.Now(), false)
		v.linkIndex = 1
		v.Input(key(tcell.KeyEnter, 0))
		if v.operation != "image/build" {
			t.Fatal("operation link failed")
		}
		v.Draw(screen, state, time.Now(), false)
		v.linkIndex = 0
		v.Input(key(tcell.KeyEnter, 0))
		if v.selected != "image" || v.operation != "" {
			t.Fatal("output link failed")
		}
		if len(v.back) > maxBackHistory {
			t.Fatal("navigation history grew without bound")
		}
	}
	if len(v.back) != maxBackHistory {
		t.Fatal("recent navigation history was discarded")
	}
	v.Input(key(tcell.KeyEscape, 0))
	if v.operation != "image/build" {
		t.Fatal("bounded Back lost most recent operation")
	}
	v.Input(key(tcell.KeyEscape, 0))
	if v.selected != "image" || v.operation != "" {
		t.Fatal("bounded Back lost preceding output")
	}
}

type sessionScreen struct {
	tcell.SimulationScreen
	onShow     func()
	panicDraw  bool
	closed     bool
	initFail   bool
	restored   atomic.Bool
	pumpJoined atomic.Bool
}

func (s *sessionScreen) Init() error {
	if s.initFail {
		return errors.New("private init detail")
	}
	if err := s.SimulationScreen.Init(); err != nil {
		return err
	}
	s.SetSize(80, 24)
	return nil
}
func (s *sessionScreen) Clear() {
	if s.panicDraw {
		panic("private renderer detail")
	}
	s.SimulationScreen.Clear()
}
func (s *sessionScreen) Fini() { s.restored.Store(true); s.SimulationScreen.Fini() }
func (s *sessionScreen) Show() {
	s.SimulationScreen.Show()
	if s.onShow != nil {
		s.onShow()
	}
}
func (s *sessionScreen) ChannelEvents(events chan<- tcell.Event, quit <-chan struct{}) {
	defer s.pumpJoined.Store(true)
	if s.closed {
		close(events)
		return
	}
	s.SimulationScreen.ChannelEvents(events, quit)
}

func TestSessionWorkerResultAndAdmission(t *testing.T) {
	cause := e.New(e.CodeFetchFailed, "owning failure")
	for _, mode := range []string{"success", "owner-failure", "owner-panic", "initialization"} {
		t.Run(mode, func(t *testing.T) {
			screen := &sessionScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8"), initFail: mode == "initialization"}
			started := false
			result := Run(t.Context(), Options{Screen: screen, Live: true}, fixedSource{fixture()}, func(context.Context) error {
				started = true
				if mode == "owner-panic" {
					panic("private worker text")
				}
				if mode == "owner-failure" {
					return cause
				}
				return nil
			})
			if mode == "initialization" {
				if started || result.PresentationErr == nil {
					t.Fatal("failed terminal started work")
				}
				return
			}
			if !screen.restored.Load() || !screen.pumpJoined.Load() {
				t.Fatal("terminal/pump not joined")
			}
			if mode == "success" && (result.WorkErr != nil || result.PresentationErr != nil) {
				t.Fatalf("success invented error: %+v", result)
			}
			if mode == "owner-failure" && !errors.Is(result.WorkErr, cause) {
				t.Fatal("owning result lost")
			}
			if mode == "owner-panic" && (result.WorkErr == nil || strings.Contains(result.WorkErr.Error(), "private")) {
				t.Fatal("worker panic escaped")
			}
		})
	}
}

func TestSessionFailureRestoresBeforeJoinedCleanup(t *testing.T) {
	for _, mode := range []string{"renderer", "closed-stream", "input", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			screen := &sessionScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8"), panicDraw: mode == "renderer", closed: mode == "closed-stream"}
			started, cleaning, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			screen.onShow = func() {
				once.Do(func() {
					<-started
					if mode == "input" {
						_ = screen.PostEvent(tcell.NewEventError(errors.New("private terminal error")))
					}
					if mode == "cancel" {
						_ = screen.PostEvent(key(tcell.KeyCtrlC, 0))
					}
				})
			}
			cause := e.New(e.CodeFetchFailed, "cleanup failure")
			returned := make(chan Result, 1)
			go func() {
				returned <- Run(ctx, Options{Screen: screen, Live: true}, fixedSource{fixture()}, func(workCtx context.Context) error {
					close(started)
					<-workCtx.Done()
					close(cleaning)
					<-release
					return cause
				})
			}()
			select {
			case <-cleaning:
			case <-ctx.Done():
				t.Fatal("failure did not cancel owner")
			}
			select {
			case <-returned:
				t.Fatal("returned before owner cleanup joined")
			default:
			}
			if mode != "cancel" {
				deadline := time.Now().Add(time.Second)
				for !screen.restored.Load() && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if !screen.restored.Load() {
					t.Fatal("terminal stayed raw during failed renderer cleanup")
				}
			}
			close(release)
			select {
			case result := <-returned:
				if !errors.Is(result.WorkErr, cause) {
					t.Fatal("cleanup error lost")
				}
				if mode != "cancel" && (result.PresentationErr == nil || strings.Contains(result.PresentationErr.Error(), "private")) {
					t.Fatal("presentation failure lost/leaked")
				}
			case <-ctx.Done():
				t.Fatal("join did not return")
			}
			if !screen.pumpJoined.Load() || !screen.restored.Load() {
				t.Fatal("terminal not joined")
			}
		})
	}
}

func TestReadOnlyCloseAndCancel(t *testing.T) {
	for _, k := range []*tcell.EventKey{key(tcell.KeyRune, 'q'), key(tcell.KeyCtrlC, 0)} {
		screen := &sessionScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8")}
		var once sync.Once
		screen.onShow = func() {
			once.Do(func() {
				_ = screen.PostEvent(k)
				if k.Rune() == 'q' {
					_ = screen.PostEvent(key(tcell.KeyRune, 'y'))
				}
			})
		}
		result := Run(t.Context(), Options{Screen: screen}, fixedSource{fixture()}, nil)
		if k.Key() == tcell.KeyCtrlC && result.WorkErr == nil {
			t.Fatal("Ctrl-C lacked timeout")
		}
		if k.Rune() == 'q' && (result.WorkErr != nil || result.PresentationErr != nil) {
			t.Fatal("inspection close failed")
		}
	}
}

type shortTTY struct{ tcell.Tty }

func (shortTTY) Write([]byte) (int, error) { return 0, nil }
func TestCheckedTTYFailureIsPrivateAndCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	tty := &checkedTTY{Tty: shortTTY{}, cancel: cancel}
	_, err := tty.Write([]byte("bytes"))
	if !errors.Is(err, io.ErrShortWrite) || tty.failure() == nil || ctx.Err() == nil {
		t.Fatal("short write not checked")
	}
}

func TestMonitorFloodDoesNotWaitForRendering(t *testing.T) {
	state := fixture()
	state.Operations = nil
	state.Outputs[0].Operations = nil
	for i := 0; i < 2000; i++ {
		id := fmt.Sprintf("operation-%04d", i)
		state.Operations = append(state.Operations, progress.Operation{ID: id, State: "queued", Outputs: []string{"image"}})
		state.Outputs[0].Operations = append(state.Outputs[0].Operations, id)
	}
	monitor := progress.New(state)
	var wg sync.WaitGroup
	for worker := range 4 {
		wg.Go(func() {
			for index := range 500 {
				id := fmt.Sprintf("operation-%04d", worker*500+index)
				monitor.SetOperation(id, "running", "")
				monitor.SetOperation(id, "completed", "")
			}
		})
	}
	screen := sim(t, 80, 24)
	v := NewView(true, true)
	for range 100 {
		v.Draw(screen, monitor.Snapshot(), time.Now(), false)
	}
	wg.Wait()
	if snapshot := monitor.Snapshot(); snapshot.Dropped == 0 || len(snapshot.Activity) > progress.MaxActivity {
		t.Fatal("flood did not exercise bounded history overflow")
	}
}

func TestOutputProducerSummaryDoesNotMintObservations(t *testing.T) {
	for _, opState := range []string{"queued", "running", "completed", "failed"} {
		t.Run(opState, func(t *testing.T) {
			state := fixture()
			state.Operations[0].State = opState
			state.Operations[0].Code = string(e.CodeFetchFailed)
			v := NewView(true, true)
			screen := sim(t, 80, 24)
			v.Draw(screen, state, time.Now(), false)
			screen.Show()
			wanted := map[string]string{"queued": "Waiting", "running": "Building", "completed": "Check pending", "failed": "Producer failed"}[opState]
			if !strings.Contains(text(screen), wanted) || !strings.Contains(text(screen), "Outputs observed 1/2") {
				t.Fatal("producer description hid work or minted observed membership")
			}
			if opState == "failed" {
				v.filter = 2
				v.Draw(screen, state, time.Now(), false)
				if len(v.visible) != 1 || v.visible[0].Name != "image" {
					t.Fatal("failed producer output missing from Failed filter")
				}
				v.operation = "image/build"
				v.focus = 1
				v.Draw(screen, state, time.Now(), false)
				screen.Show()
				if !strings.Contains(text(screen), string(e.CodeFetchFailed)) {
					t.Fatal("causal operation code hidden")
				}
			}
		})
	}
}

func TestFailedRunDoesNotInventSiblingOutputFailure(t *testing.T) {
	state := fixture()
	state.Status = "failed"
	state.FailureCode = string(e.CodeFetchFailed)
	state.Outputs[0].State = "required"
	state.Outputs[1].State = "required"
	state.Outputs[1].Operations = []string{"cli/build"}
	state.Operations[0].State = "failed"
	state.Operations = append(state.Operations, progress.Operation{ID: "cli/build", State: "completed", Kind: "build", Outputs: []string{"cli"}})
	v := NewView(true, true)
	screen := sim(t, 80, 24)
	v.Draw(screen, state, time.Now(), false)
	screen.Show()
	got := text(screen)
	for _, wanted := range []string{"Outputs observed 0/2", "Producer failed  image", "Not retained  cli"} {
		if !strings.Contains(got, wanted) {
			t.Fatalf("failed Run presentation lost %q: %s", wanted, got)
		}
	}
	v.selected = "cli"
	v.focus = 1
	v.Draw(screen, state, time.Now(), false)
	if got := strings.Join(v.detailLines(time.Now()), "\n"); !strings.Contains(got, "No observation in final Run record") || strings.Contains(got, "corrupt") || strings.Contains(got, "missing artifact") {
		t.Fatalf("sibling loss was misattributed: %s", got)
	}
	state.Operations[1].State = "canceled"
	v.Draw(screen, state, time.Now(), false)
	label, stage, failed := v.outputSummary(state.Outputs[1])
	if label != "Producer canceled" || stage != "cli/build" || failed {
		t.Fatal("exact canceled sibling was conflated with causal failure or unobserved membership")
	}
}

func TestSelectedRowsRemainVisibleAcrossPaging(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {60, 16}, {180, 40}} {
		screen := sim(t, size[0], size[1])
		state := fixture()
		state.Outputs = nil
		for i := 0; i < 40; i++ {
			state.Outputs = append(state.Outputs, progress.Output{Name: fmt.Sprintf("output-%02d", i), State: "required"})
		}
		v := NewView(true, true)
		for range 40 {
			v.Draw(screen, state, time.Now(), false)
			screen.Show()
			if !strings.Contains(text(screen), "> Waiting  "+v.selected) {
				t.Fatalf("selected row clipped at %v: %s", size, v.selected)
			}
			v.Input(key(tcell.KeyDown, 0))
		}
	}
}

func TestSuccessfulOwnerSurvivesLateCancellationAndFinalRendererFailure(t *testing.T) {
	for _, mode := range []string{"late-q", "final-render"} {
		screen := &sessionScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8")}
		started, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		screen.onShow = func() {
			once.Do(func() {
				<-started
				if mode == "late-q" {
					_ = screen.PostEvent(key(tcell.KeyRune, 'q'))
				} else {
					screen.panicDraw = true
				}
				close(release)
			})
		}
		result := Run(t.Context(), Options{Screen: screen, Live: true}, fixedSource{fixture()}, func(context.Context) error { close(started); <-release; return nil })
		if result.WorkErr != nil {
			t.Fatalf("owner success rewritten: %v", result.WorkErr)
		}
		if mode == "final-render" && result.PresentationErr == nil {
			t.Fatal("final renderer error lost")
		}
	}
}

func TestUnavailableRecordDoesNotInventMembership(t *testing.T) {
	state := fixture()
	state.Mode = "recorded"
	state.Status = "unavailable"
	state.RecordAvailable = false
	v := NewView(false, true)
	screen := sim(t, 80, 24)
	v.Draw(screen, state, time.Now(), false)
	screen.Show()
	got := text(screen)
	if !strings.Contains(got, "membership unavailable") || strings.Contains(got, "Outputs observed 1/2") {
		t.Fatal("missing record manufactured membership")
	}
}

func TestJoinedFinalFrameUsesOwnerOutcomeAfterCancellation(t *testing.T) {
	for _, status := range []string{"complete", "failed"} {
		t.Run(status, func(t *testing.T) {
			monitor := progress.New(fixture())
			screen := &sessionScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8")}
			started := make(chan struct{})
			var once sync.Once
			var finalFrame string
			screen.onShow = func() {
				finalFrame = text(screen.SimulationScreen)
				once.Do(func() { <-started; _ = screen.PostEvent(key(tcell.KeyCtrlC, 0)) })
			}
			cause := e.New(e.CodeFetchFailed, "original owning failure")
			result := Run(t.Context(), Options{Screen: screen, Live: true}, monitor, func(ctx context.Context) error {
				close(started)
				<-ctx.Done()
				if status == "complete" {
					monitor.Finish("complete", "static-unqualified", []string{"cli", "image"}, "", true, false)
					return nil
				}
				monitor.Finish("failed", "static-unqualified", nil, string(e.CodeFetchFailed), true, false)
				return cause
			})
			if strings.Contains(finalFrame, "Canceling") || strings.Contains(finalFrame, "waiting for owned work") {
				t.Fatalf("joined owner still displayed as waiting: %s", finalFrame)
			}
			if !strings.Contains(finalFrame, status) {
				t.Fatalf("final owner outcome missing: %s", finalFrame)
			}
			if status == "failed" && !errors.Is(result.WorkErr, cause) {
				t.Fatal("final frame changed original coded failure")
			}
			if status == "complete" && result.WorkErr != nil {
				t.Fatal("joined success rewritten")
			}
		})
	}
}

func TestLiveUnavailableMembershipKeepsFrozenRequiredCount(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {20, 6}} {
		state := fixture()
		state.Status = "unavailable"
		state.RecordAvailable = false
		v := NewView(true, true)
		screen := sim(t, size[0], size[1])
		v.Draw(screen, state, time.Now(), false)
		screen.Show()
		got := text(screen)
		if !strings.Contains(got, "Required 2") || strings.Contains(got, "0/2 observed") || strings.Contains(got, "Outputs observed") {
			t.Fatalf("frozen requirement lost or observed membership invented: %q", got)
		}
	}
}

// This opt-in uses no privileged operation, build, deployment or external owner.
// Simulation tests cannot establish restoration of real stdio file flags/termios.
func TestPresenterRealTTYRestoresFlags(t *testing.T) {
	if os.Getenv("NVKR_TEST_RUNPRESENTER_TTY") != "1" {
		t.Skip("requires an isolated actual terminal")
	}
	if !Eligible(os.Stdout) {
		t.Fatal("actual stdio terminal required")
	}
	fd := os.Stdin.Fd()
	beforeFlags, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	beforeState, err := term.GetState(int(fd))
	if err != nil {
		t.Fatal(err)
	}
	result := Run(t.Context(), Options{Live: true, NoColor: true}, fixedSource{fixture()}, func(ctx context.Context) error {
		timer := time.NewTimer(50 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return canceled(ctx.Err())
		case <-timer.C:
			return nil
		}
	})
	if result.WorkErr != nil || result.PresentationErr != nil {
		t.Fatalf("real terminal session failed: %+v", result)
	}
	afterFlags, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
	// Darwin reports kernel write history through F_GETFL when stdin/stdout
	// share their open description. FWASWRITTEN (0x10000) is not in FCNTLFLAGS;
	// F_SETFL cannot revoke it. Compare every other bit, including O_NONBLOCK.
	// Sources: apple-oss-distributions/xnu, bsd/sys/fcntl.h (FWASWRITTEN,
	// FCNTLFLAGS) and bsd/kern/kern_descrip.c (F_GETFL/F_SETFL preservation).
	// https://github.com/apple-oss-distributions/xnu/blob/main/bsd/sys/fcntl.h
	allowedHistory := 0
	if runtime.GOOS == "darwin" {
		allowedHistory = 0x10000
	}
	if err != nil || (beforeFlags^afterFlags)&^allowedHistory != 0 {
		t.Fatalf("stdio flags changed %d -> %d: %v", beforeFlags, afterFlags, err)
	}
	afterState, err := term.GetState(int(fd))
	if err != nil || !reflect.DeepEqual(beforeState, afterState) {
		t.Fatalf("termios not restored: %v; before=%+v after=%+v", err, beforeState, afterState)
	}
}

// Scoped signal exercise on actual stdio; distinct from disconnecting a PTY
// master. A failed scoped handler terminates this isolated test process.
func TestPresenterRealTTYHangupJoinsOwner(t *testing.T) {
	if os.Getenv("NVKR_TEST_RUNPRESENTER_HUP") != "1" {
		t.Skip("requires isolated real terminal and signal opt-in")
	}
	if !Eligible(os.Stdout) {
		t.Fatal("actual stdio terminal required")
	}
	var joined atomic.Bool
	result := Run(t.Context(), Options{Live: true, NoColor: true}, fixedSource{fixture()}, func(ctx context.Context) error {
		process, err := os.FindProcess(os.Getpid())
		if err != nil {
			return err
		}
		if err := process.Signal(syscall.SIGHUP); err != nil {
			return err
		}
		<-ctx.Done()
		// Another hangup during owner cleanup still cannot force process exit.
		if err := process.Signal(syscall.SIGHUP); err != nil {
			return err
		}
		joined.Store(true)
		return canceled(ctx.Err())
	})
	var coded *e.Error
	if !joined.Load() || !errors.As(result.WorkErr, &coded) || coded.Code != e.CodeTimeout {
		t.Fatalf("hangup bypassed joined cancellation: %+v", result)
	}
}

func TestTinyMembershipAndDetailIdentity(t *testing.T) {
	state := fixture()
	state.PlanDigest = "sha256:" + strings.Repeat("a", 64)
	state.FailureCode = string(e.CodeFetchFailed)
	v := NewView(true, true)
	screen := sim(t, 20, 6)
	v.Draw(screen, state, time.Now(), false)
	screen.Show()
	if got := text(screen); !strings.Contains(got, "1/2 observed") || !strings.Contains(got, "cancel") || !strings.Contains(got, "Code:") {
		t.Fatalf("tiny important fields hidden: %q", got)
	}
	screen.SetSize(80, 24)
	v.focus = 1
	v.Draw(screen, state, time.Now(), false)
	lines := strings.Join(v.detailLines(time.Now()), "\n")
	if !strings.Contains(lines, state.PlanDigest) || strings.Contains(lines, "Activity sample:") || strings.Contains(lines, "Diagnostic reference:") {
		t.Fatal("details lost exact identity or added unsupported telemetry placeholders")
	}
}
