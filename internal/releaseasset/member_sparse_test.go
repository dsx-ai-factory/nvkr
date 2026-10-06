package releaseasset

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func sparseReleaseArchive(t *testing.T, size int64) []byte {
	t.Helper()
	var raw bytes.Buffer
	w := tar.NewWriter(&raw)
	for _, header := range []tar.Header{
		{Name: "bin/tool", Mode: 0755, Typeflag: tar.TypeReg, Size: 1},
		{Name: "other", Mode: 0644, Typeflag: tar.TypeReg, Size: 1, Format: tar.FormatPAX, PAXRecords: map[string]string{
			"XXX.sparse.major": "0", "XXX.sparse.minor": "1", "XXX.sparse.map": "0,1",
			"XXX.sparse.numblocks": "1", "XXX.sparse.size": strconv.FormatInt(size, 10),
		}},
	} {
		if err := w.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// Go's writer deliberately filters GNU sparse keys. Replacing the same-length
	// fixture-only PAX keyword leaves the stdlib-produced headers and lengths intact.
	data := bytes.ReplaceAll(raw.Bytes(), []byte("XXX.sparse."), []byte("GNU.sparse."))
	var encoded bytes.Buffer
	gz := gzip.NewWriter(&encoded)
	if _, err := gz.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func TestSelectTarGzipSparseLogicalBound(t *testing.T) {
	data := sparseReleaseArchive(t, 8<<30)
	got, err := SelectTarGzip(t.Context(), bytes.NewReader(data), "bin/tool", Limits{ExecutableBytes: 8, ExpandedBytes: 1 << 20, Entries: 8})
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed || !strings.Contains(err.Error(), "logical member byte limit") || got != nil {
		t.Fatalf("sparse member escaped logical bound: %q, %v", got, err)
	}
}

func TestSelectTarGzipPaddingExpansionBound(t *testing.T) {
	base := releaseArchive(t, tar.Header{Name: "bin/tool", Size: 1, Typeflag: tar.TypeReg})
	reader, err := gzip.NewReader(bytes.NewReader(base))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	gz := gzip.NewWriter(&encoded)
	if _, err := gz.Write(append(raw, make([]byte, 4096)...)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = SelectTarGzip(t.Context(), bytes.NewReader(encoded.Bytes()), "bin/tool", Limits{ExecutableBytes: 8, ExpandedBytes: int64(len(raw)) + 1024, Entries: 8})
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed || !strings.Contains(err.Error(), "expanded byte limit") {
		t.Fatalf("padding bound: %v", err)
	}
}

// A deterministic context cancels while synthesized holes are being consumed,
// independently of physical compressed input reads or a timing-sensitive timer.
type scanCancelContext struct {
	context.Context
	checks int
}

func (c *scanCancelContext) Err() error {
	c.checks++
	if c.checks >= 12 {
		return context.Canceled
	}
	return nil
}

func TestSelectTarGzipSparseScanCancellation(t *testing.T) {
	ctx := &scanCancelContext{Context: t.Context()}
	got, err := SelectTarGzip(ctx, bytes.NewReader(sparseReleaseArchive(t, 1<<20)), "bin/tool", Limits{ExecutableBytes: 8, ExpandedBytes: 2 << 20, Entries: 8})
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeTimeout || !strings.Contains(err.Error(), "reading remaining release member") || got != nil {
		t.Fatalf("mid-scan cancellation: %q, %v (checks %d)", got, err, ctx.checks)
	}
}
