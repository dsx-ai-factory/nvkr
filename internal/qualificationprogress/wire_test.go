// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationprogress

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

func progressPipe(t *testing.T) (*os.File, *Sink) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	fd, err := unix.FcntlInt(writer.Fd(), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	sink, err := AdoptFD(t.Context(), fd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close(); _ = sink.Close() })
	return reader, sink
}

func TestProgressSinkAdmissionAndIsolation(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "regular")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, sink := progressPipe(t)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		name string
		ctx  context.Context
		fd   int
	}{
		{"stdout", t.Context(), 1}, {"regular", t.Context(), int(file.Fd())},
		{"reader", t.Context(), int(reader.Fd())}, {"canceled", canceled, sink.fd},
		{"nil context", nil, sink.fd},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := AdoptFD(test.ctx, test.fd)
			var coded *e.Error
			if got != nil || !errors.As(err, &coded) || coded.Code != e.CodeUsage {
				t.Fatalf("invalid descriptor admitted: %v %v", got, err)
			}
		})
	}
	flags, err := unix.FcntlInt(uintptr(sink.fd), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("progress descriptor can leak to signers: %d %v", flags, err)
	}
	flags, err = unix.FcntlInt(uintptr(sink.fd), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_NONBLOCK == 0 {
		t.Fatalf("progress descriptor can block execution: %d %v", flags, err)
	}
}

func TestProgressSinkBackpressureAndInvalidEvents(t *testing.T) {
	for _, mode := range []string{"full", "closed", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			reader, sink := progressPipe(t)
			event := Event{Kind: KindStage, Stage: StageSmoke, State: StateRunning}
			if mode == "closed" {
				_ = reader.Close()
			}
			if mode == "invalid" {
				event.Stage = "private\x1b[2J"
			}
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				for range 10000 {
					sink.Offer(event)
				}
			}()
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("telemetry blocked execution")
			}
			if err := sink.Close(); err != nil {
				t.Fatal(err)
			}
			if mode == "closed" {
				return
			}
			raw, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "invalid" {
				if len(raw) != 0 {
					t.Fatal("invalid event was emitted")
				}
				return
			}
			for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
				if got, err := Decode([]byte(line)); err != nil || got != event {
					t.Fatalf("backpressure emitted a partial frame: %+v %v", got, err)
				}
			}
		})
	}
}

func TestProgressReceiverRejectsMalformedFramesAndPreservesTail(t *testing.T) {
	event := Event{Kind: KindStage, Stage: StageReadback, State: StateCompleted}
	raw, err := canonical.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"private diagnostic\x1b[2J\n", strings.Repeat("x", MaxRecordBytes*2) + "\n",
		strings.Replace(string(raw), `"stage":"readback"`, `"stage":"private-path"`, 1) + "\n",
		strings.TrimSuffix(string(raw), "}") + `,"secret":"private"}` + "\n",
	} {
		t.Run(bad[:min(len(bad), 20)], func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			receiver, err := NewReceiver(t.Context(), reader)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(writer, bad+string(raw)+"\n"+string(raw))
			_ = writer.Close()
			if err := receiver.Finish(t.Context()); err != nil {
				t.Fatal(err)
			}
			got := receiver.Drain()
			if len(got) != 1 || got[0] != event || len(receiver.Drain()) != 0 {
				t.Fatalf("invalid/truncated telemetry accepted or final frame lost: %+v", got)
			}
		})
	}
}

func TestProgressReceiverBoundsAndCancellation(t *testing.T) {
	for _, mode := range []string{"bounded", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			receiver, err := NewReceiver(t.Context(), reader)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "bounded" {
				for i := range maxQueuedEvents + 50 {
					raw, err := canonical.Marshal(Event{Kind: KindStage, Stage: StageBuilder, State: StateRunning, Completed: i, Total: maxQueuedEvents + 50})
					if err != nil {
						t.Fatal(err)
					}
					if _, err := writer.Write(append(raw, '\n')); err != nil {
						t.Fatal(err)
					}
				}
				_ = writer.Close()
			} else {
				cancel()
			}
			if err := receiver.Finish(ctx); err != nil {
				t.Fatal(err)
			}
			if mode == "bounded" {
				got := receiver.Drain()
				if len(got) != maxQueuedEvents || got[0].Completed != 50 || got[len(got)-1].Completed != maxQueuedEvents+49 {
					t.Fatalf("queue bounds/order changed: %+v", got)
				}
			}
		})
	}
}
