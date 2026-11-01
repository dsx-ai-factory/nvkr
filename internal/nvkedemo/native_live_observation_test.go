// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"golang.org/x/sys/unix"
)

const nativeLiveStage = `{"operation":"bootstrap_site","msg":"installing postgres","error":"PRIVATE_ERROR","cluster":"PRIVATE_CLUSTER"}`

type nativeLiveRecorder struct {
	mu      sync.Mutex
	text    bytes.Buffer
	raw     bytes.Buffer
	arrived chan string
}

type nativeLiveBlockedSink struct {
	delayedDiagnosticSink
	raw bytes.Buffer
}

func (sink *nativeLiveBlockedSink) Write(raw []byte) (int, error) {
	if _, err := sink.delayedDiagnosticSink.Write(raw); err != nil {
		return 0, err
	}
	return sink.raw.Write(raw)
}

func (recorder *nativeLiveRecorder) Write(raw []byte) (int, error) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return recorder.raw.Write(raw)
}

func (recorder *nativeLiveRecorder) observeNativeBootstrap(text string) {
	recorder.mu.Lock()
	recorder.text.WriteString(text)
	recorder.mu.Unlock()
	select {
	case recorder.arrived <- text:
	default:
	}
}

func (recorder *nativeLiveRecorder) snapshot() (string, string) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return recorder.text.String(), recorder.raw.String()
}

func TestNativeLiveObservationFramesAndBoundsStayPrivate(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		count     int
	}{
		{"single", nativeLiveStage + "\n", 1},
		{"ordinary stderr then source", "PRIVATE_ERROR\n" + nativeLiveStage + "\n", 1},
		{"malformed then source", "{PRIVATE_INVALID}\n" + nativeLiveStage + "\n", 1},
		{"multiple records", nativeLiveStage + "\r\n" + nativeLiveStage + "\n", 2},
		{"oversized then source", strings.Repeat("x", 2*bufioRecordLimit) + "\n" + nativeLiveStage + "\n", 1},
		{"trailing fragment", nativeLiveStage, 0},
		{"operation bound", strings.Repeat(nativeLiveStage+"\n", 80), 64},
		{"byte bound", strings.Repeat("x\n", diagnosticLimit/2) + nativeLiveStage + "\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observer := &nativeLiveRecorder{}
			projection := newNativeLiveObservation(t.Context(), nativeLiveBootstrap, observer)
			var private bytes.Buffer
			reader := io.Reader(strings.NewReader(tc.raw))
			if tc.name == "single" || tc.name == "multiple records" {
				reader = iotest.OneByteReader(reader)
			}
			if err := projection.drain(t.Context(), &private, reader); err != nil {
				t.Fatal(err)
			}
			text, raw := observer.snapshot()
			if strings.Count(text, "Bootstrap stage observation:") != tc.count || strings.Contains(text, "PRIVATE_") || raw != "" {
				t.Fatalf("unsafe or incorrect projection: %q %q", text, raw)
			}
			if private.String() != tc.raw {
				t.Fatal("private drain dropped or changed source bytes")
			}
		})
	}
}

const bufioRecordLimit = 64 << 10

func TestNativeLiveObservationPreservesReaderFailureAfterDiagnosticLoss(t *testing.T) {
	want := errors.New("private reader failure")
	for _, size := range []int{0, diagnosticLimit + 1} {
		observer := &nativeLiveRecorder{}
		projection := newNativeLiveObservation(t.Context(), nativeLiveBootstrap, observer)
		var private bytes.Buffer
		reader := io.MultiReader(strings.NewReader(strings.Repeat("x", size)), iotest.ErrReader(want))
		if err := projection.drain(t.Context(), &private, reader); !errors.Is(err, want) {
			t.Fatal("diagnostic loss masked existing reader failure", err)
		}
		if private.Len() != size {
			t.Fatal("reader failure changed retained bytes")
		}
	}
}

