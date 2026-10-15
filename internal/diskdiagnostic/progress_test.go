// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package diskdiagnostic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReceiverOnlyAcceptsAllowlistedRecords(t *testing.T) {
	valid := Prefix + `{"phase":"root-scan","event":"start","elapsedMillis":123}` + "\n"
	for _, test := range []struct {
		name, input string
		rejected    bool
	}{
		{"valid", valid, false},
		{"pinned image literal", "NKD_INSPECTION " + `{"phase":"root-scan","event":"start","elapsedMillis":123}` + "\n", false},
		{"renamed protocol", "NVKR_INSPECTION " + `{"phase":"root-scan","event":"start","elapsedMillis":123}` + "\n", false},
		{"unknown protocol", "NKR_INSPECTION " + `{"phase":"root-scan","event":"start","elapsedMillis":123}` + "\n", true},
		{"warning", "scanner warning token=secret\n", true},
		{"unknown phase", strings.Replace(valid, "root-scan", "password=secret", 1), true},
		{"unknown event", strings.Replace(valid, "start", "secret", 1), true},
		{"unknown field", Prefix + `{"phase":"root-scan","event":"start","elapsedMillis":1,"secret":"token"}` + "\n", true},
		{"duplicate field", Prefix + `{"phase":"root-scan","phase":"nested-scan","event":"start","elapsedMillis":1}` + "\n", true},
		{"negative elapsed", strings.Replace(valid, "123", "-1", 1), true},
		{"installer nested depth", Prefix + `{"phase":"nested-scan","event":"start","elapsedMillis":1,"depth":4}` + "\n", false},
		{"excessive depth", Prefix + `{"phase":"nested-scan","event":"start","elapsedMillis":1,"depth":5}` + "\n", true},
		{"missing elapsed", Prefix + `{"phase":"root-scan","event":"start"}` + "\n", true},
		{"null elapsed", strings.Replace(valid, "123", "null", 1), true},
		{"huge elapsed", strings.Replace(valid, "123", "9223372036854775808", 1), true},
		{"trailing fragment", strings.TrimSuffix(valid, "\n"), true},
		{"control injection", strings.Replace(valid, "root-scan", `root-scan\n::warning::secret`, 1), true},
		{"oversized line", strings.Repeat("secret", MaxRecordBytes) + "\n", true},
		{"two json objects", strings.TrimSuffix(valid, "\n") + "{}\n", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			collector := NewCollector()
			receiver := NewReceiver(collector)
			for _, value := range []byte(test.input) {
				if count, err := receiver.Write([]byte{value}); count != 1 || err != nil {
					t.Fatal(count, err)
				}
			}
			receiver.Finish()
			if receiver.Unexpected() != test.rejected {
				t.Fatalf("rejected=%t want%t", receiver.Unexpected(), test.rejected)
			}
			forwarded, _ := collector.Snapshot()
			if test.rejected && len(forwarded) != 0 {
				t.Fatalf("untrusted diagnostics forwarded: %q", forwarded)
			}
			if strings.Contains(receiver.Summary(), "secret") || bytes.Contains(forwarded, []byte("secret")) {
				t.Fatal("secret escaped sanitized boundary")
			}
		})
	}
}

func TestInspectionProgressChild(t *testing.T) {
	if os.Getenv("NVKR_INSPECTION_PROGRESS_TEST_CHILD") != "1" {
		return
	}
	ctx := WithWriter(context.Background(), os.Stderr)
	Emit(ctx, "nested-scan", "start", 1, 17)
	if os.Getenv("NVKR_INSPECTION_PROGRESS_TEST_VOLUME") == "1" {
		Emit(ctx, "report-completion", "complete", 0, 0)
	}
	time.Sleep(time.Hour)
}

// A real child writes its progress pipe before blocking. CommandContext owns
// killing and joining it; the receiver still retains the last sanitized phase.
func TestNativeChildCancellationKeepsProgressAndJoins(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	receiver := NewReceiver(nil)
	command := exec.CommandContext(ctx, executable, "-test.run=^TestInspectionProgressChild$")
	command.Env = append(os.Environ(), "NVKR_INSPECTION_PROGRESS_TEST_CHILD=1")
	command.Stderr = receiver
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	accepted := waitForChildInspectionProgress(ctx, receiver, false)
	// Cancel only after the receiver accepts progress; the outer deadline is a
	// startup safety bound, not an assumption about process initialization speed.
	cancel()
	err = command.Wait()
	receiver.Finish()
	if !accepted || err == nil || ctx.Err() != context.Canceled || command.ProcessState == nil || receiver.Unexpected() || !strings.Contains(receiver.Summary(), "phase=nested-scan") || !strings.Contains(receiver.Summary(), "ordinal=17") {
		t.Fatalf("child was not joined with its safe last phase: err=%v summary=%s", err, receiver.Summary())
	}
}

