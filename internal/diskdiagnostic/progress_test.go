package diskdiagnostic

import (
	"bytes"
	"context"
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
