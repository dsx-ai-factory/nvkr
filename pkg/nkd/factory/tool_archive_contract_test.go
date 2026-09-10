package factory

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/pipeline"
)

func TestToolArchiveAcquisitionChecksTransportAndSelectedExecutable(t *testing.T) {
	for _, tc := range []struct {
		name                                         string
		names                                        []string
		link, wrongArchive, wrongExecutable, badGzip bool
		status                                       int
	}{
		{name: "valid with unrelated member", names: []string{"README", "bin/tool"}},
		{name: "missing executable", names: []string{"README"}},
		{name: "duplicate executable", names: []string{"bin/tool", "bin/tool"}},
		{name: "symlink executable", names: []string{"bin/tool"}, link: true},
		{name: "wrong archive digest", names: []string{"bin/tool"}, wrongArchive: true},
		{name: "wrong executable digest", names: []string{"bin/tool"}, wrongExecutable: true},
		{name: "not gzip", badGzip: true},
		{name: "HTTP unavailable", status: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte("exact executable")
			var archive bytes.Buffer
			gz := gzip.NewWriter(&archive)
			tw := tar.NewWriter(gz)
			for _, name := range tc.names {
				header := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(payload)), Typeflag: tar.TypeReg}
				if tc.link {
					header.Typeflag = tar.TypeSymlink
					header.Linkname = "elsewhere"
					header.Size = 0
				}
				if err := tw.WriteHeader(header); err != nil {
					t.Fatal(err)
				}
				if !tc.link {
					if _, err := tw.Write(payload); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			raw := archive.Bytes()
			if tc.badGzip {
				raw = []byte("not gzip")
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.status != 0 {
					w.WriteHeader(tc.status)
					return
				}
				_, _ = w.Write(raw)
			}))
			defer server.Close()
			node := Node{ID: "tool/tool", URL: server.URL, ToolFormat: ToolFormatTarGzip, Executable: "bin/tool", Digest: pipeline.FileDigest(raw), ExecutableDigest: pipeline.FileDigest(payload)}
			if tc.wrongArchive {
				node.Digest = pipeline.FileDigest([]byte("other archive"))
			}
			if tc.wrongExecutable {
				node.ExecutableDigest = pipeline.FileDigest([]byte("other executable"))
			}
			out := filepath.Join(t.TempDir(), "output", "tool")
			err := executeToolAcquire(t.Context(), node, Artifact{Path: out}, io.Discard)
			if tc.name != "valid with unrelated member" {
				want := e.CodeVerifyFailed
				if tc.status != 0 {
					want = e.CodeUnavailable
				}
				assertFactoryErrorCode(t, err, want)
				if _, err := os.Lstat(filepath.Dir(out)); !os.IsNotExist(err) {
					t.Fatalf("rejected tool created output state: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(out)
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("executable bytes = %q, %v", got, err)
			}
			info, err := os.Stat(out)
			if err != nil || info.Mode().Perm() != 0o555 {
				t.Fatalf("executable mode = %v, %v", info, err)
			}
		})
	}
}
