package sbom

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func contentFixture(kind, architecture, payload string) *DiskContentInspection {
	pin := "sha256:" + strings.Repeat("a", 64)
	return &DiskContentInspection{runtimeDigest: pin, report: diskContentReport{Subject: "sha256:" + strings.Repeat(payload, 64), Size: 4096,
		Kind: kind, Platform: "linux/" + architecture, Inspector: pin, Codec: pin, Guestfish: pin, Scanner: pin, PackageReader: pin, MaxFilesystemBytes: 8192,
		Components: []Component{{Type: "library", Name: "base-package", Version: "2:1.0-1ubuntu4", PURL: "pkg:deb/ubuntu/base-package@2%3A1.0-1ubuntu4?arch=" + architecture,
			Properties: []Property{{Name: "nkd:packageArchitecture", Value: architecture}, {Name: "nkd:packageFormat", Value: "deb"}}}},
		Files: []DiskFile{{Path: "usr/bin/tool", Kind: "regular", Digest: pin, Size: 12}}}}
}

func TestReconcileDiskContentsExactSubjectsBothArchitectures(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		t.Run(architecture, func(t *testing.T) {
			root := contentFixture("disk-image", architecture, "b")
			inputs := []*DiskContentInspection{contentFixture("installer-iso", architecture, "c"), contentFixture("apt-acquisition", architecture, "d"), contentFixture("tarball", architecture, "e")}
			bom, err := ReconcileDiskContents(t.Context(), "sha256:"+strings.Repeat("f", 64), "node", root, inputs)
			if err != nil {
				t.Fatal(err)
			}
			if len(bom.Components) != 2 || len(bom.Dependencies) != 1 || bom.Metadata.Component.Hashes[0].Content != strings.Repeat("b", 64) {
				t.Fatalf("exact package/file coverage absent: %+v", bom)
			}
			raw, err := bom.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(raw, []byte("disk-reconciled/v1")) || !bytes.Contains(raw, []byte("2:1.0-1ubuntu4")) {
				t.Fatalf("exact version/method absent: %s", raw)
			}
			again, err := ReconcileDiskContents(t.Context(), "sha256:"+strings.Repeat("f", 64), "node", root, inputs)
			if err != nil {
				t.Fatal(err)
			}
			second, _ := again.Marshal()
			if !bytes.Equal(raw, second) {
				t.Fatal("same observations produced non-deterministic bytes")
			}
		})
	}
}

