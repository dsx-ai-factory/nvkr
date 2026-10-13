// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package rundiagnostics

import (
	"bytes"
	"sync"
	"testing"
)

func TestBufferRetainsTheNewestBytesAndDrainsOverflow(t *testing.T) {
	for _, test := range []struct {
		name      string
		chunks    [][]byte
		truncated bool
	}{
		{"empty", nil, false},
		{"small", [][]byte{[]byte("stdout"), []byte("stderr")}, false},
		{"exact capacity", [][]byte{bytes.Repeat([]byte("a"), MaxBytes)}, false},
		{"oversized write", [][]byte{bytes.Repeat([]byte("a"), 3*MaxBytes), []byte("final failure")}, true},
		{"many writes", [][]byte{bytes.Repeat([]byte("a"), MaxBytes-5), []byte("first"), []byte("last failure")}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var collector Buffer
			var supplied []byte
			for _, chunk := range test.chunks {
				n, err := collector.Write(chunk)
				if n != len(chunk) || err != nil {
					t.Fatal("diagnostic capture interrupted a producer", n, err)
				}
				supplied = append(supplied, chunk...)
			}
			want := supplied[max(0, len(supplied)-MaxBytes):]
			got, truncated := collector.Snapshot()
			if len(got) > MaxBytes || !bytes.Equal(got, want) || truncated != test.truncated {
				t.Fatal("incorrect tail or truncation", len(got), truncated)
			}
			_, _ = collector.Write(nil)
			_, stillTruncated := collector.Snapshot()
			if stillTruncated != truncated {
				t.Fatal("empty write cleared truncation")
			}
		})
	}
}

func TestBufferOwnsInputsAndDefensiveSnapshots(t *testing.T) {
	var collector Buffer
	input := []byte("private producer bytes")
	_, _ = collector.Write(input)
	input[0] = 'X'
	first, _ := collector.Snapshot()
	first[0] = 'Y'
	second, _ := collector.Snapshot()
	if string(second) != "private producer bytes" {
		t.Fatal("input or reader can mutate retained diagnostics")
	}
}

func TestBufferConcurrentStreamsAndSnapshotsStayBounded(t *testing.T) {
	var collector Buffer
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			chunk := bytes.Repeat([]byte("a"), 1024)
			for range 64 {
				if n, err := collector.Write(chunk); n != len(chunk) || err != nil {
					t.Error("concurrent producer was not drained")
				}
				snapshot, _ := collector.Snapshot()
				if len(snapshot) > MaxBytes {
					t.Error("concurrent snapshot exceeded the bound")
				}
			}
		})
	}
	workers.Wait()
	_, _ = collector.Write([]byte("last failure"))
	got, truncated := collector.Snapshot()
	if len(got) != MaxBytes || !truncated || !bytes.HasSuffix(got, []byte("last failure")) || !bytes.Equal(got[:MaxBytes-len("last failure")], bytes.Repeat([]byte("a"), MaxBytes-len("last failure"))) {
		t.Fatal("concurrent tail was lost or corrupted")
	}
}