func waitForChildInspectionProgress(ctx context.Context, receiver *Receiver, requireExhausted bool) bool {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for ctx.Err() == nil {
		receiver.mu.Lock()
		ready := receiver.haveLast && receiver.last.Phase == "nested-scan" && receiver.last.Ordinal == 17 && (!requireExhausted || receiver.exhausted)
		receiver.mu.Unlock()
		if ready {
			return true
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
		}
	}
	return false
}

func TestReceiverFloodRemainsDrainedAndKeepsLastPhase(t *testing.T) {
	collector := NewCollector()
	receiver := NewReceiver(collector)
	for index := 0; index < 10000; index++ {
		phase := "root-scan"
		if index%2 == 0 {
			phase = "nested-scan"
		}
		raw := fmt.Sprintf("%s{\"phase\":%q,\"event\":\"start\",\"elapsedMillis\":%d}\n", Prefix, phase, index)
		if count, err := io.WriteString(receiver, raw); count != len(raw) || err != nil {
			t.Fatalf("diagnostic drain stopped: %d %v", count, err)
		}
	}
	_, _ = io.WriteString(receiver, Prefix+`{"phase":"filesystem-extraction","event":"start","elapsedMillis":10001}`+"\n")
	receiver.Finish()
	forwarded, truncated := collector.Snapshot()
	if receiver.Unexpected() || bytes.Count(forwarded, []byte("\n")) > MaxForwardedRecords || len(forwarded) > MaxRetainedBytes || !truncated {
		t.Fatal("diagnostic caps not enforced")
	}
	if summary := receiver.Summary(); !strings.Contains(summary, "phase=filesystem-extraction") || !strings.Contains(summary, "truncated=true") {
		t.Fatal(summary)
	}
}

func TestReceiverSamePhaseFloodPreservesTruncationMarker(t *testing.T) {
	collector := NewCollector()
	receiver := NewReceiver(collector)
	for index := 0; index < 10000; index++ {
		_, _ = fmt.Fprintf(receiver, "%s{\"phase\":\"root-scan\",\"event\":\"start\",\"elapsedMillis\":%d}\n", Prefix, index)
	}
	receiver.Finish()
	raw, truncated := collector.Snapshot()
	if receiver.Unexpected() || !truncated || !strings.Contains(receiver.Summary(), "truncated=true") || len(raw) > MaxRetainedBytes || !bytes.Contains(raw, []byte(`"elapsedMillis":9999`)) {
		t.Fatalf("rate-limited flood lost truncation or last phase: truncated=%t summary=%s", truncated, receiver.Summary())
	}
}

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, errors.New("closed sink") }

func TestDiagnosticWriterFailuresDoNotFailInspection(t *testing.T) {
	ctx := WithWriter(context.Background(), failedWriter{})
	Emit(ctx, "root-scan", "start", 0, 0)
	receiver := NewReceiver(NewCollector())
	_, err := io.WriteString(receiver, Prefix+`{"phase":"root-scan","event":"start","elapsedMillis":1}`+"\n")
	receiver.Finish()
	if err != nil || receiver.Unexpected() || !strings.Contains(receiver.Summary(), "phase=root-scan") {
		t.Fatal("closed diagnostic sink altered inspection", err)
	}
}

func TestEmitterDoesNotEmitCallerText(t *testing.T) {
	var output bytes.Buffer
	ctx := WithWriter(t.Context(), &output)
	Emit(ctx, "secret-token", "start", 0, 0)
	Emit(ctx, "root-scan", "start", 0, 0)
	receiver := NewReceiver(nil)
	_, _ = receiver.Write(output.Bytes())
	receiver.Finish()
	if receiver.Unexpected() || strings.Contains(output.String(), "secret-token") {
		t.Fatal(output.String())
	}
}

func TestSubjectMetadataComesOnlyFromHeldOwner(t *testing.T) {
	pin := "sha256:" + strings.Repeat("a", 64)
	wrong := "sha256:" + strings.Repeat("b", 64)
	collector := NewCollector()
	receiver := NewSubjectReceiver(collector, "disk-image", pin)
	_, _ = io.WriteString(receiver, Prefix+`{"phase":"root-scan","event":"start","elapsedMillis":1,"kind":"tarball","subject":"`+wrong+`"}`+"\n")
	receiver.Finish()
	raw, _ := collector.Snapshot()
	if receiver.Unexpected() || !bytes.Contains(raw, []byte(pin)) || bytes.Contains(raw, []byte(wrong)) || bytes.Contains(raw, []byte(`"kind":"tarball"`)) {
		t.Fatalf("helper substituted subject coordinates: %s", raw)
	}
}

