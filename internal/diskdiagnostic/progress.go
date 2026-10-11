// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package diskdiagnostic carries bounded, non-authoritative inspection progress.
// It never carries tool output, guest paths, arguments, credentials or inventory.
package diskdiagnostic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
)

const (
	// Wire ABI emitted by the digest-pinned inspector image. Rename only with
	// a versioned helper protocol and measured producer/consumer repin together.
	Prefix              = "NKD_INSPECTION "
	MaxRecordBytes      = 512
	MaxRetainedBytes    = 32 << 10
	MaxForwardedRecords = 512
	MaxFailureBytes     = 4096
	// MaxParsedRecords bounds JSON work per helper operation, independently of
	// retention. It conservatively covers six helper records and 33 filesystem
	// passes (the ISO plus 32 installer members). Each pass has at most 14
	// filesystem records, eight archive records per root/selected member, a
	// selected-member pair, and two pairs per nested OCI image: transport and scan.
	// One excess member is included for the start emitted before its budget
	// rejection. The SBOM parity test
	// binds these descriptive values to the owning inspection limits.
	MaxParsedRecords = 6 + (1+32)*(14+8*(16384+1)+2*(16384+1)+4*256)
)

// Record is diagnostic only. All text fields have a closed vocabulary.
type Record struct {
	Phase         string `json:"phase"`
	Event         string `json:"event"`
	ElapsedMillis int64  `json:"elapsedMillis"`
	Depth         int    `json:"depth,omitempty"`
	Ordinal       int    `json:"ordinal,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Subject       string `json:"subject,omitempty"`
}

func valid(record Record) bool {
	switch record.Phase {
	case "tool-authentication", "subject-copy", "filesystem-staging", "filesystem-probe", "subject-revalidation", "export-startup", "filesystem-extraction", "file-inventory", "scanner-transport", "root-scan", "member-walk", "nested-scan", "report-completion":
	default:
		return false
	}
	return (record.Event == "start" || record.Event == "complete") && record.ElapsedMillis >= 0 && record.ElapsedMillis <= 24*60*60*1000 && record.Depth >= 0 && record.Depth <= 4 && record.Ordinal >= 0 && record.Ordinal <= 200000 && (record.Kind == "" || validKind(record.Kind)) && (record.Subject == "" || canonical.IsDigest(record.Subject))
}

func validKind(kind string) bool {
	switch kind {
	case "disk-image", "installer-iso", "installer-content", "tarball", "apt-acquisition", "host-package", "oci-image":
		return true
	default:
		return false
	}
}

type contextKey struct{}
type emitter struct {
	mu     sync.Mutex
	writer io.Writer
	start  time.Time
}

// WithWriter installs an operation-local progress channel. Writers must be
// nonblocking owned diagnostic sinks; write errors are intentionally ignored.
// No background goroutine or guest-derived text is introduced.
func WithWriter(ctx context.Context, writer io.Writer) context.Context {
	return context.WithValue(ctx, contextKey{}, &emitter{writer: writer, start: time.Now()})
}

// Emit records an allowlisted phase without changing inspection acceptance.
func Emit(ctx context.Context, phase, event string, depth, ordinal int) {
	if ctx == nil {
		return
	}
	emit, _ := ctx.Value(contextKey{}).(*emitter)
	if emit == nil || emit.writer == nil {
		return
	}
	emit.mu.Lock()
	defer emit.mu.Unlock()
	record := Record{Phase: phase, Event: event, ElapsedMillis: time.Since(emit.start).Milliseconds(), Depth: depth, Ordinal: ordinal}
	if !valid(record) {
		return
	}
	raw, err := json.Marshal(record)
	if err == nil {
		_, _ = fmt.Fprintf(emit.writer, "%s%s\n", Prefix, raw)
	}
}

// Receiver drains helper stderr while accepting only fixed diagnostic records.
// Any malformed, overlong or unrecognized line remains an inspection failure;
// its raw contents are never forwarded or included in the summary.
type Receiver struct {
	mu            sync.Mutex
	collector     *Collector
	line          []byte
	dropping      bool
	unexpected    bool
	last          Record
	haveLast      bool
	observedBytes int
	truncated     bool
	forwarded     int
	now           func() time.Time
	lastForward   time.Time
	lastForwarded Record
	kind, subject string
	failure       []byte
	parsedRecords int
	exhausted     bool
}

func NewReceiver(collector *Collector) *Receiver { return &Receiver{collector: collector} }

// NewSubjectReceiver enriches records with the caller's already-authenticated
// held subject. Descriptive helper fields cannot substitute that binding.
func NewSubjectReceiver(collector *Collector, kind, subject string) *Receiver {
	receiver := NewReceiver(collector)
	if validKind(kind) && canonical.IsDigest(subject) {
		receiver.kind, receiver.subject = kind, subject
	}
	return receiver
}

func (receiver *Receiver) Write(raw []byte) (int, error) {
	receiver.mu.Lock()
	defer receiver.mu.Unlock()
	for _, value := range raw {
		if receiver.exhausted {
			break // Keep draining without framing or parsing the rejected remainder.
		}
		if value == '\n' {
			if receiver.dropping {
				receiver.unexpected = true
			} else {
				receiver.accept(receiver.line)
			}
			receiver.line = receiver.line[:0]
			receiver.dropping = false
			continue
		}
		if !receiver.dropping {
			if len(receiver.line) >= MaxRecordBytes {
				receiver.dropping = true
				receiver.rememberFailure(receiver.line)
				receiver.rememberFailure([]byte{value})
				receiver.line = receiver.line[:0]
			} else {
				receiver.line = append(receiver.line, value)
			}
		} else {
			receiver.rememberFailure([]byte{value})
		}
	}
	return len(raw), nil
}

func (receiver *Receiver) accept(line []byte) {
	if !bytes.HasPrefix(line, []byte(Prefix)) {
		receiver.unexpected = true
		receiver.rememberFailure(append(bytes.Clone(line), '\n'))
		return
	}
	// Charge every prefixed candidate before any JSON pass, including malformed
	// records. Saturate the counter and preserve the last accepted phase; volume
	// rejection must not stop owned child/daemon streams from being drained.
	if receiver.parsedRecords == MaxParsedRecords {
		receiver.exhausted, receiver.unexpected, receiver.truncated = true, true, true
		if receiver.collector != nil {
			receiver.collector.markTruncated()
		}
		return
	}
	receiver.parsedRecords++
	raw := line[len(Prefix):]
	var record Record
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if canonical.ValidateJSONDocument(raw) != nil || decoder.Decode(&record) != nil || !valid(record) {
		receiver.unexpected = true
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields["phase"] == nil || fields["event"] == nil || fields["elapsedMillis"] == nil {
		receiver.unexpected = true
		return
	}
	for _, field := range fields {
		if bytes.Equal(bytes.TrimSpace(field), []byte("null")) {
			receiver.unexpected = true
			return
		}
	}
	// Subject coordinates come from the original held capability, never from
	// helper-authored metadata. Empty receiver metadata is not a subject claim.
	record.Kind, record.Subject = receiver.kind, receiver.subject
	// Re-encode only the allowlisted projection; never forward sender bytes.
	encoded, _ := json.Marshal(record)
	encoded = append(encoded, '\n')
	boundaryChanged := !receiver.haveLast || record.Phase != receiver.last.Phase || record.Event != receiver.last.Event || record.Depth != receiver.last.Depth || record.Ordinal != receiver.last.Ordinal
	receiver.last, receiver.haveLast = record, true
	if receiver.collector != nil {
		receiver.collector.remember(record)
	}
	if len(encoded) > MaxRetainedBytes-receiver.observedBytes {
		receiver.truncated = true
		if receiver.collector != nil {
			receiver.collector.markTruncated()
		}
	} else {
		receiver.observedBytes += len(encoded)
	}
	// Reserve one record for Finish so a capped stream still delivers the
	// actual terminal phase to the persistent diagnostic sink.
	clock := receiver.now
	if clock == nil {
		clock = time.Now
	}
	now := clock()
	if receiver.collector != nil && receiver.forwarded < MaxForwardedRecords-1 && (boundaryChanged || now.Sub(receiver.lastForward) >= 100*time.Millisecond) {
		receiver.collector.append(encoded)
		receiver.forwarded++
		receiver.lastForward = now
		receiver.lastForwarded = record
	} else if receiver.collector != nil {
		receiver.truncated = true
		receiver.collector.markTruncated()
	}
}

// Finish rejects a trailing fragment, even when it resembles a valid record.
func (receiver *Receiver) Finish() {
	receiver.mu.Lock()
	defer receiver.mu.Unlock()
	if len(receiver.line) != 0 || receiver.dropping {
		receiver.unexpected = true
		if !receiver.dropping {
			receiver.rememberFailure(receiver.line)
		}
	}
	if receiver.collector != nil && receiver.haveLast && receiver.last != receiver.lastForwarded && receiver.forwarded < MaxForwardedRecords {
		raw, _ := json.Marshal(receiver.last)
		receiver.collector.append(append(raw, '\n'))
		receiver.lastForwarded = receiver.last
		receiver.forwarded++
	}
}

// FailureBytes returns a bounded raw copy only for the caller's quoted terminal
// error. It is never a retained progress record or inventory. One extra byte
// lets the owning renderer report truncation without retaining an unbounded log.
func (receiver *Receiver) FailureBytes() []byte {
	receiver.mu.Lock()
	defer receiver.mu.Unlock()
	return bytes.Clone(receiver.failure)
}

func (receiver *Receiver) rememberFailure(raw []byte) {
	remaining := MaxFailureBytes + 1 - len(receiver.failure)
	if remaining > 0 {
		receiver.failure = append(receiver.failure, raw[:min(len(raw), remaining)]...)
	}
}

func (receiver *Receiver) Unexpected() bool {
	receiver.mu.Lock()
	defer receiver.mu.Unlock()
	return receiver.unexpected
}

// Summary is an allowlisted projection retained independently of tail/rate caps.
func (receiver *Receiver) Summary() string {
	receiver.mu.Lock()
	defer receiver.mu.Unlock()
	truncated := receiver.truncated
	if receiver.collector != nil {
		truncated = truncated || receiver.collector.isTruncated()
	}
	if !receiver.haveLast {
		return fmt.Sprintf("inspection progress unavailable (unexpectedDiagnostics=%t)", receiver.unexpected)
	}
	return fmt.Sprintf("inspection kind=%s subject=%s phase=%s event=%s elapsedMillis=%d depth=%d ordinal=%d truncated=%t unexpectedDiagnostics=%t", receiver.last.Kind, receiver.last.Subject, receiver.last.Phase, receiver.last.Event, receiver.last.ElapsedMillis, receiver.last.Depth, receiver.last.Ordinal, truncated, receiver.unexpected)
}

// Collector is a package-issued bounded memory sink, not a caller callback.
// Subprocess draining never invokes an arbitrary writer or starts a goroutine.
type Collector struct {
	mu        sync.Mutex
	tail      []byte
	last      []byte
	truncated bool
}

func NewCollector() *Collector { return &Collector{} }

func (collector *Collector) markTruncated() {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	collector.truncated = true
}

func (collector *Collector) isTruncated() bool {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	return collector.truncated
}

func (collector *Collector) remember(record Record) {
	encoded, _ := json.Marshal(record)
	collector.mu.Lock()
	defer collector.mu.Unlock()
	collector.last = append(encoded, '\n')
}

func (collector *Collector) append(raw []byte) {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	for len(collector.tail)+len(raw) > MaxRetainedBytes-MaxRecordBytes {
		collector.truncated = true
		cut := bytes.IndexByte(collector.tail, '\n')
		if cut < 0 {
			collector.tail = nil
			break
		}
		collector.tail = collector.tail[cut+1:]
	}
	collector.tail = append(collector.tail, raw...)
}

// Snapshot copies only sanitized records. The separately retained last record
// survives rate/record/byte limits, including when a helper floods diagnostics.
func (collector *Collector) Snapshot() ([]byte, bool) {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	raw := bytes.Clone(collector.tail)
	if len(collector.last) != 0 && !bytes.HasSuffix(raw, collector.last) {
		raw = append(raw, collector.last...)
	}
	return raw, collector.truncated
}

// LastPhase returns the closed-vocabulary phase only, never helper text.
func (collector *Collector) LastPhase() string {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	var record Record
	if json.Unmarshal(collector.last, &record) != nil {
		return ""
	}
	return record.Phase
}