func TestNativeLiveInstallAcceptsOnlyClosedSourceItems(t *testing.T) {
	for _, tc := range []struct {
		line string
		want bool
	}{
		{"=== item nvke-base-config (template)\n", true},
		{"=== item spire-tpm-pubhashes (manifest)\n", true},
		{"=== item spire-server-nodeport (manifest)\n", true},
		{"=== item spire-oidc-provider-nodeport (manifest)\n", true},
		{"=== item capnico (manifest)\n", true},
		{"=== item nktp (helm)\n", true},
		{"=== item postgresql (helm)\n", true},
		{"=== item spire-crds (helm)\n", true},
		{"=== item temporal (helm)\n", true},
		{"=== item spire (helm)\r\n", true},
		{"=== item controller (helm)\n", true},
		{"=== item controller (manifest)\n", false},
		{"=== item PRIVATE_COMPONENT (helm)\n", false},
		{"=== item controller (helm) PRIVATE_ERROR\n", false},
		{"=== item controller -> release PRIVATE_RELEASE\n", false},
		{nativeLiveStage + "\n", false},
	} {
		t.Run(strings.TrimSpace(tc.line), func(t *testing.T) {
			observer := &nativeLiveRecorder{}
			projection := newNativeLiveObservation(t.Context(), nativeLiveInstall, observer)
			var private bytes.Buffer
			if err := projection.drain(t.Context(), &private, strings.NewReader(tc.line)); err != nil {
				t.Fatal(err)
			}
			text, _ := observer.snapshot()
			if (text != "") != tc.want || strings.Contains(text, "PRIVATE_") || private.String() != tc.line {
				t.Fatal("unadmitted install output or private loss", text)
			}
		})
	}
	if got := newNativeLiveObservation(t.Context(), nativeLiveInstall, &bytes.Buffer{}); got != nil {
		t.Fatal("plain writer admitted for live observations")
	}
}