func TestDiskContentDocumentsRetainExactFileInventory(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		for _, filesOnly := range []bool{false, true} {
			t.Run(architecture+map[bool]string{true: "/loose-only", false: "/packages-and-files"}[filesOnly], func(t *testing.T) {
				inspection := contentFixture("tarball", architecture, "b")
				if filesOnly {
					inspection.report.Components = nil
				}
				bom, err := DiskContentDocument("sha256:"+strings.Repeat("f", 64), "material", inspection)
				if err != nil {
					t.Fatal(err)
				}
				wantCount := 2
				if filesOnly {
					wantCount = 1
				}
				if len(bom.Components) != wantCount || len(bom.Dependencies) != 1 || len(bom.Dependencies[0].DependsOn) != wantCount {
					t.Fatalf("incomplete material coverage: %+v", bom)
				}
				found := false
				for _, component := range bom.Components {
					if component.Type == "file" && component.Name == "usr/bin/tool" && len(component.Hashes) == 1 && component.Hashes[0].Content == strings.Repeat("a", 64) {
						found = true
					}
				}
				if !found || bom.Metadata.Component.Hashes[0].Content != strings.Repeat("b", 64) {
					t.Fatal("file/source subject hash not retained")
				}
				if _, err := bom.Marshal(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestReconcileDiskContentsRejectsMissingAndSubstitutedEvidence(t *testing.T) {
	for _, name := range []string{"nil root", "unissued runtime", "missing base", "missing capture", "missing staged", "wrong architecture", "different scanner", "unmatched package", "version shortening", "unsupported input", "duplicate subject"} {
		t.Run(name, func(t *testing.T) {
			root := contentFixture("disk-image", "arm64", "b")
			inputs := []*DiskContentInspection{contentFixture("installer-iso", "arm64", "c"), contentFixture("apt-acquisition", "arm64", "d"), contentFixture("tarball", "arm64", "e")}
			switch name {
			case "nil root":
				root = nil
			case "unissued runtime":
				root.runtimeDigest = ""
			case "missing base":
				inputs = inputs[1:]
			case "missing capture":
				inputs = append(inputs[:1], inputs[2])
			case "missing staged":
				inputs = inputs[:2]
			case "wrong architecture":
				root.report.Components[0].Properties[0].Value = "amd64"
			case "different scanner":
				inputs[0].report.Scanner = "sha256:" + strings.Repeat("f", 64)
			case "unmatched package":
				root.report.Components[0].Name = "unaccounted"
			case "version shortening":
				root.report.Components[0].Version = "1.0"
			case "unsupported input":
				inputs[0].report.Kind = "raw-guessed"
			case "duplicate subject":
				inputs[1].report.Subject = inputs[0].report.Subject
			}
			bom, err := ReconcileDiskContents(t.Context(), "sha256:"+strings.Repeat("f", 64), "node", root, inputs)
			var coded *e.Error
			if bom != nil || !errors.As(err, &coded) {
				t.Fatalf("bad evidence accepted or uncoded error: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ReconcileDiskContents(ctx, "", "", nil, nil); sbomCode(err) != e.CodeTimeout {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestFilesystemNativeMetadataAndFileProjection(t *testing.T) {
	pin := "sha256:" + strings.Repeat("a", 64)
	for _, schema := range []string{"16.1.10", "16.1.9", "unknown"} {
		t.Run(schema, func(t *testing.T) {
			raw := []byte(`{"schema":{"version":"` + schema + `"},"artifacts":[{"name":"sample","version":"2:1.0-1ubuntu4","type":"deb","purl":"pkg:deb/ubuntu/sample@1?arch=arm64","metadata":{"architecture":"arm64"}}]}`)
			inventory, err := filesystemDependenciesFromJSON(raw, pin)
			if schema != "16.1.10" {
				if err == nil {
					t.Fatal("unknown native schema accepted")
				}
				return
			}
			if err != nil || inventory.Components[0].Version != "2:1.0-1ubuntu4" || inventory.Components[0].Properties[0].Value != "arm64" {
				t.Fatalf("native metadata changed: %+v %v", inventory, err)
			}
		})
	}
	for _, name := range []string{"valid", "traversal", "duplicate", "fifo"} {
		t.Run(name, func(t *testing.T) {
			var archive bytes.Buffer
			writer := tar.NewWriter(&archive)
			filename := "usr/bin/loose"
			if name == "traversal" {
				filename = "../loose"
			}
			header := &tar.Header{Name: filename, Typeflag: tar.TypeReg, Mode: 0o644, Size: 3}
			if name == "fifo" {
				header.Typeflag = tar.TypeFifo
				header.Size = 0
			}
			if err := writer.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if header.Size != 0 {
				_, _ = writer.Write([]byte("bin"))
			}
			if name == "duplicate" {
				_ = writer.WriteHeader(header)
				_, _ = writer.Write([]byte("bin"))
			}
			_ = writer.Close()
			file, err := os.CreateTemp(t.TempDir(), "export")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			_, _ = io.Copy(file, &archive)
			files, err := filesystemFiles(t.Context(), file, 8192)
			if name == "traversal" || name == "duplicate" {
				if err == nil {
					t.Fatal("unsafe filesystem accepted")
				}
				return
			}
			if err != nil || len(files) != 1 {
				t.Fatalf("file observation failed: %v", err)
			}
			if name == "valid" && !canonical.IsDigest(files[0].Digest) {
				t.Fatal("loose file has no exact content hash")
			}
		})
	}
}

func TestBoundedScannerTransportStopsOverflowAndCancellation(t *testing.T) {
	for _, name := range []string{"within", "overflow", "canceled"} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			if name == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			var output bytes.Buffer
			size := 8
			if name == "overflow" {
				size = 64
			}
			err := writeBoundedArchive(ctx, &output, 16, func(destination io.Writer) error {
				_, err := io.Copy(destination, strings.NewReader(strings.Repeat("x", size)))
				return err
			})
			if name == "within" {
				if err != nil || output.Len() != 8 {
					t.Fatalf("valid stream failed: %v", err)
				}
				return
			}
			want := e.CodeEngineCapability
			if name == "canceled" {
				want = e.CodeTimeout
			}
			if sbomCode(err) != want {
				t.Fatalf("wrong bounded stream error: %v", err)
			}
			if output.Len() > 17 {
				t.Fatal("writer exceeded active-stream bound")
			}
		})
	}
}
