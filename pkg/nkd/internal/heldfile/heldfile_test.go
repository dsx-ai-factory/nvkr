package heldfile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestHeldProcessHelper(t *testing.T) {
	if os.Getenv("NKD_HELDFILE_HELPER") != "1" {
		return
	}
	mode := os.Getenv("NKD_HELDFILE_MODE")
	switch mode {
	case "read":
		if len(os.Args) == 0 {
			t.Fatal("helper has no input argument")
		}
		raw, err := os.ReadFile(os.Args[len(os.Args)-1])
		if err != nil {
			t.Fatal(err)
		}
		_, _ = os.Stdout.Write(raw)
	case "environment":
		_, _ = os.Stdout.WriteString(os.Getenv("LD_PRELOAD") + "|" +
			os.Getenv("DYLD_INSERT_LIBRARIES") + "|" + os.Getenv("GLIBC_TUNABLES"))
	case "wait":
		time.Sleep(5 * time.Second)
	case "serialized":
		if err := os.WriteFile(os.Getenv("NKD_HELDFILE_READY"), []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		if !waitForPath(os.Getenv("NKD_HELDFILE_RELEASE"), 5*time.Second) {
			t.Fatal("serialized helper was not released")
		}
		if _, err := os.ReadFile(os.Args[len(os.Args)-1]); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown held-file helper mode %q", mode)
	}
}

func waitForPath(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func TestOpenHoldsOriginalAfterPathReplacementAndRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	held, err := Open(path, Options{Label: "input", MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	backup := filepath.Join(dir, "original")
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := held.ReadAll(1024)
	if err != nil || string(raw) != "original" {
		t.Fatalf("held bytes=%q err=%v", raw, err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link, Options{Label: "link", MaxBytes: 1024}); err == nil {
		t.Fatal("final-component symlink was accepted")
	}
}

func TestImmutableWriteRejectedAndCloseIsIdempotent(t *testing.T) {
	held, err := FromBytes([]byte("immutable"), Options{Label: "immutable", MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := held.OSFile().WriteAt([]byte("X"), 0); err == nil {
		t.Fatal("immutable held descriptor accepted a write")
	}
	if err := held.AssertUnchanged(); err != nil {
		t.Fatal(err)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	if err := held.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

type cancelingReader struct {
	cancel context.CancelFunc
	sent   bool
}

func (r *cancelingReader) Read(buffer []byte) (int, error) {
	if r.sent {
		return 0, io.EOF
	}
	r.sent = true
	for i := range buffer {
		buffer[i] = 'x'
	}
	r.cancel()
	return len(buffer), nil
}

type cancelingWriter struct {
	cancel context.CancelFunc
	wrote  bool
}

func (w *cancelingWriter) Write(buffer []byte) (int, error) {
	if !w.wrote {
		w.wrote = true
		w.cancel()
	}
	return len(buffer), nil
}

func TestContextAwareHeldIOStopsBetweenChunks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	_, err := FromReaderContext(ctx, &cancelingReader{cancel: cancel}, Options{
		Label: "cancel-reader", MaxBytes: 4 << 20,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("context-aware capture returned %v", err)
	}

	held, err := FromBytes(bytes.Repeat([]byte("x"), 2<<20), Options{
		Label: "cancel-write", MaxBytes: 3 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	writeCtx, writeCancel := context.WithCancel(context.Background())
	if _, err := held.WriteToContext(writeCtx, &cancelingWriter{cancel: writeCancel}); !errors.Is(err, context.Canceled) {
		t.Fatalf("context-aware materialization returned %v", err)
	}
	verifyCtx, verifyCancel := context.WithCancel(context.Background())
	verifyCancel()
	if err := held.AssertUnchangedContext(verifyCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("context-aware verification returned %v", err)
	}
}

func TestRunContextExecutesHeldMachOOrELFAndReusesInput(t *testing.T) {
	executablePath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	configured := filepath.Join(t.TempDir(), "helper")
	raw, err := os.ReadFile(executablePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configured, raw, 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := Open(configured, Options{
		Label: "helper", Executable: true, MaxBytes: int64(len(raw)) + 1,
	})
	if runtime.GOOS == "darwin" {
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("Darwin executable holding must fail closed: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer executable.Close()
	if err := os.Rename(configured, configured+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configured, []byte("attacker"), 0o700); err != nil {
		t.Fatal(err)
	}
	input, err := FromBytes([]byte("held-input"), Options{Label: "input", MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	inputPath, err := ChildPath(1)
	if err != nil {
		t.Fatal(err)
	}
	environment := append(os.Environ(), "NKD_HELDFILE_HELPER=1", "NKD_HELDFILE_MODE=read")
	for invocation := 0; invocation < 2; invocation++ {
		var stdout bytes.Buffer
		err := RunContext(context.Background(), executable,
			[]string{"-test.run=^TestHeldProcessHelper$", "--", inputPath},
			RunOptions{Stdout: &stdout, Env: environment}, input)
		if err != nil {
			t.Fatalf("held invocation %d: %v", invocation, err)
		}
		if !strings.Contains(stdout.String(), "held-input") {
			t.Fatalf("held invocation %d output=%q", invocation, stdout.String())
		}
	}
}

func TestRunContextScrubsLoaderEnvironmentAndHonorsCancellation(t *testing.T) {
	executablePath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(executablePath)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := Open(executablePath, Options{
		Label: "helper", Executable: true, MaxBytes: info.Size() + 1,
	})
	if runtime.GOOS == "darwin" {
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("Darwin executable holding must fail closed: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer executable.Close()
	var stdout bytes.Buffer
	environment := append(os.Environ(),
		"NKD_HELDFILE_HELPER=1", "NKD_HELDFILE_MODE=environment",
		"LD_PRELOAD=attacker", "DYLD_INSERT_LIBRARIES=attacker", "GLIBC_TUNABLES=attacker")
	if err := RunContext(context.Background(), executable,
		[]string{"-test.run=^TestHeldProcessHelper$"}, RunOptions{Stdout: &stdout, Env: environment}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), "attacker") {
		t.Fatalf("loader environment reached held child: %q", stdout.String())
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = RunContext(ctx, executable, []string{"-test.run=^TestHeldProcessHelper$"}, RunOptions{
		Env: append(os.Environ(), "NKD_HELDFILE_HELPER=1", "NKD_HELDFILE_MODE=wait"),
	})
	if err == nil {
		t.Fatal("canceled held command succeeded")
	}
}

func TestRunContextSerializesSharedInputUntilChildExit(t *testing.T) {
	if runtime.GOOS == "darwin" {
		return // executable authority is deliberately unsupported on Darwin
	}
	executablePath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(executablePath)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := Open(executablePath, Options{Label: "helper", Executable: true, MaxBytes: info.Size() + 1})
	if err != nil {
		t.Fatal(err)
	}
	defer executable.Close()
	input, err := FromBytes([]byte("shared"), Options{Label: "shared", MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	inputPath, err := ChildPath(1)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ready1, release1 := filepath.Join(dir, "ready-1"), filepath.Join(dir, "release-1")
	ready2, release2 := filepath.Join(dir, "ready-2"), filepath.Join(dir, "release-2")
	run := func(ready, release string) error {
		environment := append(os.Environ(), "NKD_HELDFILE_HELPER=1", "NKD_HELDFILE_MODE=serialized",
			"NKD_HELDFILE_READY="+ready, "NKD_HELDFILE_RELEASE="+release)
		return RunContext(context.Background(), executable,
			[]string{"-test.run=^TestHeldProcessHelper$", "--", inputPath},
			RunOptions{Env: environment}, input)
	}
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { first <- run(ready1, release1) }()
	if !waitForPath(ready1, 5*time.Second) {
		t.Fatal("first held child did not start")
	}
	go func() { second <- run(ready2, release2) }()
	time.Sleep(75 * time.Millisecond)
	if _, err := os.Stat(ready2); err == nil {
		t.Fatal("second child started while the first still owned their shared input")
	}
	if err := os.WriteFile(release1, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if !waitForPath(ready2, 5*time.Second) {
		t.Fatal("second held child did not start after the first exited")
	}
	if err := os.WriteFile(release2, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
}

func TestOutputSealStreamsSparseLogicalBytesAndEnforcesBound(t *testing.T) {
	const offset = int64(4<<20 + 17)
	output, err := NewOutput("sparse", offset+1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := output.OSFile().WriteAt([]byte{0x7f}, offset); err != nil {
		t.Fatal(err)
	}
	sealed, err := output.Seal()
	if err != nil {
		t.Fatal(err)
	}
	defer sealed.Close()
	if sealed.Size() != offset+1 {
		t.Fatalf("sealed size=%d", sealed.Size())
	}
	last := make([]byte, 1)
	if _, err := sealed.ReadAt(last, offset); err != nil || last[0] != 0x7f {
		t.Fatalf("last byte=%x err=%v", last, err)
	}
	hash := sha256.New()
	if _, err := sealed.WriteTo(hash); err != nil {
		t.Fatal(err)
	}
	want := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if sealed.Digest() != want {
		t.Fatalf("sealed digest=%s want=%s", sealed.Digest(), want)
	}

	tooLarge, err := NewOutput("too-large", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer tooLarge.Close()
	if _, err := tooLarge.OSFile().Write([]byte("xx")); err != nil {
		t.Fatal(err)
	}
	if _, err := tooLarge.Seal(); err == nil {
		t.Fatal("oversized held output was sealed")
	}
}

func TestSanitizedEnvironment(t *testing.T) {
	got := SanitizedEnvironment([]string{
		"OK=value", "LD_PRELOAD=x", "LD_LIBRARY_PATH=x", "DYLD_FOO=x",
		"GLIBC_TUNABLES=x", "LIBPATH=x", "SHLIB_PATH=x",
	})
	if strings.Join(got, ",") != "OK=value" {
		t.Fatalf("sanitized environment=%v", got)
	}
}