func TestNativeLiveObservationArrivesBeforeJoinedResult(t *testing.T) {
	for _, kind := range []nativeLiveObservationKind{nativeLiveBootstrap, nativeLiveInstall} {
		for _, mode := range []string{"success", "exit7", "cancel"} {
			t.Run(map[nativeLiveObservationKind]string{nativeLiveBootstrap: "bootstrap", nativeLiveInstall: "install"}[kind]+"/"+mode, func(t *testing.T) {
				root := privateTestDir(t)
				releasePath := filepath.Join(root, "release")
				if err := unix.Mkfifo(releasePath, 0600); err != nil {
					t.Fatal(err)
				}
				release, err := os.OpenFile(releasePath, os.O_RDWR, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer release.Close()
				cli := filepath.Join(root, "fixture")
				writeTenantFixture(t, cli, []byte(`#!/bin/sh
set -eu
printf 'PRIVATE_STDOUT\n=== item controller (helm)\n'
printf '%s\n' '{"operation":"bootstrap_site","msg":"phase finished","phase":"finish","outcome":"succeeded"}'
printf 'PRIVATE_STDERR\n' >&2
printf '%s' '{"operation":"bootstrap_site","msg":"installing ' >&2
printf 'postgres","error":"PRIVATE_ERROR"}\n' >&2
printf '\n=== item nktp (helm)\n' >&2
IFS= read -r token < "$RELEASE"
test "$token" = release
if test "$MODE" = exit7; then exit 7; fi
`), 0700)
				observer := &nativeLiveRecorder{arrived: make(chan string, 8)}
				var originalOutput bytes.Buffer
				runner := Runner{Work: root, Env: []string{"PATH=/usr/bin:/bin", "RELEASE=" + releasePath, "MODE=" + mode}, Progress: observer, output: &originalOutput}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				result := make(chan error, 1)
				go func() {
					if kind == nativeLiveBootstrap {
						result <- runNativeBootstrap(ctx, runner, []string{cli, "bootstrap", "site"})
					} else {
						result <- runNativeSiteInstall(ctx, runner, []string{cli})
					}
				}()
				joined := false
				t.Cleanup(func() {
					cancel()
					if !joined {
						select {
						case <-result:
						case <-time.After(10 * time.Second):
							t.Error("fixture command did not join")
						}
					}
				})
				select {
				case text := <-observer.arrived:
					if !strings.Contains(text, "Live partial source observation") {
						t.Fatal("observation claimed terminal acceptance", text)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("source observation buffered until exit")
				}
				select {
				case err := <-result:
					joined = true
					t.Fatalf("command returned before fixture release: %v", err)
				default:
				}
				if mode == "cancel" {
					cancel()
				} else if _, err := io.WriteString(release, "release\n"); err != nil {
					t.Fatal(err)
				}
				err = <-result
				joined = true
				if mode == "success" && err != nil || mode == "exit7" && CommandExitCode(err) != 7 || mode == "cancel" && !errors.Is(err, context.Canceled) || errors.Is(err, ErrUnjoined) {
					t.Fatalf("original command result changed: %v", err)
				}
				text, raw := observer.snapshot()
				if strings.Contains(text, "PRIVATE_") || strings.Contains(text, "finish succeeded") || strings.Contains(text, "controller (helm)") || raw != "" || originalOutput.Len() != 0 {
					t.Fatal("raw output or stdout record became public", text, raw)
				}
				phase := "host-nvke-bootstrap"
				if kind == nativeLiveInstall {
					phase = "host-nvke-deploy"
				}
				private, readErr := os.ReadFile(filepath.Join(root, phase+".log"))
				if readErr != nil || !bytes.Contains(private, []byte("PRIVATE_STDOUT")) || !bytes.Contains(private, []byte("PRIVATE_STDERR")) {
					t.Fatal("private source evidence lost", readErr)
				}
			})
		}
	}
}

func TestNativeLiveObservationPlainWriterKeepsSourcePrivate(t *testing.T) {
	for _, kind := range []nativeLiveObservationKind{nativeLiveBootstrap, nativeLiveInstall} {
		root := privateTestDir(t)
		cli := filepath.Join(root, "fixture")
		writeTenantFixture(t, cli, []byte("#!/bin/sh\nprintf '%s\\n' 'PRIVATE_STDOUT'\nprintf '%s\\n' '"+nativeLiveStage+"' '=== item nktp (helm)' 'PRIVATE_ERROR' >&2\n"), 0700)
		var public bytes.Buffer
		runner := Runner{Work: root, Env: []string{"PATH=/usr/bin:/bin"}, Progress: &public, output: &public}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		var err error
		if kind == nativeLiveBootstrap {
			err = runNativeBootstrap(ctx, runner, []string{cli, "bootstrap", "site"})
		} else {
			err = runNativeSiteInstall(ctx, runner, []string{cli})
		}
		cancel()
		if err != nil || strings.Contains(public.String(), "PRIVATE_") || strings.Contains(public.String(), "observation:") {
			t.Fatal("plain writer exposed source output", public.String(), err)
		}
	}
}

func TestNativeLiveObservationBlockedPresenterDoesNotDelayCleanup(t *testing.T) {
	root := privateTestDir(t)
	sink := &nativeLiveBlockedSink{delayedDiagnosticSink: delayedDiagnosticSink{entered: make(chan struct{}), release: make(chan struct{})}}
	progress, stop := startNativeE2EProgress(t.Context(), sink, time.Hour)
	var release sync.Once
	unblock := func() { release.Do(func() { close(sink.release) }) }
	t.Cleanup(func() {
		unblock()
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := stop(cleanup); err != nil {
			t.Error(err)
		}
	})
	progress.observeNativeBootstrap("held presenter\n")
	select {
	case <-sink.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("presenter did not reach its blocked sink")
	}
	cli := filepath.Join(root, "fixture")
	writeTenantFixture(t, cli, []byte("#!/bin/sh\ni=0\nwhile test \"$i\" -lt 100; do printf '%s\\n' '"+nativeLiveStage+"' >&2; i=$((i+1)); done\nprintf 'PRIVATE_ERROR\\n' >&2\nexit 7\n"), 0700)
	runner := Runner{Work: root, Env: []string{"PATH=/usr/bin:/bin"}, Progress: progress}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := runNativeBootstrap(ctx, runner, []string{cli, "bootstrap", "site"}); CommandExitCode(err) != 7 || errors.Is(err, ErrUnjoined) {
		t.Fatal("blocked presentation delayed command ownership or changed exit", err)
	}
	if err := runner.Run(ctx, []string{"/bin/sh", "-c", "exit 0"}, "live-observation-cleanup", 0); err != nil {
		t.Fatal("owned cleanup blocked on presentation", err)
	}
	cleanup, cancelCleanup := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancelCleanup()
	if err := stop(cleanup); !errors.Is(err, ErrUnjoined) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("blocked presenter falsely joined", err)
	}
	unblock()
	joined, cancelJoin := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelJoin()
	if err := stop(joined); err != nil {
		t.Fatal("released presenter did not join", err)
	}
	if strings.Contains(sink.raw.String(), "PRIVATE_") {
		t.Fatal("private source data reached presentation")
	}
}

func TestNativeLiveObservationJoinsDescendantHeldPipe(t *testing.T) {
	root := privateTestDir(t)
	observer := &nativeLiveRecorder{arrived: make(chan string, 8)}
	runner := Runner{Work: root, Env: []string{"PATH=/usr/bin:/bin"}, Progress: observer, privateCaptureStdout: true, nativeObservations: nativeLiveBootstrap}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	process, err := runner.start(ctx, []string{"/bin/sh", "-c", "printf '%s\\n' '" + nativeLiveStage + "' >&2; sleep 60 &"}, "live-descendant", true, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := process.Stop(cleanup); err != nil {
			t.Error(err)
		}
	})
	if err := process.waitOwned(ctx); err != nil {
		t.Fatal("descendant-held stderr did not join", err)
	}
	if exited, err := process.Exited(); !exited || err != nil {
		t.Fatal("owned leader/descendants did not finish", exited, err)
	}
	if alive, err := groupAlive(process.PID()); err != nil || alive {
		t.Fatal("owned descendant group remains", alive, err)
	}
	text, raw := observer.snapshot()
	if !strings.Contains(text, "installing postgres") || strings.Contains(text, "PRIVATE_") || raw != "" {
		t.Fatal("joined descendant changed safe projection", text, raw)
	}
}