func TestReceiverPreservesBoundariesAndMarksSampling(t *testing.T) {
	for _, test := range []struct {
		name      string
		second    string
		delay     time.Duration
		truncated bool
	}{
		{"event boundary", `"event":"complete","ordinal":1`, 0, false},
		{"member boundary", `"event":"start","ordinal":2`, 0, false},
		{"sampled repeat", `"event":"start","ordinal":1`, 0, true},
		{"repeat before interval", `"event":"start","ordinal":1`, 99 * time.Millisecond, true},
		{"repeat at interval", `"event":"start","ordinal":1`, 100 * time.Millisecond, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			collector := NewCollector()
			receiver := NewReceiver(collector)
			now := time.Unix(1, 0)
			receiver.now = func() time.Time { return now }
			_, _ = io.WriteString(receiver, Prefix+`{"phase":"nested-scan","event":"start","elapsedMillis":0,"ordinal":1}`+"\n")
			now = now.Add(test.delay)
			_, _ = io.WriteString(receiver, Prefix+`{"phase":"nested-scan","elapsedMillis":1,`+test.second+"}\n")
			wantForwarded := 2
			if test.truncated {
				wantForwarded = 1
			}
			if receiver.forwarded != wantForwarded {
				t.Fatalf("forwarded=%d want=%d before Finish", receiver.forwarded, wantForwarded)
			}
			receiver.Finish()
			raw, truncated := collector.Snapshot()
			if receiver.Unexpected() || truncated != test.truncated || bytes.Count(raw, []byte("\n")) != 2 || !strings.Contains(receiver.Summary(), fmt.Sprintf("truncated=%t", test.truncated)) {
				t.Fatalf("incorrect boundary/sampling: %s, %s", raw, receiver.Summary())
			}
		})
	}
}

func TestSubjectEnrichedFloodReservesSampledLastRecord(t *testing.T) {
	collector := NewCollector()
	pin := "sha256:" + strings.Repeat("a", 64)
	receiver := NewSubjectReceiver(collector, "installer-content", pin)
	receiver.now = func() time.Time { return time.Unix(1, 0) }
	last := Record{Event: "complete", Depth: 4, Ordinal: 200000, Kind: "installer-content", Subject: pin}
	for index := 0; index < 400; index++ {
		last.Phase = "filesystem-extraction"
		if index%2 == 0 {
			last.Phase = "subject-revalidation"
		}
		last.ElapsedMillis = int64(index)
		encoded, err := json.Marshal(last)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprintf(receiver, "%s%s\n", Prefix, encoded)
	}
	forwardedLast, err := json.Marshal(last)
	if err != nil {
		t.Fatal(err)
	}
	forwardedLast = append(forwardedLast, '\n')
	tail := bytes.Clone(collector.tail)
	capacity := MaxRetainedBytes - MaxRecordBytes
	if len(tail) > capacity || capacity-len(tail) >= len(forwardedLast) || !bytes.HasSuffix(tail, forwardedLast) {
		t.Fatalf("tail did not fill the reserved capacity: bytes=%d capacity=%d", len(tail), capacity)
	}
	// Same coordinates at a fixed clock are sampled, while the maximal elapsed
	// value makes the final enriched record larger than the preceding records.
	last.ElapsedMillis = 24 * 60 * 60 * 1000
	encoded, err := json.Marshal(last)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintf(receiver, "%s%s\n", Prefix, encoded)
	encoded = append(encoded, '\n')
	if len(encoded) > MaxRecordBytes || receiver.forwarded != 400 || !bytes.Equal(collector.tail, tail) {
		t.Fatal("last record was not sampled independently of the tail")
	}
	for _, finish := range []bool{false, true} {
		if finish {
			receiver.Finish()
		}
		raw, truncated := collector.Snapshot()
		lastLine := raw[bytes.LastIndexByte(bytes.TrimSuffix(raw, []byte("\n")), '\n')+1:]
		if receiver.Unexpected() || !truncated || len(raw) > MaxRetainedBytes || !bytes.Equal(lastLine, encoded) {
			t.Fatalf("sampled last record lost or oversized after Finish=%t: bytes=%d truncated=%t summary=%s", finish, len(raw), truncated, receiver.Summary())
		}
		if !finish && !bytes.Equal(raw, append(bytes.Clone(tail), encoded...)) {
			t.Fatal("Snapshot did not append the separately retained last record")
		}
	}
}

