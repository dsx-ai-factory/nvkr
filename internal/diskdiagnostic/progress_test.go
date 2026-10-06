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
	if os.Getenv("NKD_INSPECTION_PROGRESS_TEST_CHILD") != "1" {
		return
	}
	ctx := WithWriter(context.Background(), os.Stderr)
	Emit(ctx, "nested-scan", "start", 1, 17)
	time.Sleep(10 * time.Second)
}

// A real child writes its progress pipe before blocking. CommandContext owns
// killing and joining it; the receiver still retains the last sanitized phase.
func TestNativeChildTimeoutKeepsProgressAndJoins(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	receiver := NewReceiver(nil)
	command := exec.CommandContext(ctx, executable, "-test.run=^TestInspectionProgressChild$")
	command.Env = append(os.Environ(), "NKD_INSPECTION_PROGRESS_TEST_CHILD=1")
	command.Stderr = receiver
	err = command.Run()
	receiver.Finish()
	if err == nil || ctx.Err() != context.DeadlineExceeded || command.ProcessState == nil || receiver.Unexpected() || !strings.Contains(receiver.Summary(), "phase=nested-scan") || !strings.Contains(receiver.Summary(), "ordinal=17") {
		t.Fatalf("child was not joined with its safe last phase: err=%v summary=%s", err, receiver.Summary())
	}
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
		truncated bool
	}{
		{"event boundary", `"event":"complete","ordinal":1`, false},
		{"member boundary", `"event":"start","ordinal":2`, false},
		{"sampled repeat", `"event":"start","ordinal":1`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			collector := NewCollector()
			receiver := NewReceiver(collector)
			_, _ = io.WriteString(receiver, Prefix+`{"phase":"nested-scan","event":"start","elapsedMillis":0,"ordinal":1}`+"\n")
			_, _ = io.WriteString(receiver, Prefix+`{"phase":"nested-scan","elapsedMillis":1,`+test.second+"}\n")
			receiver.Finish()
			raw, truncated := collector.Snapshot()
			if receiver.Unexpected() || truncated != test.truncated || bytes.Count(raw, []byte("\n")) != 2 || !strings.Contains(receiver.Summary(), fmt.Sprintf("truncated=%t", test.truncated)) {
				t.Fatalf("incorrect boundary/sampling: %s, %s", raw, receiver.Summary())
			}
		})
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
	for _, failure := range []string{"[NKD-E-VERIFY-FAILED] exact helper cause\n", strings.Repeat("untrusted", 2000) + "\n", "unfinished runtime failure"} {
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