func TestNativeLiveObservationUnjoinedPrivateDrainRemainsOwned(t *testing.T) {
	root := privateTestDir(t)
	var public bytes.Buffer
	progress, stopProgress := startNativeE2EProgress(t.Context(), &public, time.Hour)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := stopProgress(cleanup); err != nil {
			t.Error(err)
		}
	})
	sink := &delayedDiagnosticSink{entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	unblock := func() { release.Do(func() { close(sink.release) }) }
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	runner := Runner{Work: root, Env: []string{"PATH=/usr/bin:/bin"}, Progress: progress, output: sink, privateCaptureStdout: true, nativeObservations: nativeLiveBootstrap}
	process, err := runner.start(ctx, []string{"/bin/sh", "-c", "printf '%s\\n' '" + nativeLiveStage + "' >&2"}, "live-unjoined-drain", true, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		unblock()
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := process.Stop(cleanup); err != nil {
			t.Error(err)
		}
	})
	select {
	case <-sink.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("private drain did not reach its retained sink")
	}
	cancel()
	// Exercise the existing production stop allowance. The source reader is
	// deliberately still owned; do not inspect its capture or shorten joining.
	if err := process.waitOwned(ctx); !errors.Is(err, ErrUnjoined) || !errors.Is(err, context.Canceled) {
		t.Fatal("incomplete drain lost retention or cancellation", err)
	}
	if exited, _ := process.Exited(); exited {
		t.Fatal("blocked source drain falsely joined")
	}
	// The operation context has ended while its enclosing caller remains live.
	// Stop the real presenter before releasing the retained stderr reader.
	if t.Context().Err() != nil {
		t.Fatal("outer caller ended before retained-drain observation")
	}
	presentation, stopPresentation := context.WithTimeout(t.Context(), 5*time.Second)
	defer stopPresentation()
	if err := stopProgress(presentation); err != nil {
		t.Fatal("presenter did not join before retained reader release", err)
	}
	before := public.String()
	unblock()
	joined, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	if err := process.Stop(joined); err != nil {
		t.Fatal("retained process could not be joined after releasing sink", err)
	}
	if public.String() != before || strings.Contains(public.String(), "Bootstrap stage observation") || strings.Contains(public.String(), "PRIVATE_") {
		t.Fatal("retained reader published after presentation stopped", public.String())
	}
}

func TestNativeLiveObservationStoppedPresenterDoesNotBlockLiveCaller(t *testing.T) {
	var public bytes.Buffer
	progress, stop := startNativeE2EProgress(t.Context(), &public, time.Hour)
	cleanup, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := stop(cleanup); err != nil {
		t.Fatal(err)
	}
	projection := newNativeLiveObservation(t.Context(), nativeLiveBootstrap, progress)
	var private bytes.Buffer
	raw := strings.Repeat(nativeLiveStage+"\n", 80)
	if err := projection.drain(t.Context(), &private, strings.NewReader(raw)); err != nil {
		t.Fatal("late bounded observations could not drain", err)
	}
	if private.String() != raw || public.Len() != 0 {
		t.Fatal("stopped presenter changed private drain or published late output")
	}
}
