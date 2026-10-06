package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/diskdiagnostic"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

func TestDiskInspectionProgressDoesNotRelaxDiagnosticGate(t *testing.T) {
	progress := diskdiagnostic.Prefix + `{"phase":"root-scan","event":"start","elapsedMillis":123}` + "\n"
	for _, test := range []struct {
		name, diagnostic string
		canceled         bool
		want             e.Code
	}{
		{"valid progress and complete report", progress, false, ""},
		{"unexpected warning", progress + "password=secret\n", false, e.CodeEnvelopeDecode},
		{"forged progress field", progress + diskdiagnostic.Prefix + `{"phase":"root-scan","event":"start","elapsedMillis":1,"secret":"token"}` + "\n", false, e.CodeEnvelopeDecode},
		{"timeout keeps last phase", progress, true, e.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			subject, err := heldfile.FromBytesContext(ctx, []byte("held subject"), heldfile.Options{Label: "progress-fixture", MaxBytes: 128})
			if err != nil {
				t.Fatal(err)
			}
			defer subject.Close()
			collector := diskdiagnostic.NewCollector()
			selection := testDiskInspectionSelection(t)
			selection.Diagnostics = collector
			runtime := &diskInspectionRunRuntime{report: "report", scanDiagnostic: test.diagnostic}
			if test.canceled {
				runtime.cancel = cancel
				runtime.scanErr = context.Canceled
			}
			raw, err := runDiskInspectionContainer(ctx, runtime, selection, "image", subject)
			if code(err) != test.want || (raw != nil) != (test.want == "") {
				t.Fatalf("raw=%q err=%v", raw, err)
			}
			output, _ := collector.Snapshot()
			if strings.Contains(string(output), "secret") {
				t.Fatal("raw untrusted text leaked", string(output))
			}
			if test.canceled && (!strings.Contains(err.Error(), "phase=root-scan") || !strings.Contains(err.Error(), "elapsedMillis=123")) {
				t.Fatal("timeout discarded safe diagnostics", err)
			}
		})
	}
}
