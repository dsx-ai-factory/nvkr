package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

func TestDefaultEvidenceHTTPClientHasExplicitDeadlines(t *testing.T) {
	client := newEvidenceHTTPClient()
	if client.Timeout <= 0 || client.Timeout > 10*time.Minute {
		t.Fatalf("evidence client timeout = %s, want a positive bounded deadline", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.ResponseHeaderTimeout <= 0 || transport.TLSHandshakeTimeout <= 0 {
		t.Fatalf("evidence transport lacks bounded header/TLS deadlines: %#v", client.Transport)
	}
}

func TestEvidenceReaderCancellationIsCodedBeforeTransport(t *testing.T) {
	called := false
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("transport must not run")
	})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (commandEvidenceReader{client: client}).ReadEvidence(ctx, "https://evidence.example.test/sbom")
	if cliErrorCode(err) != nkderr.CodeTimeout {
		t.Fatalf("canceled evidence read code = %q, want %q: %v",
			cliErrorCode(err), nkderr.CodeTimeout, err)
	}
	if called {
		t.Fatal("canceled evidence read reached the HTTP transport")
	}
}

func TestEvidenceReaderCancellationStopsBoundedRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := &cancelingEvidenceReader{cancel: cancel}
	_, err := readBoundedEvidence(ctx, reader)
	if cliErrorCode(err) != nkderr.CodeTimeout || !nkderr.Retryable(err) {
		t.Fatalf("mid-read cancellation error = %v, want retryable timeout", err)
	}
	if reader.reads != 1 {
		t.Fatalf("evidence reader performed %d reads after cancellation, want one", reader.reads)
	}
}

func TestEvidenceReaderRejectsSymlinkAndFIFOWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := (commandEvidenceReader{}).ReadEvidence(context.Background(), "file://"+link); cliErrorCode(err) != nkderr.CodeUnavailable {
		t.Fatalf("symlink evidence error = %v, want unavailable", err)
	}
	fifo := filepath.Join(root, "fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (commandEvidenceReader{}).ReadEvidence(context.Background(), "file://"+fifo); cliErrorCode(err) != nkderr.CodeUnavailable {
		t.Fatalf("FIFO evidence error = %v, want unavailable", err)
	}
}

type cancelingEvidenceReader struct {
	cancel context.CancelFunc
	reads  int
}

func (reader *cancelingEvidenceReader) Read(buffer []byte) (int, error) {
	reader.reads++
	n := copy(buffer, "one evidence chunk")
	reader.cancel()
	return n, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
