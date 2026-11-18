// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"
)

type mkosiInputTiming struct {
	path      string
	bytes     int64
	elapsed   time.Duration
	succeeded bool
}

// Diagnostics use the local digest-derived input paths, never transport URLs,
// response bodies or redirect locations. They do not enter build identities.
type mkosiAcquisitionMetrics struct {
	mu      sync.Mutex
	log     io.Writer
	records []mkosiInputTiming
}

func newMkosiAcquisitionMetrics(log io.Writer) *mkosiAcquisitionMetrics {
	if log == nil {
		log = io.Discard
	}
	return &mkosiAcquisitionMetrics{log: log}
}

func (metrics *mkosiAcquisitionMetrics) record(record mkosiInputTiming) {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.records = append(metrics.records, record)
}

func (metrics *mkosiAcquisitionMetrics) retry(path string, status, attempt int) {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	_, _ = fmt.Fprintf(metrics.log, "mkosi acquisition retry: input=%q http_status=%d attempt=%d\n", path, status, attempt)
}

// report runs after all request workers have joined, on success and failure.
// Body bytes include partial failed downloads; throughput uses wall time rather
// than the sum of overlapping request durations. Only the ten slowest files emit
// individual records, ordered by elapsed time and then by input path.
func (metrics *mkosiAcquisitionMetrics) report(elapsed time.Duration, succeeded bool) {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	var total int64
	var completed int
	for _, record := range metrics.records {
		total += record.bytes
		if record.succeeded {
			completed++
		}
	}
	var throughput float64
	if elapsed > 0 {
		throughput = float64(total) / elapsed.Seconds()
	}
	_, _ = fmt.Fprintf(metrics.log, "mkosi acquisition summary: files=%d completed=%d body_bytes=%d elapsed_seconds=%.3f bytes_per_second=%.3f succeeded=%t\n",
		len(metrics.records), completed, total, elapsed.Seconds(), throughput, succeeded)
	slices.SortFunc(metrics.records, func(a, b mkosiInputTiming) int {
		if a.elapsed > b.elapsed {
			return -1
		}
		if a.elapsed < b.elapsed {
			return 1
		}
		return strings.Compare(a.path, b.path)
	})
	for _, record := range metrics.records[:min(10, len(metrics.records))] {
		_, _ = fmt.Fprintf(metrics.log, "mkosi acquisition slow input: input=%q body_bytes=%d elapsed_seconds=%.3f succeeded=%t\n",
			record.path, record.bytes, record.elapsed.Seconds(), record.succeeded)
	}
}

type mkosiDownloadCounter struct {
	reader io.Reader
	bytes  int64
}

func (counter *mkosiDownloadCounter) Read(buffer []byte) (int, error) {
	n, err := counter.reader.Read(buffer)
	counter.bytes += int64(n)
	return n, err
}
