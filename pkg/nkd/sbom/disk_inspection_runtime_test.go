package sbom

import (
	"context"
	"io"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

func TestDiskInspectionRuntimeRejectsDescriptions(t *testing.T) {
	for _, observation := range []*pipeline.DiskInspectionObservation{nil, {}} {
		if inventory, err := inventoryFromContainerObservation(observation); inventory != nil || sbomCode(err) == "" {
			t.Fatalf("unissued observation accepted: %v", err)
		}
	}
	if (&DiskInstalledInventory{}).RuntimeBinding() != "" {
		t.Fatal("executable-only inventory grants runtime binding")
	}
	var capability *DiskInspectionRuntime
	if _, _, _, err := capability.BindingContext(t.Context()); sbomCode(err) != e.CodeEngineCapability {
		t.Fatalf("absent runtime: %v", err)
	}
	if err := capability.AdmitSubject(t.Context(), 1); sbomCode(err) != e.CodeEngineCapability {
		t.Fatalf("absent runtime admits subject: %v", err)
	}
	if err := capability.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDiskInspectionHelperAdmitsBeforeToolIO(t *testing.T) {
	digest := "sha256:" + strings.Repeat("1", 64)
	for _, tt := range []struct {
		name                      string
		ctx                       context.Context
		inspector, codec, subject string
		size                      int64
		input                     io.Reader
		output                    io.Writer
		want                      e.Code
	}{
		{name: "nil context", inspector: digest, codec: digest, subject: digest, size: 1, input: strings.NewReader("x"), output: io.Discard, want: e.CodeUsage},
		{name: "wrong inspector", ctx: t.Context(), codec: digest, subject: digest, size: 1, input: strings.NewReader("x"), output: io.Discard, want: e.CodeUnpinnedInput},
		{name: "wrong codec", ctx: t.Context(), inspector: digest, subject: digest, size: 1, input: strings.NewReader("x"), output: io.Discard, want: e.CodeUnpinnedInput},
		{name: "oversized subject", ctx: t.Context(), inspector: digest, codec: digest, subject: digest, size: MaxDiagnosticDiskBytes + 1, input: strings.NewReader("x"), output: io.Discard, want: e.CodeUnpinnedInput},
		{name: "missing input", ctx: t.Context(), inspector: digest, codec: digest, subject: digest, size: 1, output: io.Discard, want: e.CodeUsage},
		{name: "missing output", ctx: t.Context(), inspector: digest, codec: digest, subject: digest, size: 1, input: strings.NewReader("x"), want: e.CodeUsage},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := RunDiskInspectionHelper(tt.ctx, tt.inspector, tt.codec, tt.subject, tt.size, tt.input, tt.output); sbomCode(err) != tt.want {
				t.Fatalf("got %v, want %s", err, tt.want)
			}
		})
	}
}
