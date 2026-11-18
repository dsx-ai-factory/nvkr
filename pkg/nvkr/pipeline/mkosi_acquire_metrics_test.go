// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestMkosiAcquisitionSummaryBoundsAndThroughput(t *testing.T) {
	for _, succeeded := range []bool{false, true} {
		t.Run(fmt.Sprintf("succeeded=%t", succeeded), func(t *testing.T) {
			var log bytes.Buffer
			metrics := newMkosiAcquisitionMetrics(&log)
			for index := 0; index < 12; index++ {
				metrics.record(mkosiInputTiming{path: fmt.Sprintf("runtime/packages/%02d.deb", index), bytes: 100,
					elapsed: time.Duration(index) * time.Second, succeeded: index != 11 || succeeded})
			}
			metrics.report(20*time.Second, succeeded)
			text := log.String()
			if !strings.Contains(text, "body_bytes=1200 elapsed_seconds=20.000 bytes_per_second=60.000") ||
				!strings.Contains(text, fmt.Sprintf("succeeded=%t", succeeded)) || strings.Count(text, "mkosi acquisition slow input:") != 10 {
				t.Fatalf("incorrect summary or throughput: %s", text)
			}
			lines := strings.Split(strings.TrimSpace(text), "\n")
			for index, line := range lines[1:] {
				if !strings.Contains(line, fmt.Sprintf("runtime/packages/%02d.deb", 11-index)) {
					t.Fatalf("slowest-input order changed: %s", line)
				}
			}
		})
	}
}

func TestMkosiAcquisitionSummaryCountsPartialReads(t *testing.T) {
	for _, count := range []int{0, 3, 7} {
		t.Run(fmt.Sprintf("bytes=%d", count), func(t *testing.T) {
			counter := &mkosiDownloadCounter{reader: strings.NewReader("payload")}
			if count != 0 {
				if _, err := counter.Read(make([]byte, count)); err != nil {
					t.Fatal(err)
				}
			}
			var log bytes.Buffer
			metrics := newMkosiAcquisitionMetrics(&log)
			metrics.record(mkosiInputTiming{path: "runtime/packages/input.deb", bytes: counter.bytes, elapsed: time.Second})
			metrics.report(time.Second, false)
			if !strings.Contains(log.String(), fmt.Sprintf("completed=0 body_bytes=%d elapsed_seconds=1.000 bytes_per_second=%d.000", count, count)) {
				t.Fatalf("partial failed response bytes were lost: %s", log.String())
			}
		})
	}
}
