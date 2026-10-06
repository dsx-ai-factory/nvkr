package sbom

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/diskdiagnostic"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

func TestExportProgressTracksActualFirstBytes(t *testing.T) {
	var diagnostic, payload bytes.Buffer
	ctx := diskdiagnostic.WithWriter(t.Context(), &diagnostic)
	diskdiagnostic.Emit(ctx, "export-startup", "start", 0, 0)
	writer := diskExtractionWriter{ctx: ctx, writer: &payload}
	if _, err := writer.Write(nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(diagnostic.String(), "filesystem-extraction") {
		t.Fatal("extraction declared before any exported bytes")
	}
	for _, chunk := range []string{"tar-prefix", "tar-content"} {
		if _, err := writer.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Count(diagnostic.String(), `"phase":"filesystem-extraction"`) != 1 || payload.String() != "tar-prefixtar-content" {
		t.Fatal("export stream or phase altered", diagnostic.String(), payload.String())
	}
	receiver := diskdiagnostic.NewReceiver(nil)
	_, _ = receiver.Write(diagnostic.Bytes())
	receiver.Finish()
	if receiver.Unexpected() || !strings.Contains(receiver.Summary(), "phase=filesystem-extraction") {
		t.Fatal(receiver.Summary())
	}
}

func TestContainerScanProgress(t *testing.T) {
	const inventory = `{"schema":{"version":"16.1.10"},"artifacts":[{"name":"observed","version":"1","type":"deb","metadata":{"architecture":"all"}}]}`
	for _, test := range []struct {
		name, body string
		want       e.Code
	}{
		{"scanner admission failure", "", e.CodeUnverifiedRef},
		{"success", "printf '%s' '" + inventory + "'", ""},
		{"scanner failure", "exit 23", e.CodeProvenanceMissing},
		{"timeout", "sleep 30", e.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.body != "" && runtime.GOOS != "linux" {
				t.Skip("held scanner execution requires native Linux")
			}
			filename := filepath.Join(t.TempDir(), "scanner")
			ready := filepath.Join(t.TempDir(), "ready")
			body := test.body
			if test.want == e.CodeTimeout {
				body = "touch '" + ready + "'\nexec sleep 30"
			}
			script := []byte("#!/bin/sh\n" + body + "\n")
			if test.body != "" {
				if err := os.WriteFile(filename, script, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			archive := nestedOCIFixture(t, []string{runtime.GOARCH}, false, nil)
			subject, err := heldfile.FromReaderContext(t.Context(), io.NewSectionReader(archive, 0, archive.Size()), heldfile.Options{Label: "container-progress", MaxBytes: 1 << 20})
			if err != nil {
				t.Fatal(err)
			}
			defer subject.Close()
			var diagnostic bytes.Buffer
			ctx := diskdiagnostic.WithWriter(t.Context(), &diagnostic)
			var components []Component
			if test.want == e.CodeTimeout {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				done := make(chan struct{})
				go func() {
					components, err = inspectContainerMaterial(ctx, subject, "linux/"+runtime.GOARCH, Scanner{Path: filename, Digest: rawDigest(script)}, 1<<20)
					close(done)
				}()
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
				started := false
			waitReady:
				for {
					select {
					case <-ticker.C:
						if _, statErr := os.Stat(ready); statErr == nil {
							started = true
							cancel()
							break waitReady
						}
					case <-ctx.Done():
						break waitReady
					case <-done:
						break waitReady
					}
				}
				cancel()
				<-done // Inspection owns child termination; test owns this join.
				if !started {
					t.Fatalf("scanner did not signal readiness: %v", err)
				}
			} else {
				components, err = inspectContainerMaterial(ctx, subject, "linux/"+runtime.GOARCH, Scanner{Path: filename, Digest: rawDigest(script)}, 1<<20)
			}
			if test.want != "" {
				assertDependencyCode(t, err, test.want)
				if len(components) != 0 {
					t.Fatal("failed scan returned partial components")
				}
			} else if err != nil || len(components) != 1 {
				t.Fatalf("successful scan changed: %+v %v", components, err)
			}
			if test.body == "" {
				if !strings.Contains(err.Error(), "authenticating generated transport scanner") || strings.Contains(diagnostic.String(), `"phase":"root-scan"`) {
					t.Fatalf("wrong scanner admission boundary: %v %s", err, diagnostic.String())
				}
				return
			}
			receiver := diskdiagnostic.NewReceiver(nil)
			_, _ = receiver.Write(diagnostic.Bytes())
			receiver.Finish()
			last := "event=start"
			phase := "phase=root-scan"
			if test.want == "" {
				last = "event=complete"
				phase = "phase=subject-revalidation"
				if !strings.Contains(diagnostic.String(), `"phase":"root-scan","event":"complete"`) {
					t.Fatal("successful scanner did not finish its own phase")
				}
			}
			if receiver.Unexpected() || !strings.Contains(receiver.Summary(), phase) || !strings.Contains(receiver.Summary(), last) {
				t.Fatalf("scanner phase lost: %s", receiver.Summary())
			}
		})
	}
}
