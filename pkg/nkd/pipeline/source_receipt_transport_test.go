package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestIndependentSourceReceiptsRestoreTogether(t *testing.T) {
	ctx := context.Background()
	consumer := t.TempDir()
	for _, destination := range []string{"source-go", "source-buildkit"} {
		producer := t.TempDir()
		receipt, err := SourceReceiptPath(filepath.Join(producer, "src"), destination)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(receipt), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(receipt, []byte(destination), 0o600); err != nil {
			t.Fatal(err)
		}
		relative, err := filepath.Rel(producer, receipt)
		if err != nil {
			t.Fatal(err)
		}
		archive := filepath.Join(producer, "receipt.tar")
		if err := PackTransport(ctx, archive, producer, []string{relative}); err != nil {
			t.Fatal(err)
		}
		if err := UnpackTransport(ctx, archive, consumer, []string{relative}); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(consumer, relative))
		if err != nil || string(got) != destination {
			t.Fatalf("receipt handoff: %q %v", got, err)
		}
	}
	_, err := SourceReceiptPath(filepath.Join(consumer, "src"), "../escape")
	if e.ExitCode(err) != e.ExitCode(e.New(e.CodeInvalidCatalog, "expected")) {
		t.Fatalf("unsafe receipt destination: %v", err)
	}
}