func TestSubjectEnrichedCollectorEvictsWholeLinesAcrossReceivers(t *testing.T) {
	collector := NewCollector()
	for attempt := 0; attempt < 2; attempt++ {
		pin := "sha256:" + strings.Repeat(string(rune('a'+attempt)), 64)
		receiver := NewSubjectReceiver(collector, "disk-image", pin)
		count := 400
		if attempt == 1 {
			count = 80
		}
		for index := 0; index < count; index++ {
			_, _ = fmt.Fprintf(receiver, "%s{\"phase\":\"nested-scan\",\"event\":\"start\",\"elapsedMillis\":%d,\"ordinal\":%d}\n", Prefix, index, index+1)
		}
		receiver.Finish()
		raw, truncated := collector.Snapshot()
		if receiver.Unexpected() || !truncated || !strings.Contains(receiver.Summary(), "truncated=true") || len(raw) > MaxRetainedBytes || !bytes.HasSuffix(raw, []byte("\n")) || !bytes.Contains(raw, []byte(fmt.Sprintf(`"ordinal":%d`, count))) || !bytes.Contains(raw, []byte(pin)) {
			t.Fatalf("eviction lost bounded final record: %d %t %s", len(raw), truncated, receiver.Summary())
		}
		for _, line := range bytes.Split(bytes.TrimSuffix(raw, []byte("\n")), []byte("\n")) {
			var record Record
			if err := json.Unmarshal(line, &record); err != nil || !valid(record) {
				t.Fatalf("eviction split a record: %q %v", line, err)
			}
		}
	}
}

func TestFailureBytesAreBoundedAndNeverRetained(t *testing.T) {
	for _, failure := range []string{"[NVKR-E-VERIFY-FAILED] exact helper cause\n", strings.Repeat("untrusted", 2000) + "\n", "unfinished runtime failure"} {
		t.Run(fmt.Sprintf("bytes=%d", len(failure)), func(t *testing.T) {
			collector := NewCollector()
			receiver := NewReceiver(collector)
			_, _ = io.WriteString(receiver, Prefix+`{"phase":"root-scan","event":"start","elapsedMillis":1}`+"\n")
			_, _ = io.WriteString(receiver, failure)
			receiver.Finish()
			raw, _ := collector.Snapshot()
			cause := receiver.FailureBytes()
			if !receiver.Unexpected() || len(cause) == 0 || len(cause) > MaxFailureBytes+1 || !bytes.HasPrefix([]byte(failure), bytes.TrimSuffix(cause, []byte("\n"))) || bytes.Contains(raw, []byte("helper cause")) || bytes.Contains(raw, []byte("untrusted")) || bytes.Contains(raw, []byte("runtime failure")) {
				t.Fatalf("invalid failure-only capture: %q %q", cause, raw)
			}
			cause[0] = '!'
			if receiver.FailureBytes()[0] == '!' {
				t.Fatal("caller mutated receiver cause")
			}
		})
	}
}

func TestReceiverParsingCeiling(t *testing.T) {
	const record = Prefix + `{"phase":"nested-scan","event":"start","elapsedMillis":1,"ordinal":17}` + "\n"
	for _, test := range []struct {
		name            string
		previous, count int
		chunk           int
		rejected        bool
	}{
		{"ceiling minus one", MaxParsedRecords - 2, 1, len(record), false},
		{"exact ceiling", MaxParsedRecords - 1, 1, len(record), false},
		{"over ceiling", MaxParsedRecords - 1, 2, len(record), true},
		{"over ceiling byte chunks", MaxParsedRecords - 1, 2, 1, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			collector := NewCollector()
			receiver := NewReceiver(collector)
			// Exercise the arithmetic boundary without millions of repeated JSON
			// parses. The existing flood tests cover accumulation and sampling.
			receiver.parsedRecords = test.previous
			raw := []byte(strings.Repeat(record, test.count))
			for len(raw) != 0 {
				size := min(test.chunk, len(raw))
				if count, err := receiver.Write(raw[:size]); count != size || err != nil {
					t.Fatal("volume rejection stopped draining", count, err)
				}
				raw = raw[size:]
			}
			receiver.Finish()
			if receiver.Unexpected() != test.rejected || receiver.exhausted != test.rejected || receiver.parsedRecords != min(MaxParsedRecords, test.previous+test.count) || !strings.Contains(receiver.Summary(), "ordinal=17") {
				t.Fatalf("incorrect parsing ceiling: parsed=%d exhausted=%t summary=%s", receiver.parsedRecords, receiver.exhausted, receiver.Summary())
			}
			retained, truncated := collector.Snapshot()
			if truncated != test.rejected || len(retained) > MaxRetainedBytes || len(receiver.FailureBytes()) != 0 {
				t.Fatal("incorrect rejection retention", truncated, string(retained))
			}
		})
	}
}

