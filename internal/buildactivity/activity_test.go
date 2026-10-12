// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildactivity

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

func validRecord() Record {
	return Record{Version: Version, Operation: "nvke-node-image-arm64/build-nvke-node-image", Available: true,
		Files: 1, Bytes: 1024, GrowthObserved: true, GrowthAgeMillis: 100, SampleAgeMillis: 2}
}

func TestRecordClosedSchemaAndBounds(t *testing.T) {
	for _, test := range []struct {
		name  string
		edit  func(*Record)
		valid bool
	}{
		{"valid", func(*Record) {}, true},
		{"longest operation", func(r *Record) { r.Operation = strings.Repeat("a", 256) }, true},
		{"largest atomic frame", func(r *Record) {
			r.Operation, r.Files, r.Bytes = strings.Repeat("a", 256), MaxFiles, MaxBytes
			r.GrowthAgeMillis, r.SampleAgeMillis = MaxAgeMillis, MaxAgeMillis
		}, true},
		{"version", func(r *Record) { r.Version++ }, false},
		{"path", func(r *Record) { r.Operation = "/tmp/secret" }, false},
		{"traversal", func(r *Record) { r.Operation = "a/../b" }, false},
		{"terminal control", func(r *Record) { r.Operation = "a\x1b[2J" }, false},
		{"long operation", func(r *Record) { r.Operation = strings.Repeat("a", 257) }, false},
		{"files", func(r *Record) { r.Files = MaxFiles + 1 }, false},
		{"no files with bytes", func(r *Record) { r.Files = 0 }, false},
		{"no files with growth", func(r *Record) { r.Files, r.Bytes = 0, 0 }, false},
		{"empty directory", func(r *Record) {
			r.Files, r.Bytes, r.GrowthObserved, r.GrowthAgeMillis = 0, 0, false, 0
		}, true},
		{"bytes", func(r *Record) { r.Bytes = MaxBytes + 1 }, false},
		{"negative", func(r *Record) { r.Bytes = -1 }, false},
		{"growth age", func(r *Record) { r.GrowthAgeMillis = MaxAgeMillis + 1 }, false},
		{"sample age", func(r *Record) { r.SampleAgeMillis = -1 }, false},
		{"unavailable residual", func(r *Record) { r.Available = false }, false},
		{"unobserved residual", func(r *Record) { r.GrowthObserved = false }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := validRecord()
			test.edit(&r)
			raw, err := canonical.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if test.valid && len(raw)+1 > maxAtomicBytes {
				t.Fatalf("valid record cannot fit atomic pipe frame: %d", len(raw))
			}
			got, err := Decode(raw)
			if test.valid {
				if err != nil || got != r {
					t.Fatalf("record=%+v err=%v", got, err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeUsage || got != (Record{}) {
				t.Fatalf("invalid diagnostic accepted: %+v %v", got, err)
			}
		})
	}
	base, err := canonical.Marshal(validRecord())
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		string(base) + `{}`, " " + string(base), strings.Replace(string(base), `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(string(base), `"version":1`, `"private_text":"secret","version":1`, 1), strings.Repeat("a", MaxRecordBytes),
	} {
		if _, err := Decode([]byte(raw)); err == nil {
			t.Fatal("ambiguous or oversized frame accepted")
		}
	}
}

func TestSinkFullPipeDropsWholeRecord(t *testing.T) {
	reader, sink := pipeSink(t)
	chunk := []byte(strings.Repeat("x", maxAtomicBytes))
	filled := 0
	for {
		n, err := unix.Write(sink.fd, chunk)
		if n > 0 {
			filled += n
		}
		if errors.Is(err, unix.EAGAIN) {
			break
		}
		if err != nil || n != len(chunk) {
			t.Fatalf("fill pipe: %d %v", n, err)
		}
	}
	// Leave less than one frame free. An atomic offer must drop the complete
	// frame, not write a prefix that corrupts the next receiver observation.
	if n, err := reader.Read(make([]byte, 1)); err != nil || n != 1 {
		t.Fatalf("make bounded room: %d %v", n, err)
	}
	sink.Offer(validRecord())
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(reader)
	if err != nil || len(raw) != filled-1 || string(raw) != strings.Repeat("x", filled-1) {
		t.Fatalf("partial record after backpressure: bytes=%d want=%d err=%v", len(raw), filled-1, err)
	}
}

func TestSinkClosedReaderDoesNotTerminateProcess(t *testing.T) {
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, "-test.run=TestActivityClosedReaderHelper")
	cmd.Env = []string{"NVKR_ACTIVITY_CLOSED_READER_HELPER=1"}
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("closed metadata reader terminated producer: %v %s", err, raw)
	}
}

func TestActivityClosedReaderHelper(t *testing.T) {
	if os.Getenv("NVKR_ACTIVITY_CLOSED_READER_HELPER") != "1" {
		return
	}
	reader, sink := pipeSink(t)
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	sink.Offer(validRecord())
	if sink.fd != -1 {
		t.Fatal("closed pipe was not retired")
	}
}

func pipeSink(t *testing.T) (*os.File, *Sink) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	// The fixture owns writer through os.File; duplicate its descriptor so
	// adopting and closing the inherited handle cannot race its finalizer.
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

func TestSinkNonblockingAndClosedReceiver(t *testing.T) {
	for _, mode := range []string{"full", "closed", "concurrent close", "zero"} {
		t.Run(mode, func(t *testing.T) {
			reader, sink := pipeSink(t)
			if mode == "closed" {
				_ = reader.Close()
			}
			if mode == "zero" {
				sink = &Sink{}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				var workers sync.WaitGroup
				for range 4 {
					workers.Go(func() {
						for range 3000 {
							sink.Offer(validRecord())
						}
					})
				}
				if mode == "concurrent close" {
					_ = sink.Close()
				}
				workers.Wait()
			}()
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("diagnostic pipe blocked producer")
			}
		})
	}
}

func TestSinkAdmitsOnlyPrivateWritablePipes(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "regular")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		name string
		ctx  context.Context
		fd   int
	}{
		{"stdin", t.Context(), 0}, {"regular", t.Context(), int(file.Fd())}, {"read end", t.Context(), int(reader.Fd())},
		{"absent", t.Context(), -1}, {"canceled", canceled, int(writer.Fd())}, {"nil", nil, int(writer.Fd())},
	} {
		t.Run(test.name, func(t *testing.T) {
			sink, err := AdoptFD(test.ctx, test.fd)
			var coded *e.Error
			if sink != nil || !errors.As(err, &coded) || coded.Code != e.CodeUsage {
				t.Fatalf("pipe admission=%v %v", sink, err)
			}
		})
	}
}

func TestSinkDoesNotLeakDescriptorToChild(t *testing.T) {
	_, sink := pipeSink(t)
	flags, err := unix.FcntlInt(uintptr(sink.fd), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("not close-on-exec: %v", err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), program, "-test.run=TestActivityDescriptorHelper")
	cmd.Env = []string{"NVKR_ACTIVITY_DESCRIPTOR_HELPER=1"}
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child descriptor leak: %v %s", err, raw)
	}
}

func TestSinkAdoptionClosesOriginalAndIsolatesRejectedDescriptor(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	fd, err := unix.FcntlInt(writer.Fd(), unix.F_DUPFD, 3)
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
	defer sink.Close()
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
		t.Fatal("original inherited descriptor was not closed")
	}
	// A rejected endpoint also cannot leak into a subsequently launched child.
	rejected, err := unix.FcntlInt(reader.Fd(), unix.F_DUPFD, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(rejected)
	if _, err := AdoptFD(t.Context(), rejected); err == nil {
		t.Fatal("read endpoint was admitted")
	}
	flags, err := unix.FcntlInt(uintptr(rejected), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatal("rejected inherited endpoint remains inheritable")
	}
}

func TestActivityDescriptorHelper(t *testing.T) {
	if os.Getenv("NVKR_ACTIVITY_DESCRIPTOR_HELPER") != "1" {
		return
	}
	for fd := 3; fd < 64; fd++ {
		var stat unix.Stat_t
		if unix.Fstat(fd, &stat) == nil && stat.Mode&unix.S_IFMT == unix.S_IFIFO {
			t.Fatalf("inherited private pipe at FD %d", fd)
		}
	}
}

func TestReceiverBoundsAndJoin(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	receiver, err := NewReceiver(t.Context(), reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := receiver.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	for i := 0; i < maxOperations+20; i++ {
		r := validRecord()
		r.Operation = "operation-" + strings.Repeat("a", i+1)
		raw, err := canonical.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(append(raw, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		receiver.mu.Lock()
		count := len(receiver.known)
		receiver.mu.Unlock()
		if count == maxOperations {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reader did not receive bounded records")
		}
		time.Sleep(time.Millisecond)
	}
	if got := receiver.Drain(); len(got) != maxOperations {
		t.Fatalf("retained %d operations", len(got))
	}
	// A malformed or oversized frame invalidates observations but never carries
	// arbitrary source text into the diagnostic store.
	if _, err := writer.Write([]byte(strings.Repeat("private", 2048) + "\n{\"secret\":true}\n")); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		got := receiver.Drain()
		if len(got) > 0 {
			for _, r := range got {
				if r.Available {
					t.Fatal("malformed frame retained live metadata")
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("malformed stream did not invalidate metadata")
		}
		time.Sleep(time.Millisecond)
	}
	if err := receiver.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-receiver.done:
	default:
		t.Fatal("reader not joined")
	}
}

func TestReceiverReplacementAndEOF(t *testing.T) {
	reader, sink := pipeSink(t)
	receiver, err := NewReceiver(t.Context(), reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = receiver.Close(t.Context()) })
	r := validRecord()
	sink.Offer(r)
	deadline := time.Now().Add(time.Second)
	for {
		got := receiver.Drain()
		if len(got) == 1 {
			if got[0].Bytes != r.Bytes || got[0].Operation != r.Operation || got[0].SampleAgeMillis < r.SampleAgeMillis {
				t.Fatalf("metadata changed: %+v", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("missing record")
		}
		time.Sleep(time.Millisecond)
	}
	_ = sink.Close()
	select {
	case <-receiver.done:
	case <-time.After(time.Second):
		t.Fatal("EOF did not finish receiver")
	}
	got := receiver.Drain()
	if len(got) != 1 || got[0].Available || got[0].Bytes != 0 {
		t.Fatalf("EOF state=%+v", got)
	}
}

func TestReceiverDelayedDrainRetainsAges(t *testing.T) {
	r := validRecord()
	receiver := &Receiver{latest: map[string]receivedRecord{r.Operation: {record: r, received: time.Now().Add(-2 * time.Second)}}}
	got := receiver.Drain()
	if len(got) != 1 || got[0].SampleAgeMillis < 2000+r.SampleAgeMillis || got[0].GrowthAgeMillis != r.GrowthAgeMillis {
		t.Fatalf("delayed metadata falsely fresh: %+v", got)
	}
	receiver.latest[r.Operation] = receivedRecord{record: Record{Version: Version, Operation: r.Operation}, received: time.Now().Add(-time.Hour)}
	got = receiver.Drain()
	if len(got) != 1 || got[0].SampleAgeMillis != 0 || got[0].GrowthAgeMillis != 0 {
		t.Fatalf("unavailable ages nonzero: %+v", got)
	}
}