func TestReceiverChargesMalformedCandidatesBeforeParsing(t *testing.T) {
	receiver := NewReceiver(nil)
	receiver.parsedRecords = MaxParsedRecords - 1
	_, _ = io.WriteString(receiver, Prefix+"{invalid-json}\n")
	if receiver.parsedRecords != MaxParsedRecords || !receiver.Unexpected() || receiver.exhausted {
		t.Fatal("malformed candidate escaped parsing allowance")
	}
	_, _ = io.WriteString(receiver, Prefix+"{invalid-json}\n")
	if !receiver.exhausted || receiver.parsedRecords != MaxParsedRecords {
		t.Fatal("malformed flood escaped ceiling")
	}
}

func TestExhaustedReceiverDrainsFloodWithoutParsingOrRetaining(t *testing.T) {
	collector := NewCollector()
	receiver := NewReceiver(collector)
	receiver.parsedRecords = MaxParsedRecords - 1
	_, _ = io.WriteString(receiver, Prefix+`{"phase":"member-walk","event":"start","elapsedMillis":1}`+"\n")
	_, _ = io.WriteString(receiver, Prefix+"{invalid-json}\n")
	before, _ := collector.Snapshot()
	for _, raw := range []string{strings.Repeat(Prefix+"{secret}\n", 10000), strings.Repeat("secret", MaxFailureBytes), "partial"} {
		if count, err := io.WriteString(receiver, raw); count != len(raw) || err != nil {
			t.Fatal("exhausted stream did not drain", count, err)
		}
	}
	receiver.Finish()
	after, truncated := collector.Snapshot()
	if !receiver.Unexpected() || !receiver.exhausted || receiver.parsedRecords != MaxParsedRecords || len(receiver.line) != 0 || len(receiver.FailureBytes()) != 0 || !bytes.Equal(before, after) || !truncated || !strings.Contains(receiver.Summary(), "phase=member-walk") {
		t.Fatal("exhausted stream parsed or retained rejected bytes", receiver.Summary())
	}
	// A shared retention collector does not share the helper's parsing allowance.
	sibling := NewReceiver(collector)
	_, _ = io.WriteString(sibling, Prefix+`{"phase":"report-completion","event":"complete","elapsedMillis":2}`+"\n")
	sibling.Finish()
	if sibling.Unexpected() || sibling.parsedRecords != 1 || !strings.Contains(sibling.Summary(), "phase=report-completion") {
		t.Fatal("sibling helper inherited exhausted allowance", sibling.Summary())
	}
}

func TestReceiverConcurrentWritesSaturateParsingCount(t *testing.T) {
	receiver := NewReceiver(NewCollector())
	receiver.parsedRecords = MaxParsedRecords - 4
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			_, _ = io.WriteString(receiver, Prefix+`{"phase":"nested-scan","event":"start","elapsedMillis":1}`+"\n")
			_ = receiver.Summary()
		})
	}
	workers.Wait()
	receiver.Finish()
	if !receiver.Unexpected() || receiver.parsedRecords != MaxParsedRecords || !receiver.exhausted || !strings.Contains(receiver.Summary(), "phase=nested-scan") {
		t.Fatal("concurrent stream escaped parsing ceiling", receiver.Summary())
	}
}

func TestNativeChildParsingCeilingCancellationStillDrainsAndJoins(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	receiver := NewReceiver(NewCollector())
	receiver.parsedRecords = MaxParsedRecords - 1
	command := exec.CommandContext(ctx, executable, "-test.run=^TestInspectionProgressChild$")
	command.Env = append(os.Environ(), "NVKR_INSPECTION_PROGRESS_TEST_CHILD=1", "NVKR_INSPECTION_PROGRESS_TEST_VOLUME=1")
	command.Stderr = receiver
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	exhausted := waitForChildInspectionProgress(ctx, receiver, true)
	cancel()
	err = command.Wait()
	receiver.Finish()
	if !exhausted || err == nil || ctx.Err() != context.Canceled || command.ProcessState == nil || !receiver.Unexpected() || !receiver.exhausted || receiver.parsedRecords != MaxParsedRecords || !strings.Contains(receiver.Summary(), "phase=nested-scan") || !strings.Contains(receiver.Summary(), "ordinal=17") || strings.Contains(receiver.Summary(), "report-completion") {
		t.Fatalf("volume rejection lost child joining or accepted a later phase: err=%v summary=%s", err, receiver.Summary())
	}
}
